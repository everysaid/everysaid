import pytest

from everysaid import text
from everysaid.core import changes, queries
from everysaid.errors import UserError


def test_fold():
    assert text.fold("Καλημέρα ΦΊΛΟΣ") == "καλημερα φιλοσ"
    assert text.query('a* "b') == '"a"* """b"'


def test_chats_and_streams(store):
    chats = queries.chats(store)
    kinds = {c["type"] for c in chats}
    assert {"person", "group"} <= kinds
    assert chats == sorted(chats, key=lambda c: (c["pinned"], c["last_ts"]), reverse=True)
    person = next(c for c in chats if c["type"] == "person" and len(c["services"]) > 1)
    page = queries.stream(store, person["id"], limit=40)
    items = page["items"]
    assert items and [i["ts"] for i in items] == sorted(i["ts"] for i in items)
    assert {i["service"] for i in items} <= set(person["services"]) | {"phone"}
    seen = {i["cursor"] for i in items}
    older = queries.stream(store, person["id"], before=items[0]["cursor"], limit=40)
    assert not seen & {i["cursor"] for i in older["items"]}
    assert all(i["ts"] <= items[0]["ts"] for i in older["items"])
    newer = queries.stream(store, person["id"], after=older["items"][-1]["cursor"], limit=10)
    assert newer["items"][0]["cursor"] == items[0]["cursor"]


def test_a_stream_without_some_services(store):
    person = next(c for c in queries.chats(store) if c["type"] == "person" and len(c["services"]) > 1)
    off = person["services"][0]
    page = queries.stream(store, person["id"], limit=200, hidden={off})
    assert page["items"] and off not in {i["service"] for i in page["items"]}
    older = queries.stream(store, person["id"], before=page["items"][0]["cursor"], limit=200, hidden={off})
    assert all(i["service"] != off for i in older["items"])
    assert queries.stream(store, person["id"], limit=200, hidden={"no-such-service"}) == queries.stream(store, person["id"], limit=200)


def test_walk_whole_stream_once(store):
    chat = next(c for c in queries.chats(store) if c["type"] == "group")
    seen, cursor = [], None
    while True:
        page = queries.stream(store, chat["id"], before=cursor, limit=97)
        seen = page["items"] + seen
        if not page["has_older"]:
            break
        cursor = page["items"][0]["cursor"]
    total = store.read().execute("SELECT count(*) FROM message WHERE conversation_id = ?",
                                 (chat["conversation_id"],)).fetchone()[0]
    assert len(seen) == total == len({i["cursor"] for i in seen})


def test_search_ignores_accents(store):
    a = queries.search(store, "καλημερα")
    b = queries.search(store, "ΚΑΛΗΜΈΡΑ")
    assert a["total"] == b["total"] > 0
    assert any(h[1] for h in a["items"][0]["highlight"])
    assert a["items"][0]["chat_id"]


def test_person_and_names(store):
    chat = next(c for c in queries.chats(store) if c["type"] == "person")
    p = queries.person(store, chat["person_id"])
    assert p["name"] == chat["title"] and p["handles"]
    changes.set_person(store, p["id"], name="Κάποιος Άλλος")
    assert queries.person(store, p["id"])["name"] == "Κάποιος Άλλος"


def test_merge_and_split(store):
    persons = [c for c in queries.chats(store) if c["type"] == "person"][:2]
    a, b = persons[0]["person_id"], persons[1]["person_id"]
    n_before = len(queries.chats(store))
    changes.merge_people(store, a, b)
    chats = queries.chats(store)
    assert len(chats) == n_before - 1
    merged = next(c for c in chats if c["id"] == f"p{a}")
    assert set(persons[1]["services"]) <= set(merged["services"])
    moved = queries.person(store, a)["handles"][-1]["address_id"]
    changes.set_chat_state(store, f"p{a}", archived=True)
    pid = changes.split_address(store, moved)
    assert len(queries.chats(store, include_archived=True)) == n_before
    assert queries.chat(store, f"p{pid}")["archived"]          # split off an archived chat: archived too


def test_chat_state_and_unread(store):
    chat = next(c for c in queries.chats(store) if c["unread"])
    changes.set_chat_state(store, chat["id"], pinned=True, read_until="now")
    top = queries.chats(store)[0]
    assert top["id"] == chat["id"] and top["pinned"] and top["unread"] == 0
    changes.set_chat_state(store, chat["id"], archived=True)
    assert chat["id"] not in {c["id"] for c in queries.chats(store)}


def test_media_and_decisions(store):
    m = queries.media(store, kind="image")["items"]
    assert m and m[0]["available"] == "local"
    changes.decide_media(store, m[0]["sha256"], "keep")
    assert queries.media(store, kind="image")["items"][0]["decision"] == "keep"


def test_calls_stats_timeline(store):
    assert queries.calls(store, missed=True)["items"]
    s = queries.stats(store)
    assert s["messages"] > 1000 and s["people"] > 10
    assert s["top_groups"] and {queries.chat(store, g["chat_id"])["type"] for g in s["top_groups"]} == {"group"}
    last = s["last"]
    t = queries.timeline(store, last - 86400_000, last + 1)
    assert t["items"]


def test_a_search_of_dates_alone_gives_everything_of_those_days(store):
    db = store.read()
    ts = db.execute("SELECT max(ts) FROM message").fetchone()[0]
    since, until = ts - 30 * 86400_000, ts + 1             # some weeks, with messages and calls
    r = queries.search(store, "", since=since, until=until, limit=1000)
    n_msgs = db.execute("SELECT count(*) FROM message WHERE ts >= ? AND ts < ?", (since, until)).fetchone()[0]
    n_calls = db.execute("SELECT count(*) FROM call WHERE ts >= ? AND ts < ?", (since, until)).fetchone()[0]
    assert r["total"] == n_msgs + n_calls == len(r["items"]) and n_msgs and n_calls
    assert [i["ts"] for i in r["items"]] == sorted(i["ts"] for i in r["items"])        # oldest first
    assert all(i["chat_id"] and i["chat_title"] for i in r["items"] if i["type"] == "call")
    assert sum(c["count"] for c in r["chats"]) <= r["total"]
    chat = next(i["chat_id"] for i in r["items"] if i["type"] == "call")
    one = queries.search(store, "", chat_id=chat, since=since, until=until, limit=1000)
    assert one["items"] and all(i["chat_id"] == chat for i in one["items"])
    assert {i["type"] for i in queries.search(store, "", kind="text", since=since, until=until)["items"]} == {"message"}
    assert queries.search(store, "")["items"] == []                                  # no words, no dates: nothing


def test_context(store):
    mid = store.read().execute("SELECT id FROM message ORDER BY id LIMIT 1 OFFSET 500").fetchone()[0]
    ctx = queries.context(store, mid, n=5)
    assert any(i["type"] == "message" and i["id"] == mid for i in ctx["items"])


def test_name_order(store):
    from everysaid.core.names import people
    with store.write() as db:
        pid = db.execute("INSERT INTO person DEFAULT VALUES").lastrowid
        phone = db.execute("INSERT INTO address (kind_id, value) VALUES ((SELECT id FROM address_kind WHERE name = 'phone'), "
                           "'+15559990000')").lastrowid
        other = db.execute("INSERT INTO address (kind_id, value) VALUES ((SELECT id FROM address_kind WHERE name = 'phone'), "
                           "'+15559990001')").lastrowid
        for aid in (phone, other):
            db.execute("INSERT INTO person_address (address_id, person_id) VALUES (?, ?)", (aid, pid))
        service = dict(db.execute("SELECT name, id FROM service"))
        for aid, svc, kind, name in ((phone, "telegram", "profile", "Tg Name"), (phone, "whatsapp", "profile", "Push"),
                                     (phone, "whatsapp", "book", "Book Name"), (other, "whatsapp", "book", "Book Name")):
            db.execute("INSERT INTO handle_name (address_id, service_id, kind, name, first_seen, last_seen) "
                       "VALUES (?, ?, ?, ?, 1, 1)", (aid, service[svc], kind, name))
    ppl = people(store)
    assert ppl.info(pid) == ("Book Name", "whatsapp/book")      # by the plugins' weights
    assert not ppl.self_named(pid)
    assert {a["name"] for a in ppl.aka(pid)} == {"Tg Name", "Push"}
    changes.set_setting(store, "name_order", ["telegram/profile"])
    assert people(store).info(pid) == ("Tg Name", "telegram/profile")
    assert people(store).self_named(pid)
    changes.set_person(store, pid, name_source="whatsapp/profile")         # pinned to one source
    assert people(store).name(pid) == "Push"
    changes.set_person(store, pid, name_source=f"address:{other}")         # pinned to one handle
    assert people(store).name(pid) == "Book Name"
    changes.set_person(store, pid, name="Mine")
    assert people(store).info(pid) == ("Mine", "user")                     # the user's own name comes first


def test_chat_state_from_services_and_the_user(store):
    import time
    chat = next(c for c in queries.chats(store) if c["type"] == "person")
    conv = queries.chat(store, chat["id"])["conversations"][0]
    now = int(time.time() * 1000)
    with store.write() as db:
        iid = db.execute("SELECT id FROM plugin_instance WHERE plugin = 'whatsapp-bridge'").fetchone()[0]
        db.execute("INSERT OR REPLACE INTO state_report VALUES (?, ?, 'muted', -1, ?, ?)", (conv, iid, now, now))
    assert queries.chat(store, chat["id"])["muted"]                          # muted by a service
    changes.set_chat_state(store, chat["id"], muted=False)                  # the user's later choice wins
    assert not queries.chat(store, chat["id"])["muted"]
    with store.write() as db:                                               # the service changes it again, later
        db.execute("UPDATE state_report SET value = -1, changed_at = ?, observed_at = ? WHERE conversation_id = ?",
                   (now + 10_000_000, now + 10_000_000, conv))
    assert queries.chat(store, chat["id"])["muted"]
    changes.set_chat_state(store, chat["id"], muted=False, always=True)     # unless the user said always
    assert not queries.chat(store, chat["id"])["muted"]
    changes.set_chat_state(store, chat["id"], muted=None)                   # follow the services again
    assert queries.chat(store, chat["id"])["muted"]


def test_archived_is_the_apps_own_and_a_merge_keeps_in_view(store):
    a, b = [c for c in queries.chats(store) if c["type"] == "person"][:2]
    changes.set_chat_state(store, a["id"], archived=True)
    assert queries.chat(store, a["id"])["archived"] and not queries.chat(store, b["id"])["archived"]
    changes.merge_people(store, a["person_id"], b["person_id"])             # one in view: the whole person in view
    assert not queries.chat(store, a["id"])["archived"]
    c, d = [c for c in queries.chats(store) if c["type"] == "person"][:2]
    for x in (c, d):
        changes.set_chat_state(store, x["id"], archived=True)
    changes.merge_people(store, c["person_id"], d["person_id"])             # both archived: archived
    assert queries.chat(store, c["id"])["archived"]

def test_search_inside_words_whole_words_and_case(store):
    total = lambda q, **kw: queries.search(store, q, **kw)["total"]
    assert total("λημερ") > 0                               # inside a word (Καλημέρα), by default
    assert total("λημερ", whole=True) == 0                  # not a word of its own
    assert total("καλημερα", whole=True) == total("Καλημέρα", whole=True) > 0
    assert total("ΚΑΛΗΜΈΡΑ", case=True) == 0 < total("ΚΑΛΗΜΈΡΑ")      # as typed, or not
    assert total("Καλημέρα", case=True) > 0
    assert total("να") >= 0                                 # two letters: at the start of words
    hit = queries.search(store, "λημερ", limit=1)["items"][0]
    assert any(m and "λημέρ" in piece for piece, m in hit["highlight"])          # the part is marked


def test_search_says_where_it_found_and_filters_by_it(store):
    for case in (False, True):
        r = queries.search(store, "Καλημέρα", case=case)
        assert r["chats"] and sum(c["count"] for c in r["chats"]) == r["total"]
        top = r["chats"][0]
        only = queries.search(store, "Καλημέρα", chat_id=top["chat_id"], case=case)
        assert only["total"] == top["count"] and all(i["chat_id"] == top["chat_id"] for i in only["items"])
        assert [c["chat_id"] for c in only["chats"]] == [c["chat_id"] for c in r["chats"]]   # still every chat


def test_an_archived_chat_is_found_by_name_only_among_the_archived(store):
    chat = next(c for c in queries.chats(store) if c["type"] == "person")
    changes.set_chat_state(store, chat["id"], archived=True)
    assert chat["id"] not in {c["id"] for c in queries.chats(store)}                      # not in the list
    assert chat["id"] not in {c["id"] for c in queries.chats(store, q=chat["title"][:4])}    # nor looked for there
    found = {c["id"]: c for c in queries.chats(store, q=chat["title"][:4], include_archived=True)}
    assert chat["id"] in found and found[chat["id"]]["archived"]                             # but among them


def test_a_search_is_in_the_archived_chats_or_in_the_others(store):
    every = queries.search(store, "καλημερα", limit=200)
    chat = every["chats"][0]["chat_id"]
    changes.set_chat_state(store, chat, archived=True)
    out = queries.search(store, "καλημερα", archived=False, limit=200)
    inside = queries.search(store, "καλημερα", archived=True, limit=200)
    assert out["total"] + inside["total"] == every["total"] and inside["total"]
    assert {i["chat_id"] for i in inside["items"]} == {chat} and chat not in {i["chat_id"] for i in out["items"]}
    assert [c["chat_id"] for c in inside["chats"]] == [chat]
    assert queries.search(store, "καλημερα", chat_id=chat, archived=False)["total"] == inside["total"]   # asked for: searched
    ts = inside["items"][0]["ts"]                                   # dates alone too, calls with them
    days = dict(since=ts - 400 * 86400_000, until=ts + 1, limit=1000)
    a, b, c = (queries.search(store, "", archived=x, **days) for x in (None, False, True))
    assert a["total"] == b["total"] + c["total"] and c["total"] and all(i["chat_id"] == chat for i in c["items"])


def test_a_name_is_found_by_parts_of_its_words(store):
    chat = next(c for c in queries.chats(store) if c["type"] == "person" and len(c["title"].split()) > 1
                and all(len(w) > 3 for w in c["title"].split()))
    first, last = chat["title"].split()[:2]
    q = f"{last[:-1]} {first[:-1].upper()}"                     # each word cut short, the other way round
    assert chat["id"] in {c["id"] for c in queries.chats(store, q=q)}
    assert chat["person_id"] in {p["id"] for p in queries.people_list(store, q)["items"]}


def test_archived_is_decided_once_when_a_chat_is_first_seen(store):
    from everysaid.archive import Archive
    import time
    p = next(c for c in queries.chats(store) if c["type"] == "person" and len(c["services"]) > 1)
    convs = queries.chat(store, p["id"])["conversations"]
    g = next(c for c in queries.chats(store) if c["type"] == "group")
    seen = next(c for c in queries.chats(store) if c["type"] == "person" and c["id"] != p["id"])
    a = Archive(store.path)
    try:
        # every chat of the demo was seen when it was made: one no source reported on, not archived
        assert a.db.execute("SELECT value FROM chat_state WHERE chat = ? AND field = 'archived'", (seen["id"],)).fetchone() == (0,)
        a.db.execute("DELETE FROM chat_state WHERE chat IN (?, ?)", (p["id"], g["id"]))      # these two: new
        src = a.db.execute("SELECT id FROM source WHERE instance_id IS NOT NULL LIMIT 1").fetchone()[0]
        now = int(time.time() * 1000)
        # a person reached on two services, one archiving their chat: they stay in view
        a.report_state(src, convs[0], "archived", 1, now)
        a.report_state(src, convs[1], "archived", 0, now)
        a.report_state(src, g["conversation_id"], "archived", 1, now)
        a.report_state(src, seen["conversation_id"], "archived", 1, now)                  # seen before: no change
        a.init_archived()
        a.db.commit()
    finally:
        a.db.close()
    assert not queries.chat(store, p["id"])["archived"]          # one in view: in view
    assert queries.chat(store, g["id"])["archived"]              # the group archived there: archived here
    assert not queries.chat(store, seen["id"])["archived"]
    changes.set_chat_state(store, g["id"], archived=False)       # then ours alone
    a = Archive(store.path)
    try:
        a.report_state(src, g["conversation_id"], "archived", 1, now + 1000)
        a.init_archived()
        a.db.commit()
    finally:
        a.db.close()
    assert not queries.chat(store, g["id"])["archived"]


def test_stats_leave_archived_chats_out_unless_asked(store):
    every = queries.stats(store, include_archived=True)
    chat = next(c for c in queries.chats(store) if c["type"] == "person" and c["id"] in {p["chat_id"] for p in every["top_people"]})
    assert queries.stats(store) == every                            # nothing archived yet
    changes.set_chat_state(store, chat["id"], archived=True)
    shown = queries.stats(store)
    assert shown["people"] == every["people"] - 1
    assert shown["messages"] < every["messages"]
    assert chat["id"] not in {p["chat_id"] for p in shown["top_people"]}
    assert queries.stats(store, include_archived=True) == every


def test_an_archived_chat_stays_archived_and_says_nothing_of_new_messages(store):
    import time
    from everysaid.core import changes
    from everysaid.server.host import Host
    db = store.read()
    by_chat = {}            # two chats, one conversation of each
    for (c,) in db.execute("SELECT id FROM conversation c WHERE NOT is_group AND EXISTS "
                           "(SELECT 1 FROM message m WHERE m.conversation_id = c.id AND NOT m.outgoing)"):
        by_chat.setdefault(queries.chat_of_conversation(store, c), c)
    (archived, other), convs = list(by_chat)[:2], list(by_chat.values())[:2]
    changes.set_chat_state(store, archived, archived=True)
    first = db.execute("SELECT max(id) FROM message").fetchone()[0]
    with store.write() as w:
        for conv in convs:
            w.execute("INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text) "
                      "SELECT service_id, conversation_id, ?, 0, sender_id, kind_id, 'hi' FROM message "
                      "WHERE conversation_id = ? AND NOT outgoing LIMIT 1", (int(time.time() * 1000), conv))
    last = store.read().execute("SELECT max(id) FROM message").fetchone()[0]
    host, pushed = Host(store), []
    host.push = type("P", (), {"notify": lambda self, s, incoming: pushed.extend(incoming)})()
    event = host._describe_new({"type": "new", "messages": (first, last)})
    assert event["chats"] == {archived: 1, other: 1}            # both shown as they come
    assert queries._states(store)[archived][2]                  # still archived
    assert [c for c, *_ in pushed] == [other]                   # and only the other one notifies


def test_groups_merge_into_one_chat_and_split_again(store):
    gs = [c for c in queries.chats(store, include_archived=True) if c["type"] == "group"][:3]
    a, b, c = (g["id"] for g in gs)
    before = {g["id"]: queries.chat(store, g["id"]) for g in gs}
    changes.set_chat_state(store, b, archived=True, pinned=True)
    assert changes.merge_groups(store, a, b) == a
    merged = queries.chat(store, a)
    assert queries.chat(store, b) is None
    assert sorted(merged["conversations"]) == sorted(before[a]["conversations"] + before[b]["conversations"])
    assert {g["conversation_id"] for g in merged["groups"]} == set(merged["conversations"])
    assert merged["pinned"] and not merged["archived"]          # its choices kept; archived only if both were
    assert merged["title"] == max((before[a], before[b]), key=lambda x: x["last_ts"])["title"]   # the latest one's name
    names = {m["name"] for x in (a, b) for m in before[x]["members"]}
    assert {m["name"] for m in merged["members"]} == names
    items = queries.stream(store, a, limit=1000)["items"]
    assert {i["conversation_id"] for i in items if i["type"] == "message"} == set(merged["conversations"])
    changes.merge_groups(store, a, c)                              # a third
    assert len(queries.chat(store, a)["groups"]) == 3
    with pytest.raises(UserError):
        changes.merge_groups(store, a, a)

    # the one whose id the chat has leaves: the others keep the chat, under another id
    head = int(a[1:])
    rest = changes.split_group(store, a, head)
    assert rest != a and queries.chat(store, rest)["pinned"]
    assert queries.chat(store, a)["conversations"] == before[a]["conversations"]
    for g in queries.chat(store, rest)["groups"]:
        rest = changes.split_group(store, rest, g["conversation_id"])      # (its id changes with its head)
    assert {x["id"] for x in queries.chats(store, include_archived=True) if x["type"] == "group"} >= {a, b, c}


def test_groups_alike_are_suggested_until_turned_down(store):
    from everysaid.archive import Archive
    a, b = [c for c in queries.chats(store, include_archived=True) if c["type"] == "group"][:2]
    ca, cb = (queries.chat(store, x["id"])["conversation_id"] for x in (a, b))
    arch = Archive(store.path)
    try:                    # b gets a's members: the same people, on another group
        arch.db.execute("INSERT OR IGNORE INTO conversation_member SELECT ?, address_id FROM conversation_member "
                        "WHERE conversation_id = ?", (cb, ca))
        arch.db.execute("DELETE FROM conversation_member WHERE conversation_id = ? AND address_id NOT IN "
                        "(SELECT address_id FROM conversation_member WHERE conversation_id = ?)", (cb, ca))
        arch.db.commit()
    finally:
        arch.db.close()
    found = queries.group_suggestions(store, chat_id=a["id"])
    assert any({x["chat_id"] for x in s["chats"]} == {a["id"], b["id"]} and "members" in s["why"] for s in found)
    changes.dismiss_group_merge(store, [a["id"], b["id"]])
    assert not any({x["chat_id"] for x in s["chats"]} == {a["id"], b["id"]} for s in queries.group_suggestions(store))


def test_people_without_a_name(store):
    every = {c["title"] for c in queries.chats(store)}
    named = {c["title"] for c in queries.chats(store, unnamed=False)}
    # the numbers no source names: gone, but for the one with an unread message; those with calls
    # only are no chats at all (the calls have their page)
    assert every - named == {"+1 555-010-0010", "katerina.oikonomou@example.com"}
    assert not {"+1 555-010-0000", "+1 555-010-0002"} & every
    assert "+1 555-010-0011" in named
    assert [c["title"] for c in queries.chats(store, q="555-010-0010", unnamed=False)] == ["+1 555-010-0010"]
    all_calls = queries.calls(store, limit=10000)["items"]
    calls = queries.calls(store, limit=10000, unnamed=False)["items"]
    assert len(all_calls) - len(calls) == 6                 # five of theirs and the hidden number's
    assert all(c["chat_id"] for c in calls)
    pid = queries.people_list(store, q="555-010-0002")["items"][0]["id"]
    assert len(queries.calls(store, chat_id=f"p{pid}", unnamed=False)["items"]) == 2     # one chat's: all
    names = {p["name"] for p in queries.people_list(store, limit=1000, unnamed=False)["items"]}
    assert not any(n.startswith("+1 555-010-") for n in names)
    assert queries.people_list(store, q="555-010-0003", unnamed=False)["total"] == 1


def test_names_that_sound_the_same():
    same = [("Ελένη Ιωάννου", "Eleni Ioannou"), ("Θανάσης Σπύρος", "Spyros Thanasis"),
            ("Ευάγγελος Φίλιππος", "evangelos filippos"), ("Ντίνος Μπάμπης", "Dinos Babis")]
    assert all(queries._skeleton(a) == queries._skeleton(b) for a, b in same)
    assert queries._skeleton("Ελένη Ιωάννου") != queries._skeleton("Olivia Ιωάννου")


def test_merge_suggestions_and_not_the_same(store):
    found = {tuple(sorted(p["name"] for p in s["people"])): s["why"] for s in queries.merge_suggestions(store)}
    assert found[("Eleni Ioannou", "Ελένη Ιωάννου")] == ["similar"]
    assert found[("Νίκος Γεωργίου", "Νίκος Γεωργίου")] == ["book"]
    s = next(s for s in queries.merge_suggestions(store) if s["why"] == ["similar"])
    changes.dismiss_merge(store, [p["id"] for p in s["people"]])
    assert not any(x["why"] == ["similar"] for x in queries.merge_suggestions(store))


def test_many_merge_decisions_at_once(store):
    found = {s["why"][0]: [p["id"] for p in s["people"]] for s in queries.merge_suggestions(store)}
    book, similar = found["book"], found["similar"]
    # the book pair merged; the similar pair apart, one of them named through the merged person
    assert changes.apply_merges(store, [book], [[similar[0], similar[1]], [book[1], similar[0]]]) == (1, 2)
    assert queries.person(store, book[1]) is None
    assert not queries.merge_suggestions(store)
    apart = queries.merges_dismissed(store)
    assert {(x["a"]["id"], x["b"]["id"]) for x in apart} == {tuple(sorted(similar)), tuple(sorted((book[0], similar[0])))}
    changes.undismiss_merge(store, *similar)                        # turned down by mistake
    assert [s["why"] for s in queries.merge_suggestions(store)] == [["similar"]]
    assert queries.merge_suggestions(store, recent=True)[0]["people"][0]["recent"]


def test_nothing_in_it_is_not_listed(store):
    with store.write() as db:              # what a source may leave: an empty chat, a handle of no one
        sid = db.execute("SELECT id FROM service WHERE name = 'whatsapp'").fetchone()[0]
        kid = db.execute("SELECT id FROM address_kind WHERE name = 'id'").fetchone()[0]
        db.execute("INSERT INTO conversation (service_id, key, title, is_group) VALUES (?, 'empty', 'Empty chat', 1)", (sid,))
        aid = db.execute("INSERT INTO address (kind_id, value, service_id) VALUES (?, 'nobody', ?)", (kid, sid)).lastrowid
        pid = db.execute("INSERT INTO person DEFAULT VALUES").lastrowid
        db.execute("INSERT INTO person_address (address_id, person_id) VALUES (?, ?)", (aid, pid))
    assert "Empty chat" not in {c["title"] for c in queries.chats(store)}
    assert pid not in {p["id"] for p in queries.people_list(store, limit=1000, q="nobody")["items"]}


def test_people_without_a_name_to_name(store):
    r = queries.unnamed_people(store, limit=100)
    names = [p["name"] for p in r["items"]]
    assert r["total"] == len(names) and "+1 555-010-0010" in names
    sizes = [p["stats"]["messages"] for p in r["items"]]
    assert sizes == sorted(sizes, reverse=True) and all("recent" in p for p in r["items"])
    first = r["items"][0]
    changes.set_person(store, first["id"], name="The courier")
    assert first["id"] not in {p["id"] for p in queries.unnamed_people(store, limit=100)["items"]}


def test_the_chat_list_filtered(store):
    every = queries.chats(store, unnamed=True)
    sizes = dict(store.read().execute("SELECT conversation_id, count(*) FROM message GROUP BY conversation_id"))
    index, _ = queries._chat_index(store)
    many = queries.chats(store, unnamed=True, min_messages=100)
    assert many and len(many) < len(every)
    assert all(sum(sizes.get(c, 0) for c in index[x["id"]]["conversations"]) >= 100 for x in many)
    few = queries.chats(store, unnamed=True, max_messages=99)
    assert {x["id"] for x in few} | {x["id"] for x in many} == {x["id"] for x in every}
    assert not {x["id"] for x in few} & {x["id"] for x in many}
    wa = queries.chats(store, unnamed=True, with_services=["whatsapp"])
    assert wa and all("whatsapp" in x["services"] for x in wa)
    no_wa = queries.chats(store, unnamed=True, without_services=["whatsapp"])
    assert {x["id"] for x in wa} | {x["id"] for x in no_wa} == {x["id"] for x in every}
    one = every[0]
    if one["person_id"]:
        assert [x["id"] for x in queries.chats(store, people_only={one["person_id"]})] == [one["id"]]


def test_hidden_services_show_nowhere(store):
    from everysaid.core import changes
    before = queries.chats(store, unnamed=True)
    assert any("whatsapp" in c["services"] for c in before)
    assert queries.search(store, "καλημερα", service="whatsapp")["total"] > 0
    changes.set_setting(store, "hidden_services", ["whatsapp"])
    after = queries.chats(store, unnamed=True)
    assert after and not any("whatsapp" in c["services"] for c in after)
    assert queries.search(store, "καλημερα", service="whatsapp")["total"] == 0
    assert queries.search(store, "καλημερα")["total"] > 0
    assert all(c["service"] != "whatsapp" for c in queries.calls(store, limit=1000)["items"])
    for c in after[:10]:
        assert all(i.get("service") != "whatsapp" for i in queries.stream(store, c["id"])["items"])
    changes.set_setting(store, "hidden_services", [])
    assert len(queries.chats(store, unnamed=True)) == len(before)


def test_notes_to_self_are_one_chat_across_services(store):
    """Viber's notes and a Telegram chat with oneself (no one else in either, ever) are one chat;
    a group everyone else left is not notes (others wrote in it)."""
    index, conv_chat = queries._chat_index(store)
    notes = next(c for c in index.values() if c["type"] == "conversation")
    with store.write() as db:
        tg = db.execute("SELECT id FROM service WHERE name = 'telegram'").fetchone()[0]
        own = db.execute("SELECT address_id FROM account LIMIT 1").fetchone()[0]
        cid = db.execute("INSERT INTO conversation (service_id, key, is_group) VALUES (?, 'saved', 0)", (tg,)).lastrowid
        db.execute("INSERT INTO conversation_member VALUES (?, ?)", (cid, own))
        kind = db.execute("SELECT id FROM message_kind WHERE name = 'text'").fetchone()[0]
        db.execute("INSERT INTO message (service_id, conversation_id, ts, outgoing, kind_id, text) "
                   "VALUES (?, ?, 1700000000000, 1, ?, 'a note')", (tg, cid, kind))
        # WhatsApp's chat with oneself, one message of it given as received from the owner (another device)
        wa = db.execute("SELECT id FROM service WHERE name = 'whatsapp'").fetchone()[0]
        wid = db.execute("INSERT INTO conversation (service_id, key, is_group) VALUES (?, 'self', 0)", (wa,)).lastrowid
        db.execute("INSERT INTO conversation_member VALUES (?, ?)", (wid, own))
        db.execute("INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text) "
                   "VALUES (?, ?, 1700000001000, 0, ?, ?, 'from my other phone')", (wa, wid, own, kind))
        # and a notice of the service in it, given as received from its member (the owner)
        system = db.execute("SELECT id FROM message_kind WHERE name = 'system'").fetchone()[0]
        db.execute("INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text) "
                   "VALUES (?, ?, 1700000002000, 0, ?, ?, 'end-to-end encrypted')", (wa, wid, own, system))
        # a chat whose source listed no one, only the owner writing in it: not notes
        nobody = db.execute("INSERT INTO conversation (service_id, key, is_group) VALUES (?, 'someone', 0)", (tg,)).lastrowid
        db.execute("INSERT INTO message (service_id, conversation_id, ts, outgoing, kind_id, text) "
                   "VALUES (?, ?, 1700000003000, 1, ?, 'hello?')", (tg, nobody, kind))
    index, conv_chat = queries._chat_index(store)
    assert conv_chat[cid] == notes["id"] and conv_chat[wid] == notes["id"]
    assert conv_chat[nobody] != notes["id"]
    one = index[notes["id"]]
    assert {"viber", "telegram"} <= one["services"]
    assert queries.chat_title(store, one) in ("Notes", "Σημειώσεις")
    assert any(i.get("text") == "a note" for i in queries.stream(store, notes["id"], limit=200)["items"])


def test_hidden_accounts_hide_the_chats_only_on_them(store):
    from everysaid.core import changes
    with store.write() as db:
        mine = db.execute("SELECT address_id FROM account LIMIT 1").fetchone()[0]
        other = db.execute("INSERT INTO address (kind_id, value) VALUES ((SELECT id FROM address_kind WHERE name = 'email'), "
                           "'me2@example.com')").lastrowid
        db.execute("INSERT INTO account (address_id) VALUES (?)", (other,))
    groups = [c for c in queries.chats(store, unnamed=True) if c["type"] == "group"]
    only, both = groups[0]["conversation_id"], groups[1]["conversation_id"]
    with store.write() as db:
        db.execute("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", (only, other))
        db.executemany("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", [(both, other), (both, mine)])
        db.execute("DELETE FROM conversation_member WHERE conversation_id = ? AND address_id = ?", (only, mine))
    changes.set_setting(store, "hidden_accounts", [other])
    ids = {c["id"] for c in queries.chats(store, unnamed=True)}
    assert groups[0]["id"] not in ids and groups[1]["id"] in ids
    assert queries.search(store, "καλημερα", chat_id=groups[1]["id"])["total"] >= 0
    changes.set_setting(store, "hidden_accounts", [])
    assert groups[0]["id"] in {c["id"] for c in queries.chats(store, unnamed=True)}


def test_groups_with_no_one_else_hidden_when_asked(store):
    groups = [c for c in queries.chats(store, unnamed=True) if c["type"] == "group"]
    g = groups[0]
    with store.write() as db:
        db.execute("DELETE FROM conversation_member WHERE conversation_id = ? AND address_id NOT IN "
                   "(SELECT address_id FROM account)", (g["conversation_id"],))
        db.execute("INSERT OR REPLACE INTO chat_state VALUES (?, 'read_until', ?, 0, 1)", (g["id"], 2 ** 50))
    assert g["id"] in {c["id"] for c in queries.chats(store, unnamed=True)}
    assert g["id"] not in {c["id"] for c in queries.chats(store, unnamed=True, empty_groups=False)}
    assert all(c["id"] in {x["id"] for x in queries.chats(store, unnamed=True, empty_groups=False)} for c in groups[1:])
    if g["title"]:
        assert g["id"] in {c["id"] for c in queries.chats(store, q=g["title"], empty_groups=False)}


def test_short_numbers_hidden_when_asked(store):
    with store.write() as db:
        aid = db.execute("INSERT INTO address (kind_id, value) VALUES ((SELECT id FROM address_kind WHERE name = 'phone'), "
                         "'13800')").lastrowid
        pid = db.execute("INSERT INTO person DEFAULT VALUES").lastrowid
        db.execute("INSERT INTO person_address (address_id, person_id) VALUES (?, ?)", (aid, pid))
        conv = db.execute("INSERT INTO conversation (service_id, key) VALUES ((SELECT id FROM service WHERE name = 'sms'), "
                          "'13800')").lastrowid
        db.execute("INSERT INTO conversation_member VALUES (?, ?)", (conv, aid))
        db.execute("INSERT INTO message (service_id, conversation_id, ts, outgoing, sender_id, kind_id, text) VALUES "
                   "((SELECT id FROM service WHERE name = 'sms'), ?, 1700000000000, 0, ?, "
                   "(SELECT id FROM message_kind WHERE name = 'text'), 'Your code is 1234')", (conv, aid))
    assert f"p{pid}" in {c["id"] for c in queries.chats(store, unnamed=True)}
    assert f"p{pid}" not in {c["id"] for c in queries.chats(store, unnamed=True, short=False)}
    assert f"p{pid}" in {c["id"] for c in queries.chats(store, q="13800", short=False)}
    assert pid not in {p["id"] for p in queries.people_list(store, limit=5000, short=False)["items"]}
