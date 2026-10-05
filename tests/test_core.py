from chronika import text
from chronika.core import changes, queries


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
    changes.split_address(store, moved)
    assert len(queries.chats(store)) == n_before


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
    last = s["last"]
    t = queries.timeline(store, last - 86400_000, last + 1)
    assert t["items"]


def test_context(store):
    mid = store.read().execute("SELECT id FROM message ORDER BY id LIMIT 1 OFFSET 500").fetchone()[0]
    ctx = queries.context(store, mid, n=5)
    assert any(i["type"] == "message" and i["id"] == mid for i in ctx["items"])


def test_name_order(store):
    from chronika.core.names import people
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


def test_an_archived_chat_is_found_by_name(store):
    chat = next(c for c in queries.chats(store) if c["type"] == "person")
    changes.set_chat_state(store, chat["id"], archived=True)
    assert chat["id"] not in {c["id"] for c in queries.chats(store)}                      # not in the list
    found = {c["id"]: c for c in queries.chats(store, q=chat["title"][:4])}
    assert chat["id"] in found and found[chat["id"]]["archived"]                             # but found, marked


def test_archived_starts_from_the_services_then_is_the_apps_alone(store):
    from chronika.archive import Archive
    import time
    a = Archive(store.path)
    try:
        # a person reached on two services, one archiving their chat: they stay in view
        p = next(c for c in queries.chats(store) if c["type"] == "person" and len(c["services"]) > 1)
        convs = queries.chat(store, p["id"])["conversations"]
        g = next(c for c in queries.chats(store) if c["type"] == "group")
        src = a.db.execute("SELECT id FROM source WHERE instance_id IS NOT NULL LIMIT 1").fetchone()[0]
        now = int(time.time() * 1000)
        a.report_state(src, convs[0], "archived", 1, now)
        a.report_state(src, convs[1], "archived", 0, now)
        a.report_state(src, g["conversation_id"], "archived", 1, now)
        a.init_archived()
        a.db.commit()
    finally:
        a.db.close()
    assert not queries.chat(store, p["id"])["archived"]          # one in view: in view
    assert queries.chat(store, g["id"])["archived"]              # the group archived there: archived here
    changes.set_chat_state(store, g["id"], archived=False)       # then ours alone
    a = Archive(store.path)
    try:
        a.report_state(src, g["conversation_id"], "archived", 1, now + 1000)
        a.init_archived()
        a.db.commit()
    finally:
        a.db.close()
    assert not queries.chat(store, g["id"])["archived"]
