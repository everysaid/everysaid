"""The WhatsApp bridge's databases as the importers and the plugin read them: a bridge of this
version (kinds, replies, places, reactions, edits, deletions, calls, its state), and one from before."""
import json
import sqlite3

import pytest

from chronika import voip, whatsapp
from chronika.archive import Archive

PEER = "15551234567@s.whatsapp.net"
GROUP = "120363000000000001@g.us"
OLD_SCHEMA = """
CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP);
CREATE TABLE messages (id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP, is_from_me BOOLEAN,
    media_type TEXT, filename TEXT, url TEXT, media_key BLOB, file_sha256 BLOB, file_enc_sha256 BLOB,
    file_length INTEGER, PRIMARY KEY (id, chat_jid));
"""
NEW_SCHEMA = OLD_SCHEMA + """
ALTER TABLE messages ADD COLUMN kind TEXT; ALTER TABLE messages ADD COLUMN subtype TEXT;
ALTER TABLE messages ADD COLUMN reply_to TEXT; ALTER TABLE messages ADD COLUMN reply_text TEXT;
ALTER TABLE messages ADD COLUMN forwarded BOOLEAN; ALTER TABLE messages ADD COLUMN edited BOOLEAN;
ALTER TABLE messages ADD COLUMN deleted BOOLEAN; ALTER TABLE messages ADD COLUMN lat REAL;
ALTER TABLE messages ADD COLUMN lon REAL; ALTER TABLE messages ADD COLUMN place TEXT;
CREATE TABLE reactions (chat_jid TEXT, message_id TEXT, sender TEXT, is_from_me BOOLEAN, emoji TEXT, timestamp TIMESTAMP,
    PRIMARY KEY (chat_jid, message_id, sender));
CREATE TABLE calls (id TEXT PRIMARY KEY, source TEXT, chat_jid TEXT, creator TEXT, is_from_me BOOLEAN, is_group BOOLEAN,
    video BOOLEAN, timestamp TIMESTAMP, outcome TEXT, duration INTEGER, accepted_at TIMESTAMP, ended_at TIMESTAMP,
    end_reason TEXT);
CREATE TABLE call_participants (call_id TEXT, jid TEXT, outcome TEXT, PRIMARY KEY (call_id, jid));
CREATE TABLE bridge_state (key TEXT PRIMARY KEY, value TEXT, at TIMESTAMP);
"""


def ts(minute):
    return f"2026-10-06 10:{minute:02}:00+03:00"


def bridge(tmp_path, schema=NEW_SCHEMA):
    path = tmp_path / "messages.db"
    db = sqlite3.connect(path)
    db.executescript(schema)
    db.execute("INSERT INTO chats VALUES (?, 'Peer', ?)", (PEER, ts(30)))
    db.commit()
    return path, db


def message(db, id, minute, content="", from_me=0, **cols):
    row = {"id": id, "chat_jid": PEER, "sender": "" if from_me else PEER, "content": content, "timestamp": ts(minute),
           "is_from_me": from_me, "media_type": "", **cols}
    db.execute(f"INSERT INTO messages ({', '.join(row)}) VALUES ({', '.join('?' * len(row))})", list(row.values()))
    db.commit()


def run(store, path):
    a = Archive(store.path)
    try:
        return whatsapp.run(a, iphone_db=None, contacts_db=None, bridge_db=str(path), store_db=None)
    finally:
        a.db.close()


def row(store, key, cols):
    return store.read().execute(f"SELECT {cols} FROM message WHERE key = ?", (key,)).fetchone()


def reactions(store, key):
    return sorted(store.read().execute(
        "SELECT r.emoji, r.outgoing FROM reaction r JOIN message m ON m.id = r.message_id WHERE m.key = ?", (key,)).fetchall(),
        key=lambda r: (r[1] or 0, r[0]))


def test_bridge_kinds_replies_places_and_reactions(store, tmp_path):
    path, db = bridge(tmp_path)
    message(db, "M1", 1, "coming tonight?", kind="text")
    message(db, "M2", 2, "", kind="location", lat=37.97, lon=23.73, place="Syntagma")
    message(db, "M3", 3, "Nikos, +30 690 000 0000", kind="contact")
    message(db, "M4", 4, "When?\n• Mon\n• Tue", kind="poll")
    message(db, "M5", 5, "yes", from_me=1, kind="text", reply_to="M1", reply_text="coming tonight?", forwarded=1)
    message(db, "M6", 6, "", kind="video", subtype="gif", media_type="video")
    db.execute("INSERT INTO reactions VALUES (?, 'M1', ?, 0, '👍', ?)", (PEER, PEER, ts(7)))
    db.execute("INSERT INTO reactions VALUES (?, 'M1', 'me@s.whatsapp.net', 1, '❤️', ?)", (PEER, ts(8)))
    db.commit()
    run(store, path)

    kind = lambda key: store.read().execute("SELECT k.name FROM message m JOIN message_kind k ON k.id = m.kind_id "
                                            "WHERE m.key = ?", (key,)).fetchone()[0]
    assert [kind(k) for k in ("M1", "M2", "M3", "M4", "M5", "M6")] == ["text", "location", "contact", "text", "text", "video"]
    assert tuple(row(store, "M2", "lat, lon, place")) == (37.97, 23.73, "Syntagma")
    assert row(store, "M3", "text")[0] == "📇 Nikos, +30 690 000 0000"
    assert tuple(row(store, "M4", "subtype, subtype_code")) == ("poll", "whatsmeow:poll")
    m1 = row(store, "M1", "id")[0]
    assert tuple(row(store, "M5", "reply_to, reply_text, forwarded")) == (m1, None, 1)    # quoted text dropped once linked
    assert tuple(row(store, "M6", "subtype, subtype_code")) == ("gif", "whatsmeow:gif")
    assert reactions(store, "M1") == [("👍", None), ("❤️", 1)]


def test_bridge_changes_to_messages_already_there(store, tmp_path):
    path, db = bridge(tmp_path)
    message(db, "M1", 1, "see you at 8", kind="text")
    message(db, "M2", 2, "oops", kind="text")
    db.execute("INSERT INTO reactions VALUES (?, 'M1', ?, 0, '👍', ?)", (PEER, PEER, ts(3)))
    db.execute("INSERT INTO reactions VALUES (?, 'M1', 'me@s.whatsapp.net', 1, '❤️', ?)", (PEER, ts(3)))
    db.commit()
    run(store, path)
    assert run(store, path) == {}                                    # nothing new: nothing changed

    db.execute("UPDATE messages SET content = 'see you at 9', edited = 1 WHERE id = 'M1'")
    db.execute("UPDATE messages SET deleted = 1 WHERE id = 'M2'")
    db.execute("UPDATE reactions SET emoji = '😂' WHERE NOT is_from_me")          # changed
    db.execute("UPDATE reactions SET emoji = '' WHERE is_from_me")                 # taken back
    db.commit()
    assert run(store, path) == {"edited": 1, "deleted": 1, "reactions": 2}
    assert tuple(row(store, "M1", "text, edited")) == ("see you at 8", 1)           # the text the archive first had
    assert tuple(row(store, "M2", "text, deleted")) == ("oops", 1)                  # kept, marked
    assert reactions(store, "M1") == [("😂", None)]
    assert run(store, path) == {}


def test_a_bridge_from_before_imports_as_it_did(store, tmp_path):
    path, db = bridge(tmp_path, OLD_SCHEMA)
    message(db, "O1", 1, "hello")
    message(db, "O2", 2, "look", media_type="image")
    assert run(store, path) == {}
    assert row(store, "O1", "text")[0] == "hello"
    kind = store.read().execute("SELECT k.name FROM message m JOIN message_kind k ON k.id = m.kind_id WHERE m.key = 'O2'").fetchone()
    assert kind[0] == "image"
    a = Archive(store.path)
    try:
        voip.bridge_calls(a, voip.Calls(a), str(path), None)        # no calls table: nothing, no error
    finally:
        a.db.close()


def test_bridge_calls(store, tmp_path):
    path, db = bridge(tmp_path)
    calls = [  # id, source, chat, creator, from me, group, video, ts, outcome, duration, accepted, ended, reason
        ("L1", "log", PEER, None, 1, 0, 1, ts(1), "CONNECTED", 125, None, None, None),
        ("L2", "log", PEER, None, 0, 0, 0, ts(10), "MISSED", 0, None, None, None),
        ("E1", "event", PEER, PEER, 0, 0, 0, ts(20), None, None, ts(20), "2026-10-06 10:21:30+03:00", "terminate"),
        ("E2", "event", PEER, PEER, 0, 0, 0, ts(40), None, None, None, ts(40), "reject"),
        ("E3", "event", PEER, PEER, 0, 0, 0, ts(50), None, None, None, None, None),       # still ringing
        ("E4", "event", PEER, PEER, 0, 0, 0, ts(10), None, None, None, ts(10), "timeout"),   # the same call as L2
    ]
    db.executemany("INSERT INTO calls VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", calls)
    db.commit()
    a = Archive(store.path)
    try:
        before = a.db.execute("SELECT count(*) FROM call").fetchone()[0]
        voip.bridge_calls(a, voip.Calls(a), str(path), None)
        a.db.commit()
        got = a.db.execute("SELECT outgoing, answered, duration, detail, video FROM call WHERE id > (SELECT ifnull(max(id), 0) "
                           "FROM call) - 4 ORDER BY ts").fetchall()
        assert a.db.execute("SELECT count(*) FROM call").fetchone()[0] == before + 4
        assert [tuple(r) for r in got] == [(1, 1, 125, None, 1), (0, 0, 0, "missed", 0), (0, 1, 90, None, 0),
                                           (0, 0, 0, "rejected", 0)]
        voip.bridge_calls(a, voip.Calls(a), str(path), None)    # again: nothing new
        assert a.db.execute("SELECT count(*) FROM call").fetchone()[0] == before + 4
    finally:
        a.db.close()


@pytest.fixture
def bridge_instance(store, tmp_path):
    from chronika.server.host import Host
    path, db = bridge(tmp_path)
    db.execute("INSERT INTO bridge_state VALUES ('send_enabled', '1', ?)", (ts(0),))
    db.commit()
    with store.write() as w:
        iid = w.execute("SELECT id FROM plugin_instance WHERE plugin = 'whatsapp-bridge'").fetchone()[0]
        w.execute("UPDATE plugin_instance SET settings = ? WHERE id = ?",
                  (json.dumps({"store": str(tmp_path), "send": True}), iid))
    host = Host(store)
    alerts = []
    host.alert = lambda title, body: alerts.append((title, body))
    return host, iid, db, alerts


def test_the_plugin_turns_sending_off_when_the_bridge_blocks_it(bridge_instance):
    from chronika import plugins
    host, iid, db, alerts = bridge_instance
    p = plugins.get("whatsapp-bridge")
    assert p.sending(host.ctx(iid))
    db.execute("INSERT INTO bridge_state VALUES ('send_blocked', 'temporary ban: sent to too many people', ?)", (ts(1),))
    db.commit()
    ctx = host.ctx(iid)
    assert not p.sending(ctx)
    assert p.check(ctx) == (True, "sending blocked by the bridge: temporary ban: sent to too many people")
    p.watch_state(ctx)
    assert alerts == [("WhatsApp warned the account", "temporary ban: sent to too many people")]
    ctx = host.ctx(iid)
    assert ctx.settings["send"] is False                            # off in the app too, until the user says
    p.watch_state(ctx)
    assert len(alerts) == 1                                         # told once
    db.execute("UPDATE bridge_state SET value = '' WHERE key = 'send_blocked'")     # cleared at the bridge
    db.commit()
    p.watch_state(host.ctx(iid))
    assert not p.sending(host.ctx(iid))                             # still off: turning it on is the user's


def test_sending_needs_the_bridge_started_with_send(bridge_instance):
    from chronika import plugins
    host, iid, db, _ = bridge_instance
    db.execute("UPDATE bridge_state SET value = '' WHERE key = 'send_enabled'")
    db.commit()
    assert not plugins.get("whatsapp-bridge").sending(host.ctx(iid))
    assert host.unsendable()["whatsapp"] == "off at the bridge (started without -send)"   # what the lock says


def test_its_card_says_what_the_bridge_says_now(bridge_instance, monkeypatch):
    from chronika import plugins
    host, iid, _, _ = bridge_instance
    p = plugins.get("whatsapp-bridge")
    live = {"connected": True, "connection": "connected", "send_enabled": True, "send_blocked": "",
            "sent": {"day": 4}, "limits": {"per_day": 300}}
    monkeypatch.setattr(p, "bridge_status", lambda ctx: live)
    assert p.info(host.ctx(iid)) == [("Connection", "connected to WhatsApp"), ("Sending", "on, 4 of 300 today")]
    live.update(connected=False, send_enabled=False)            # its last record says connected; not so now
    assert p.info(host.ctx(iid)) == [("Connection", "not connected to WhatsApp"),
                                     ("Sending", "off at the bridge (started without -send)")]
    live.update(connection="logged_out", send_blocked="logged out: 401")
    info = host.status(iid, "el")["info"]
    assert {"label": "Σύνδεση", "value": "αποσυνδέθηκε από το WhatsApp"} in info
    assert {"label": "Αποστολή", "value": "μπλοκαρισμένη από τη γέφυρα: logged out: 401"} in info
    monkeypatch.setattr(p, "bridge_status", lambda ctx: None)
    assert host.status(iid, "el")["info"] == [{"label": "Σύνδεση", "value": "η γέφυρα δεν απαντά"}]
