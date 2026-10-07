"""Labels (the tone of a chat, who someone is), names found for people without one, and the local
analysis that suggests both."""
import asyncio

from everysaid.core import changes, labels, queries
from everysaid.core.names import people
from everysaid.plugins import analysis


def by_key(store):
    return {lb["key"]: lb for lb in labels.labels(store) if lb["key"]}


def someone(store, unnamed=False):
    ppl = people(store)
    for pid in sorted(ppl.handles):
        if pid not in ppl.me and (ppl.info(pid)[1] == "handle") == unnamed:
            return pid


def knownByEmail(store):
    ppl = people(store)
    return next(pid for pid, hs in ppl.handles.items() if any(v == "katerina.oikonomou@example.com" for _, v, _, _ in hs))


def test_the_lists_the_app_starts_with(store):
    keys = by_key(store)
    assert {"friendly", "professional", "romantic", "friend", "client"} <= set(keys)
    assert "sexual" not in keys                         # one with the romantic
    assert keys["romantic"]["sensitive"] and not keys["friendly"]["sensitive"]
    assert keys["romantic"]["kind"] == "tone" and keys["friend"]["kind"] == "relation"
    words = [w for _, w, _, _ in labels.for_models(store, "tone")]
    assert "romantic" in words and "friend" not in words


def test_the_user_shapes_the_lists(store):
    keys = by_key(store)
    mine = labels.add_label(store, "tone", "Acme", "work talk about the Acme project")
    quiet = labels.add_label(store, "tone", "Holidays")                 # no meaning: never the models'
    words = {w: m for _, w, m, _ in labels.for_models(store, "tone")}
    assert words["Acme"] == "work talk about the Acme project" and "Holidays" not in words
    before = labels.digest(store)
    labels.edit_label(store, keys["formal"]["id"], name="Ψυχρή", meaning="cold")
    assert labels.digest(store) != before
    lb = {x["id"]: x for x in labels.labels(store)}[keys["formal"]["id"]]
    assert lb["name"] == "Ψυχρή" and lb["meaning"] == "cold"
    labels.edit_label(store, keys["formal"]["id"], name=None, meaning=None)   # back to the app's
    lb = {x["id"]: x for x in labels.labels(store)}[keys["formal"]["id"]]
    assert lb["name"] is None and lb["meaning"] is None
    try:
        labels.add_label(store, "tone", "acme")
        assert False, "the same name twice"
    except Exception as e:
        assert getattr(e, "code", "") == "labels.exists"
    ids = [x["id"] for x in labels.labels(store, "tone")]
    labels.order_labels(store, [quiet, *[i for i in ids if i != quiet]])
    assert labels.labels(store, "tone")[0]["id"] == quiet
    labels.remove_label(store, mine)
    assert mine not in {x["id"] for x in labels.labels(store)}


def test_merging_labels_keeps_the_users_word(store):
    keys = by_key(store)
    a, b = someone(store), someone(store, unnamed=True)
    labels.save_analysis(store, a, 50, ["m"], tones=[(keys["romantic"]["id"], 2, 3, "a line")])
    labels.set_person_label(store, a, keys["personal"]["id"], "yes")
    labels.save_analysis(store, b, 50, ["m"], tones=[(keys["personal"]["id"], 2, 3, None)])
    # "romantic" into "personal": a's own yes stays, b's suggestion stays a suggestion
    labels.merge_labels(store, keys["personal"]["id"], keys["romantic"]["id"])
    assert "romantic" not in by_key(store)
    mine = {x["key"]: x["state"] for x in labels.person_labels(store, a)}
    assert mine == {"personal": "yes"}
    assert {x["key"]: x["state"] for x in labels.person_labels(store, b)} == {"personal": "suggested"}
    try:
        labels.merge_labels(store, keys["friend"]["id"], keys["friendly"]["id"])
        assert False, "a tone is not a relation"
    except Exception as e:
        assert getattr(e, "code", "") == "labels.other_kind"


def test_a_persons_labels_and_the_models_suggestions(store):
    keys = by_key(store)
    pid = someone(store)
    labels.set_person_label(store, pid, keys["friend"]["id"], "yes")
    labels.set_person_label(store, pid, keys["colleague"]["id"], "yes")     # one relation: the last
    labels.set_person_label(store, pid, keys["formal"]["id"], "no")
    got = {x["key"]: x["state"] for x in labels.person_labels(store, pid)}
    assert got == {"colleague": "yes"}                  # "no" is not shown
    # the models: their relation gives way to the user's, a "no" is not suggested again
    labels.save_analysis(store, pid, 40, ["m1", "m2"],
                         tones=[(keys["formal"]["id"], 2, 2, None), (keys["friendly"]["id"], 2, 2, None)],
                         relation=(keys["friend"]["id"], 2, 2, None))
    got = {x["key"]: x["state"] for x in labels.person_labels(store, pid)}
    assert got == {"colleague": "yes", "friendly": "suggested"}
    assert {x["key"] for x in labels.person_labels(store, pid, suggested=False)} == {"colleague"}
    assert labels.analysed(store, pid)["messages"] == 40
    # forgetting the analysis keeps the user's word
    labels.forget_analysis(store)
    assert {x["key"]: x["state"] for x in labels.person_labels(store, pid)} == {"colleague": "yes"}
    assert labels.analysed(store, pid) is None
    labels.set_person_label(store, pid, keys["colleague"]["id"], None)
    assert labels.person_labels(store, pid) == []


def test_labels_follow_a_merge_and_a_split(store):
    keys = by_key(store)
    k = knownByEmail(store)
    other = someone(store, unnamed=True)
    labels.save_analysis(store, k, 24, ["m"], name=("Κατερίνα", 1, 1, "Κατερίνα, τα λέμε"),
                         tones=[(keys["professional"]["id"], 1, 1, None)])
    labels.set_person_label(store, k, keys["friend"]["id"], "yes")
    changes.merge_people(store, other, k)
    got = {x["key"]: x["state"] for x in labels.person_labels(store, other)}
    assert got == {"friend": "yes", "professional": "suggested"}
    assert labels.analysed(store, other) is None        # their chat is one now: to be read again
    assert labels.guess(store, other)["how"] == "models"
    labels.save_analysis(store, other, 30, ["m"])
    aid = people(store).addresses(other)[0]
    changes.split_address(store, aid)
    assert labels.analysed(store, other) is None


def test_a_name_from_an_email(store):
    k = knownByEmail(store)
    g = labels.guess(store, k)
    assert g == {"name": "Κατερίνα Οικονόμου", "how": "handle", "votes": None, "models": None,
                 "evidence": "katerina.oikonomou@example.com"}
    assert k in labels.guessed(store)
    labels.decide_guess(store, k, "handle", False)          # wrong: not suggested again
    assert labels.guess(store, k) is None
    # the models find one: it is suggested before the handle's
    labels.save_analysis(store, k, 24, ["a", "b"], name=("Κατερίνα", 2, 2, "Κατερίνα, τα λέμε αύριο"))
    assert labels.guess(store, k)["name"] == "Κατερίνα"
    assert labels.decide_guess(store, k, "models", True) == "Κατερίνα"
    assert people(store).name(k) == "Κατερίνα"
    assert labels.guess(store, k) is None                   # named now
    first = queries.unnamed_people(store, first={someone(store, unnamed=True)})
    assert first["items"][0]["id"] == someone(store, unnamed=True)


def test_handles_words():
    assert labels._words("maria.eleni") == ["maria", "eleni"]
    assert labels._words("MariaK_82") == ["maria"]
    assert labels._words("nick80") == ["nick"]
    assert labels._key("Γιώργος") == labels._key("giorgos") == labels._key("Γιώργο")


def test_what_the_analysis_reads(store):
    k = knownByEmail(store)
    todo = dict(labels.to_analyse(store))
    assert todo.get(k) == 24
    assert all(n >= 20 for n in todo.values())
    labels.save_analysis(store, k, 24, ["m"])
    assert k not in dict(labels.to_analyse(store))
    with store.write() as db:
        db.execute("UPDATE analysis SET messages = 10 WHERE person_id = ?", (k,))   # it grew
    assert k in dict(labels.to_analyse(store))
    with store.write() as db:
        db.execute("UPDATE analysis SET messages = 24, labels = 'old' WHERE person_id = ?", (k,))
    assert labels.stale(store) == 1
    assert labels.judge_again(store) == 1 and k in dict(labels.to_analyse(store))


TONES = [(1, "friendly", "friends", False), (2, "romantic", "love", True), (3, "professional", "work", False)]
RELATIONS = [(10, "friend", "a friend", False), (11, "colleague", "a colleague", False)]
TEXT = "ME: Έλα Γιώργο, τι λες; THEM: Γιώργος Νικόλας εδώ. ME: σ' αγαπώ πολύ μωρό μου"


def test_the_models_vote():
    own = {labels._key("Petros")}
    answers = [
        {"name": "Γιώργο", "evidence": "Έλα Γιώργο", "tone": ["friendly", "romantic"],
         "sensitive_evidence": "σ' αγαπώ πολύ μωρό μου", "relationship": "friend"},
        {"name": "Γιώργος Νικόλας", "evidence": "Γιώργος Νικόλας εδώ", "tone": ["friendly", "romantic"],
         "sensitive_evidence": "an invented line", "relationship": "friend"},
        {"name": "Γιώργος Νικόλας", "evidence": "Γιώργος Νικόλας εδώ", "tone": ["professional", "romantic"],
         "sensitive_evidence": "σ' αγαπώ πολύ μωρό μου", "relationship": "colleague"},
    ]
    name, tones, relation = analysis.vote(answers, TEXT, ["giorgos@x.org"], own, TONES, RELATIONS)
    assert name == ("Γιώργος Νικόλας", 3, 3, "Γιώργος Νικόλας εδώ")
    # friendly: 2 of 3; romantic: two copied the line (the invented one does not count); professional: 1
    assert sorted(i for i, *_ in tones) == [1, 2]
    assert dict((i, ev) for i, _, _, ev in tones)[2] == "σ' αγαπώ πολύ μωρό μου"
    assert relation == (10, 2, 3, None)
    # one model: a sensitive tone is never its alone
    name, tones, relation = analysis.vote(answers[:1], TEXT, [], own, TONES, RELATIONS)
    assert name[0] == "Γιώργο" and [i for i, *_ in tones] == [1] and relation[0] == 10
    # not a name: an email, a handle, a number, the owner's, one not in the texts
    for bad in ("giorgos@x.org", "Petros", "Μαρία", "user_123", None, "null"):
        assert analysis.vote([{"name": bad}], TEXT + " Petros", ["giorgos@x.org"], own, [], [])[0] is None


def test_only_a_local_address():
    assert analysis.local_address("http://localhost:11434")
    assert analysis.local_address("http://127.0.0.1:11434")
    assert analysis.local_address("http://192.168.0.10:11434")
    assert not analysis.local_address("https://8.8.8.8/")
    assert not analysis.local_address("")


def test_the_plugin_reads_and_suggests(store, monkeypatch):
    from everysaid import plugins
    from everysaid.plugins.base import Context
    iid = plugins.create(store, "ollama", "Local", {"models": "a, b"})

    class Host:
        def __init__(self):
            self.store = store

        def emit(self, e):
            pass

    ctx = Context(Host(), plugins.instance(store, iid))
    p = plugins.get("ollama")
    asked = []

    def ask(ctx, model, prompt, schema):
        asked.append((model, prompt, schema))
        return {"name": "Κατερίνα", "evidence": "Κατερίνα, τα λέμε αύριο", "tone": ["professional"],
                "sensitive_evidence": None, "relationship": "colleague"}

    monkeypatch.setattr(p, "ask", ask)
    assert p.check(ctx)[0]
    k = knownByEmail(store)
    assert p.todo(ctx)[0][0] == k
    assert p.step(ctx)
    model, prompt, schema = asked[0]
    assert {m for m, _, _ in asked} == {"a", "b"}
    assert "romantic" in schema["properties"]["tone"]["items"]["enum"]
    assert "katerina.oikonomou@example.com" in prompt and "Κατερίνα, τα λέμε αύριο" in prompt
    assert labels.guess(store, k)["name"] == "Κατερίνα" and labels.guess(store, k)["votes"] == 2
    assert {x["key"] for x in labels.person_labels(store, k)} == {"professional", "colleague"}
    assert k not in dict(p.todo(ctx))
    assert dict(p.info(ctx))["Read"] == "1"
    p.action(ctx, "forget")
    assert labels.person_labels(store, k) == [] and labels.guess(store, k)["how"] == "handle"
    # the live loop: one step at a time, in a thread
    calls = []
    monkeypatch.setattr(p, "step", lambda ctx: calls.append(1) or len(calls) < 3)

    async def run():
        try:
            await asyncio.wait_for(p.live(ctx), 0.5)
        except asyncio.TimeoutError:
            pass
    asyncio.run(run())
    assert len(calls) == 3
