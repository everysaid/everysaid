import shutil

import pytest
from fastapi.testclient import TestClient

from tests.conftest import PRISTINE

BASE = "http://localhost:8520"
H = {"X-Everysaid": "1"}


@pytest.fixture
def app(tmp_path):
    from everysaid.server.app import create_app
    db = tmp_path / "archive.db"
    shutil.copy(PRISTINE, db)
    app = create_app(str(db), str(tmp_path / "server.db"))
    with TestClient(app, base_url=BASE) as c:
        yield app, c


def login(app, c):
    auth = app.state.auth
    uid = auth.create_user("Test", app.state.store.path)
    c.cookies.set("everysaid_session", auth.new_session(uid, "pytest", "127.0.0.1"))
    return uid


def test_guards(app):
    app, c = app
    assert c.get("/api/chats").status_code == 401
    assert c.get("/api/health").json() == {"ok": True}
    r = c.get("/api/health")
    assert "frame-ancestors 'none'" in r.headers["content-security-policy"]
    assert r.headers["x-frame-options"] == "DENY"
    assert TestClient(app, base_url="http://evil.example").get("/api/health").status_code == 421
    login(app, c)
    assert c.get("/api/chats").status_code == 200
    first = c.get("/api/chats").json()["items"][0]["id"]
    assert c.post(f"/api/chats/{first}/read").status_code == 403                 # no X-Everysaid
    assert c.post(f"/api/chats/{first}/read", headers={**H, "Origin": "https://evil.example"}).status_code == 403
    assert c.post(f"/api/chats/{first}/read", headers=H).status_code == 200


def test_status_and_setup(app):
    app, c = app
    s = c.get("/api/auth/status").json()
    assert s["needs_setup"] and not s["logged_in"]
    r = c.post("/api/auth/register/options", json={"token": "wrong"}, headers=H)
    assert r.status_code == 403
    token = app.state.auth.setup_link()
    r = c.post("/api/auth/register/options", json={"token": token, "name": "Me"}, headers=H)
    assert r.status_code == 200 and r.json()["options"]["authenticatorSelection"]["residentKey"] == "required"
    assert c.post("/api/auth/login/options", headers=H).json()["options"]["rpId"] == "localhost"


def test_recovery(app):
    app, c = app
    uid = app.state.auth.create_user("Me", app.state.store.path)
    codes = app.state.auth.new_recovery_codes(uid)
    assert c.post("/api/auth/recover", json={"code": "nope"}, headers=H).status_code == 403
    r = c.post("/api/auth/recover", json={"code": codes[0]}, headers=H)
    assert r.status_code == 200 and r.json()["left"] == 9
    assert c.get("/api/auth/account").json()["user"]["name"] == "Me"
    c.cookies.clear()
    assert c.post("/api/auth/recover", json={"code": codes[0]}, headers=H).status_code == 403


def test_api_flow(app):
    app, c = app
    login(app, c)
    chats = c.get("/api/chats").json()["items"]
    person = next(x for x in chats if x["type"] == "person")
    page = c.get(f"/api/chats/{person['id']}/stream?limit=20").json()
    assert len(page["items"]) <= 20
    assert c.get(f"/api/chats/{person['id']}").json()["person"]["name"] == person["title"]
    assert c.get("/api/search", params={"q": "καλημερα"}).json()["total"] > 0
    pid = person["person_id"]
    r = c.patch(f"/api/people/{pid}", json={"name": "Νέο Όνομα"}, headers=H)
    assert r.json()["name"] == "Νέο Όνομα"
    m = c.get("/api/media?kind=image").json()["items"][0]
    r = c.get(f"/api/media/{m['sha256']}/thumb")
    assert r.status_code == 200 and r.headers["content-type"] == "image/webp"
    assert c.get(f"/api/media/{m['sha256']}/original").status_code == 200
    assert c.get("/api/stats").json()["messages"] > 1000
    assert len(c.get("/api/plugins/catalog").json()["items"]) >= 10
    assert c.get("/api/plugins").json()["items"]
    assert c.get("/api/avatar/999999").status_code == 404


def test_library_folder(app, tmp_path):
    app, c = app
    login(app, c)
    lib = tmp_path / "photos"
    lib.mkdir()
    r = c.post("/api/plugins", json={"plugin": "folder", "label": "Test photos", "settings": {"path": str(lib)}}, headers=H)
    iid = r.json()["id"]
    m = c.get("/api/media?kind=image").json()["items"][0]
    r = c.post(f"/api/media/{m['sha256']}/library", json={"instance_id": iid}, headers=H)
    assert r.status_code == 200 and not r.json()["already"], r.text
    assert list(lib.rglob("*.jpg"))
    r = c.post(f"/api/media/{m['sha256']}/library", json={"instance_id": iid}, headers=H)
    assert r.json()["already"]


def test_websocket(app):
    app, c = app
    with pytest.raises(Exception):
        with c.websocket_connect("/api/events") as ws:
            ws.receive_json()
    login(app, c)
    with c.websocket_connect("/api/events") as ws:
        assert ws.receive_json()["type"] == "hello"


def test_password_with_code(app):
    from everysaid.server.auth import totp_at
    import time
    app, c = app
    token = app.state.auth.setup_link()
    o = c.post("/api/auth/password/options", json={"token": token, "name": "Me"}, headers=H).json()
    assert o["uri"].startswith("otpauth://totp/")
    assert c.post("/api/auth/password/set", json={"nonce": "nope", "password": "a long enough password", "code": "000000"}, headers=H).status_code == 410
    assert c.post("/api/auth/password/set", json={"nonce": o["nonce"], "password": "short", "code": "000000"}, headers=H).status_code == 400
    assert c.post("/api/auth/password/set", json={"nonce": o["nonce"], "password": "a long enough password", "code": "000000"}, headers=H).status_code == 400
    code = totp_at(o["secret"], int(time.time()) // 30)
    r = c.post("/api/auth/password/set", json={"nonce": o["nonce"], "password": "a long enough password", "code": code}, headers=H)
    assert r.status_code == 200 and len(r.json()["recovery_codes"]) == 10      # mistakes did not use the setup up
    assert c.post("/api/auth/password/set", json={"nonce": o["nonce"], "password": "a long enough password", "code": code}, headers=H).status_code == 410
    assert c.get("/api/auth/account").json()["has_password"]
    c.cookies.clear()
    bad = c.post("/api/auth/password/login", json={"name": "Me", "password": "wrong password!", "code": code}, headers=H)
    assert bad.status_code == 403
    reused = c.post("/api/auth/password/login", json={"name": "me", "password": "a long enough password", "code": code}, headers=H)
    assert reused.status_code == 403            # a code is accepted once
    nxt = totp_at(o["secret"], int(time.time()) // 30 + 1)
    ok = c.post("/api/auth/password/login", json={"name": "me", "password": "a long enough password", "code": nxt}, headers=H)
    assert ok.status_code == 200
    assert c.get("/api/chats").status_code == 200



def test_password_lockout(app):
    from everysaid.server.auth import totp_at, totp_secret
    import time
    app, c = app
    auth = app.state.auth
    uid = auth.create_user("Lock", app.state.store.path)
    secret = totp_secret()
    auth.set_password(uid, "a long enough password", secret)
    for _ in range(5):
        assert auth.check_password("Lock", "wrong password!!", "000000") is None
    code = totp_at(secret, int(time.time()) // 30)
    assert auth.check_password("Lock", "a long enough password", code) is None      # locked now
    assert auth.locked(uid)


def test_plugins_declare_names_services_and_sending(app):
    app, c = app
    login(app, c)
    looks = c.get("/api/services?lang=el").json()
    assert looks and all({"name", "color", "short", "messages"} <= set(v) for v in looks.values())
    names = c.get("/api/names").json()
    ids = [x["id"] for x in names["order"]]
    weights = [x["weight"] for x in names["order"]]
    assert not names["custom"] and weights == sorted(weights, reverse=True)     # the plugins' order
    mine = ids[::-1]
    c.put("/api/settings", json={"name_order": mine}, headers=H)
    assert [x["id"] for x in c.get("/api/names").json()["order"]] == mine
    assert c.put("/api/settings", json={"name_order": ["no-such-source"]}, headers=H).status_code == 200
    assert [x["id"] for x in c.get("/api/names").json()["order"]] == mine        # refused, kept
    c.put("/api/settings", json={"name_order": None}, headers=H)
    back = c.get("/api/names").json()
    assert not back["custom"] and [x["id"] for x in back["order"]] == ids
    chat = next(x for x in c.get("/api/chats").json()["items"] if x["type"] == "person")
    detail = c.get(f"/api/chats/{chat['id']}").json()
    assert set(detail["sendable"]) <= set(detail["services"])
    for p in c.get("/api/plugins").json()["items"]:
        if p["kind"] == "source":
            c.patch(f"/api/plugins/{p['id']}", json={"enabled": False}, headers=H)
    assert c.get(f"/api/chats/{chat['id']}").json()["sendable"] == []           # no source can send now


def test_changing_ways_in_needs_a_recent_sign_in(app):
    app, c = app
    login(app, c)
    assert c.post("/api/auth/password/options", json={}, headers=H).status_code == 200
    app.state.auth.x("UPDATE session SET created_at = created_at - 3600")        # signed in an hour ago
    for method, path in (("post", "/api/auth/password/options"), ("post", "/api/auth/recovery-codes"),
                         ("post", "/api/auth/register/options"), ("delete", "/api/auth/password")):
        r = getattr(c, method)(path, **({"json": {}} if method == "post" else {}), headers=H)
        assert r.status_code == 403, path
    assert c.get("/api/chats").status_code == 200                                 # everything else still works


def test_a_setting_that_is_not_what_it_must_be_is_refused(app):
    app, c = app
    login(app, c)
    made = c.post("/api/plugins", json={"plugin": "iphone-backup", "label": "Test iPhone"}, headers=H).json()
    bad = c.patch(f"/api/plugins/{made['id']}", json={"settings": {"udid": "Somebody"}}, headers={**H, "X-Lang": "el"})
    assert bad.status_code == 400 and bad.json()["detail"]["code"] == "settings.invalid"     # a name a browser filled in
    assert bad.json()["detail"]["params"]["value"] == "Somebody"
    good = c.patch(f"/api/plugins/{made['id']}", json={"settings": {"udid": "00008110-001A2B3C4D5E6F70"}}, headers=H)
    assert good.status_code == 200 and good.json()["settings"]["udid"] == "00008110-001A2B3C4D5E6F70"
    assert c.patch(f"/api/plugins/{made['id']}", json={"settings": {"udid": ""}}, headers=H).status_code == 200   # found by itself


def test_mentions_files_receipts_and_read_receipts(app, monkeypatch):
    """A group's members to name with @, a file sent with its caption, who got and read the user's
    messages, and the services told the chat was read: through a plugin that records what it is asked."""
    import time
    from everysaid import plugins
    from everysaid.plugins.base import Plugin

    asked = []

    class Recorder(Plugin):
        id, kind, services = "demo-sender", "source", ("whatsapp", "telegram", "viber", "sms")
        can_send = can_reply = can_mention = can_mark_read = can_send_files = True

        def check(self, ctx):
            return True, "ready"

        async def send(self, ctx, conversation, text, reply_to=None, mentions=None, file=None):
            asked.append(("send", conversation["service"], text, mentions, file))
            return {"id": 1}

        async def mark_read(self, ctx, conversation, until):
            asked.append(("read", conversation["service"], until))
            return 1

    monkeypatch.setitem(plugins.REGISTRY, "demo-sender", Recorder())
    app, c = app
    login(app, c)
    group = next(x for x in c.get("/api/chats?kind=group").json()["items"]
                 if "whatsapp" in c.get(f"/api/chats/{x['id']}").json()["services"])
    detail = c.get(f"/api/chats/{group['id']}").json()
    assert "whatsapp" in detail["mentionable"] and "whatsapp" in detail["fileable"]
    member = detail["members"][0]
    assert member["address_id"] and "whatsapp" in member["services"]

    # the stream: mentions with their tokens, and ticks of the user's messages
    items = c.get(f"/api/chats/{group['id']}/stream?limit=200").json()["items"]
    page, before = items, items[0]["cursor"]
    while not any(i.get("mentions") for i in page) and before:
        older = c.get(f"/api/chats/{group['id']}/stream?limit=200&before={before}").json()
        page, before = older["items"], older["items"][0]["cursor"] if older["has_older"] else None
        items += page
    named = next(i for i in items if i.get("mentions"))
    assert named["text"].startswith(named["mentions"][0]["token"]) and named["mentions"][0]["name"]
    mine = next(i for i in items if i["type"] == "message" and i["outgoing"] and i["receipts"])
    assert mine["receipts"]["to"] >= mine["receipts"]["delivered"] >= mine["receipts"]["read"]
    who = c.get(f"/api/messages/{mine['id']}/receipts").json()["items"]
    assert len(who) == mine["receipts"]["to"] and all(r["delivered_at"] for r in who)

    # @ and a file
    name = f"@{member['name']}"
    text = f"🙂 {name} look"
    r = c.post(f"/api/chats/{group['id']}/send", headers=H, json={
        "text": text, "service": "whatsapp", "mentions": [{"start": 2, "length": len(name), "address_id": member["address_id"]}]})
    assert r.status_code == 200, r.text
    assert asked[-1][3] == [{"start": 2, "length": len(name), "address_id": member["address_id"]}]
    outside = {a for (a,) in app.state.store.read().execute("SELECT id FROM address")} - {m["address_id"] for m in detail["members"]}
    r = c.post(f"/api/chats/{group['id']}/send", headers=H, json={
        "text": "@x hi", "service": "whatsapp", "mentions": [{"start": 0, "length": 2, "address_id": min(outside)}]})
    assert r.status_code == 400 and r.json()["detail"]["code"] == "chat.not_a_member"
    aid = member["address_id"]
    for bad in ([{"start": 3, "length": 2, "address_id": aid}],                  # not an "@"
                [{"start": -5, "length": 2, "address_id": aid}],                 # outside the text
                [{"start": 0, "length": 9, "address_id": aid}],
                [{"start": 0, "length": 2, "address_id": aid}, {"start": 1, "length": 2, "address_id": aid}],
                [{"start": 0}], "nonsense"):
        r = c.post(f"/api/chats/{group['id']}/send", headers=H, json={"text": "@x hi", "service": "whatsapp", "mentions": bad})
        assert r.status_code == 400, bad
    assert c.post(f"/api/chats/{group['id']}/send", headers=H, json=["not", "an", "object"]).status_code == 400
    r = c.post(f"/api/chats/{group['id']}/send", headers=H, data={"text": "a\r\n@x b", "service": "whatsapp",
               "mentions": f'[{{"start": 2, "length": 2, "address_id": {aid}}}]'}, files={"file": ("a.txt", b"t", "text/plain")})
    assert r.status_code == 200 and asked[-1][2] == "a\n@x b"         # the form's CRLF as one line break
    r = c.post(f"/api/chats/{group['id']}/send", headers=H, data={"text": "", "service": "whatsapp"},
               files={"file": ("photo.jpg", b"\xff\xd8 picture", "image/jpeg")})
    assert r.status_code == 200, r.text
    assert asked[-1][2] == "" and asked[-1][4] == {"data": b"\xff\xd8 picture", "filename": "photo.jpg", "mime_type": "image/jpeg"}
    assert c.post(f"/api/chats/{group['id']}/send", headers=H, json={"text": " "}).json()["detail"]["code"] == "empty_message"

    # read here: the services told, where something of the others is newer than what they said was read
    asked.clear()
    assert c.post(f"/api/chats/{group['id']}/read", headers=H).status_code == 200
    for _ in range(50):
        if asked:
            break
        time.sleep(0.05)
    assert asked and asked[0][0] == "read" and asked[0][1] == "whatsapp"


def test_people_without_a_name_only_when_asked(app):
    app, c = app
    login(app, c)
    titles = lambda: {x["title"] for x in c.get("/api/chats").json()["items"]}       # noqa: E731
    assert "+1 555-010-0000" not in titles()
    assert c.put("/api/settings", json={"show_unnamed": True}, headers=H).status_code == 200
    assert "+1 555-010-0000" in titles()


def test_labels_and_names_found(app):
    app, c = app
    login(app, c)
    from everysaid.core import labels
    store = app.state.store
    items = c.get("/api/labels").json()["items"]
    keys = {x["key"]: x for x in items if x["key"]}
    assert "romantic" in keys and "sexual" not in keys
    r = c.post("/api/labels", json={"kind": "tone", "name": "Acme", "meaning": "work at Acme"}, headers=H)
    acme = r.json()["id"]
    assert c.post("/api/labels", json={"kind": "tone", "name": "acme"}, headers=H).json()["detail"]["code"] == "labels.exists"
    c.patch(f"/api/labels/{acme}", json={"name": "Acme SA"}, headers=H)
    assert any(x["name"] == "Acme SA" for x in c.get("/api/labels").json()["items"])
    # the person with the email that says who they are
    un = c.get("/api/people/unnamed", params={"guessed": True}).json()["items"]
    k = un[0]
    assert k["guess"]["name"] == "Κατερίνα Οικονόμου" and k["guess"]["how"] == "handle"
    pid = k["id"]
    labels.save_analysis(store, pid, 24, ["m"], tones=[(keys["professional"]["id"], 1, 1, None)])
    c.put(f"/api/people/{pid}/labels/{acme}", json={"state": "yes"}, headers=H)
    # the models' labels only when the user shows them; their own always
    assert [x["name"] for x in c.get(f"/api/people/{pid}").json()["labels"]] == ["Acme SA"]
    c.put("/api/settings", json={"show_tone": True}, headers=H)
    got = c.get(f"/api/people/{pid}").json()
    assert {x["key"] or x["name"] for x in got["labels"]} == {"professional", "Acme SA"}
    assert got["analysed"]["messages"] == 24
    assert c.post(f"/api/labels/{acme}/merge", json={"into": keys["professional"]["id"]}, headers=H).status_code == 200
    got = c.get(f"/api/people/{pid}").json()["labels"]
    assert [(x["key"], x["state"]) for x in got] == [("professional", "yes")]
    assert c.post(f"/api/people/{pid}/analyse", headers=H).json()["analysed"] is None
    p = c.post(f"/api/people/{pid}/guess", json={"how": "handle", "accept": True}, headers=H).json()
    assert p["name"] == "Κατερίνα Οικονόμου" and p["guess"] is None
    assert c.post(f"/api/people/{pid}/guess", json={"how": "handle", "accept": True}, headers=H).json()["detail"]["code"] == "people.no_guess"
    assert c.delete(f"/api/labels/{keys['formal']['id']}", headers=H).status_code == 200
    assert "formal" not in {x["key"] for x in c.get("/api/labels").json()["items"]}


def test_the_assistant_sees_labels_only_when_allowed(app):
    app, c = app
    from everysaid.core import labels
    from everysaid.mcp_server import build
    from tests.test_mcp import call
    from everysaid.core import queries
    store = app.state.store
    friend = next(x["id"] for x in labels.labels(store) if x["key"] == "friend")
    person = queries.people_list(store)["items"][0]
    labels.set_person_label(store, person["id"], friend, "yes")
    mcp = build(store.path)
    assert "labels" not in call(mcp, "get_person", person_id=person["id"])
    from everysaid.core import changes
    changes.set_setting(store, "mcp_labels", True)
    assert call(mcp, "get_person", person_id=person["id"])["labels"] == [{"kind": "relation", "label": "friend", "by": "user"}]


def test_people_by_label(app):
    app, c = app
    login(app, c)
    from everysaid.core import labels
    store = app.state.store
    friend = next(x["id"] for x in labels.labels(store) if x["key"] == "friend")
    first, second = c.get("/api/people").json()["items"][:2]
    labels.set_person_label(store, first["id"], friend, "yes")
    labels.save_analysis(store, second["id"], 30, ["m"], relation=(friend, 1, 1, None))
    people = {p["id"]: p for p in c.get("/api/people").json()["items"]}
    assert [x["key"] for x in people[first["id"]]["labels"]] == ["friend"]
    assert people[second["id"]]["labels"] == []                 # the models' only when shown
    assert [p["id"] for p in c.get("/api/people", params={"label": friend}).json()["items"]] == [first["id"]]
    c.put("/api/settings", json={"show_tone": True}, headers=H)
    assert {p["id"] for p in c.get("/api/people", params={"label": friend}).json()["items"]} == {first["id"], second["id"]}
