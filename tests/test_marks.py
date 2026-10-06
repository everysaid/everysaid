"""Mentions, receipts and how far chats were read, as the Telegram and Viber importers bring them,
on small databases made here (shaped as telegram.db and the iPhone's viber.sqlite)."""
import json
import sqlite3

from chronika import telegram, telegram_store, viber
from chronika.archive import APPLE_EPOCH, IPHONE, Archive

ME, MARIA, BOB, GROUP = 999, 111, 222, -5


def telegram_db(path):
    db = telegram_store.open_store(str(path))
    for pid, user in ((ME, {"_": "User", "id": ME, "is_self": True, "phone": "15550000000"}),
                      (MARIA, {"_": "User", "id": MARIA, "first_name": "Maria", "username": "Maria_K", "phone": "15557770001"}),
                      (BOB, {"_": "User", "id": BOB, "first_name": "Bob"})):
        db.execute("INSERT INTO entity VALUES (?, ?)", (pid, json.dumps(user)))
    db.execute("INSERT INTO chat VALUES (?, 'group', 'Friends', 0, '{}', 1)", (GROUP,))
    db.execute("INSERT INTO chat VALUES (?, 'user', 'Maria', 0, ?, 1)",
               (MARIA, json.dumps({"_": "User", "id": MARIA, "username": "Maria_K", "phone": "15557770001"})))
    text = "hi 😀 @maria_k and Bob"          # the emoji is two UTF-16 units: the offsets count them so
    msgs = [
        (GROUP, 1, {"_": "Message", "id": 1, "message": text, "from_id": {"user_id": BOB},
                    "entities": [{"_": "MessageEntityMention", "offset": 6, "length": 8},
                                 {"_": "MessageEntityMentionName", "offset": 19, "length": 3, "user_id": BOB},
                                 {"_": "MessageEntityBold", "offset": 0, "length": 2}]}),
        (GROUP, 2, {"_": "Message", "id": 2, "message": "from Maria", "from_id": {"user_id": MARIA}}),
        (GROUP, 3, {"_": "Message", "id": 3, "message": "anonymous admin", "from_id": {"channel_id": 77}}),
        (MARIA, 10, {"_": "Message", "id": 10, "message": "mine 1", "out": True}),
        (MARIA, 11, {"_": "Message", "id": 11, "message": "theirs", "from_id": {"user_id": MARIA}}),
        (MARIA, 12, {"_": "Message", "id": 12, "message": "mine 2", "out": True}),
        (MARIA, 13, {"_": "Message", "id": 13, "message": "mine 3", "out": True}),
    ]
    for chat, mid, m in msgs:
        db.execute("INSERT INTO message (chat_id, id, date, json) VALUES (?, ?, ?, ?)",
                   (chat, mid, 1_790_000_000 + mid, json.dumps(m)))
    return db


def test_telegram_mentions_members_and_reads(store, tmp_path):
    path = tmp_path / "telegram.db"
    db = telegram_db(path)
    telegram_store.note_read(db, MARIA, inbox=11, outbox=10)
    db.commit()
    a = Archive(store.path)
    try:
        a.db.execute("INSERT INTO plugin_instance (plugin, kind, label, created_at) VALUES ('telegram', 'source', 'T', 0)")
        iid = a.db.execute("SELECT max(id) FROM plugin_instance").fetchone()[0]
        telegram.run(a, db_path=str(path))
        a.db.execute("UPDATE source SET instance_id = ? WHERE name = 'telegram'", (iid,))
        telegram.run(a, db_path=str(path))      # again, now with an instance: the reads are reported
        a.db.commit()
    finally:
        a.db.close()
    r = store.read()
    group = r.execute("SELECT c.id FROM conversation c JOIN service s ON s.id = c.service_id "
                      "WHERE s.name = 'telegram' AND c.key = ?", (str(GROUP),)).fetchone()[0]
    named = r.execute("SELECT a.value, n.token FROM mention n JOIN address a ON a.id = n.address_id "
                      "JOIN message m ON m.id = n.message_id WHERE m.conversation_id = ? ORDER BY n.token", (group,)).fetchall()
    assert named == [("+15557770001", "@maria_k"), (str(BOB), "Bob")]
    members = {v for (v,) in r.execute("SELECT a.value FROM conversation_member cm JOIN address a ON a.id = cm.address_id "
                                       "WHERE cm.conversation_id = ?", (group,))}
    assert members == {"+15557770001", str(BOB)}          # whoever wrote there; not an anonymous admin
    chat = r.execute("SELECT c.id FROM conversation c JOIN service s ON s.id = c.service_id "
                     "WHERE s.name = 'telegram' AND c.key = ?", (str(MARIA),)).fetchone()[0]
    got = r.execute("SELECT m.key, r.read_at FROM receipt r JOIN message m ON m.id = r.message_id "
                    "WHERE m.conversation_id = ? ORDER BY m.key", (chat,)).fetchall()
    assert got == [("10", 0)]                               # read, when not known
    read = r.execute("SELECT value FROM state_report WHERE conversation_id = ? AND field = 'read_until'", (chat,)).fetchone()
    assert read == ((1_790_000_000 + 11) * 1000,)

    # seen live: the other read up to 13 now; 10 keeps its "known, not when"
    telegram_store.note_read(db, MARIA, outbox=13, seen_now=True)
    db.commit()
    a = Archive(store.path)
    try:
        telegram.reads(a, db_path=str(path), chats={MARIA})
        a.db.commit()
    finally:
        a.db.close()
    got = dict(r.execute("SELECT m.key, r.read_at FROM receipt r JOIN message m ON m.id = r.message_id "
                         "WHERE m.conversation_id = ?", (chat,)).fetchall())
    assert got["10"] == 0 and got["12"] > 0 and got["12"] == got["13"] and "11" not in got


def viber_db(path):
    db = sqlite3.connect(path)
    db.executescript("""
    CREATE TABLE ZMEMBER (Z_PK INTEGER PRIMARY KEY, ZMEMBERID TEXT);
    CREATE TABLE ZPHONENUMBER (ZMEMBER INTEGER, ZCANONIZEDPHONENUM TEXT, ZPHONE TEXT);
    CREATE TABLE Z_5PHONENUMINDEXES (Z_5CONVERSATIONS INTEGER, Z_10PHONENUMINDEXES INTEGER);
    CREATE TABLE ZCONVERSATION (Z_PK INTEGER PRIMARY KEY, ZGROUPID INTEGER, ZNAME TEXT, ZSUBTYPE INTEGER,
        ZLASTREADTOKEN INTEGER, ZSEENSTATUSLASTTOKEN INTEGER);
    CREATE TABLE ZATTACHMENT (Z_PK INTEGER PRIMARY KEY, ZTYPE TEXT);
    CREATE TABLE ZVIBERLOCATION (Z_PK INTEGER PRIMARY KEY, ZLATITUDE REAL, ZLONGITUDE REAL, ZADDRESS TEXT);
    CREATE TABLE ZVIBERMESSAGE (Z_PK INTEGER PRIMARY KEY, ZSTATE TEXT, ZSYSTEMTYPE TEXT, ZATTACHMENT INTEGER,
        ZCONVERSATION INTEGER, ZDATE REAL, ZTOKEN INTEGER, ZPHONENUMINDEX INTEGER, ZTEXT TEXT, ZMETADATA TEXT,
        ZCLIENTMETADATA TEXT, ZCALLTYPE INTEGER, ZLIKESCOUNT INTEGER, ZLOCATION INTEGER, ZCALLSCOUNT INTEGER,
        ZFORWARDTYPE INTEGER, ZLIKESTYPE INTEGER);
    INSERT INTO ZMEMBER VALUES (1, 'mid-anna'), (2, 'mid-nick');
    INSERT INTO ZPHONENUMBER VALUES (1, '+15558880001', NULL), (2, '+15558880002', NULL);
    INSERT INTO Z_5PHONENUMINDEXES VALUES (10, 1), (20, 1), (20, 2);
    INSERT INTO ZCONVERSATION VALUES (10, NULL, NULL, 0, 1002, 1003), (20, 4242, 'Team', 0, NULL, NULL);
    """)
    date = 1_790_000_000 - APPLE_EPOCH
    mention = json.dumps({"textMetaInfo": [{"type": 0, "memberId": "mid-nick", "start": 4, "end": 11}]})
    rows = [(1001, "delivered", 10, None, "one", None), (1002, "received", 10, 1, "two", None),
            (1003, "delivered", 10, None, "three", None), (1004, "delivered", 10, None, "four", None),
            (2001, "received", 20, 1, "hey ‪@Nick‬, look", mention)]
    for i, (token, state, conv, sender, text, md) in enumerate(rows):
        db.execute("INSERT INTO ZVIBERMESSAGE (Z_PK, ZSTATE, ZSYSTEMTYPE, ZCONVERSATION, ZDATE, ZTOKEN, ZPHONENUMINDEX, "
                   "ZTEXT, ZMETADATA, ZLIKESCOUNT) VALUES (?, ?, '', ?, ?, ?, ?, ?, ?, 0)",
                   (i + 1, state, conv, date + i, token, sender, text, md))
    db.commit()
    db.close()


def test_viber_mentions_seen_and_read(store, tmp_path):
    path = tmp_path / "viber.sqlite"
    viber_db(path)
    a = Archive(store.path)
    try:
        a.db.execute("INSERT INTO plugin_instance (plugin, kind, label, created_at) VALUES ('iphone-backup', 'source', 'P', 0)")
        iid = a.db.execute("SELECT max(id) FROM plugin_instance").fetchone()[0]
        a.db.execute("INSERT INTO source (name, path, instance_id) VALUES (?, ?, ?)", (f"{IPHONE}/viber", str(path), iid))
        viber.run(a, iphone_db=str(path), desktop_db=None)
        a.db.commit()
    finally:
        a.db.close()
    r = store.read()
    named = r.execute("SELECT a.value, n.token FROM mention n JOIN address a ON a.id = n.address_id "
                      "JOIN message m ON m.id = n.message_id WHERE m.key = '2001'").fetchall()
    assert named == [("+15558880002", "‪@Nick‬")]
    seen = r.execute("SELECT m.key, a.value, r.read_at, r.delivered_at FROM receipt r JOIN message m ON m.id = r.message_id "
                     "JOIN address a ON a.id = r.address_id WHERE m.key LIKE '100%' ORDER BY m.key").fetchall()
    assert seen == [("1001", "+15558880001", 0, None), ("1003", "+15558880001", 0, None)]     # not 1004, not theirs
    conv = r.execute("SELECT conversation_id FROM message WHERE key = '1001'").fetchone()[0]
    read = r.execute("SELECT value FROM state_report WHERE conversation_id = ? AND field = 'read_until'", (conv,)).fetchone()
    assert read == ((1_790_000_000 + 1) * 1000,)


def test_a_read_seen_late_is_known_but_not_when(tmp_path):
    """The other read further while nothing was connected: not the time last seen live (which would
    be earlier than some of those messages), but known, not when."""
    db = telegram_store.open_store(str(tmp_path / "t.db"))
    assert telegram_store.note_read(db, MARIA, outbox=10, seen_now=True)
    assert db.execute("SELECT outbox_at FROM chat_read").fetchone()[0]
    assert not telegram_store.note_read(db, MARIA, outbox=10)               # the same: nothing moved
    assert db.execute("SELECT outbox_at FROM chat_read").fetchone()[0]
    assert telegram_store.note_read(db, MARIA, outbox=20)
    assert db.execute("SELECT outbox, outbox_at FROM chat_read").fetchone() == (20, None)


def test_telegram_mentions_sent_each_at_its_place(store):
    """Two people named: each "@" dropped, each link where its name is in the text as sent (UTF-16)."""
    import asyncio
    from types import SimpleNamespace
    from chronika.plugins import telegram_live
    with store.write() as w:
        for uid in ("301", "302"):
            w.execute("INSERT INTO address (kind_id, value, service_id) SELECT k.id, ?, s.id FROM address_kind k, "
                      "service s WHERE k.name = 'id' AND s.name = 'telegram'", (uid,))
            w.execute("INSERT INTO person (name) VALUES (?)", (uid,))
            w.execute("INSERT INTO person_address (person_id, address_id) SELECT max(p.id), a.id FROM person p, address a "
                      "WHERE a.value = ? GROUP BY a.id", (uid,))
    ids = dict(store.read().execute("SELECT value, id FROM address WHERE value IN ('301', '302')"))

    class Client:
        async def get_input_entity(self, uid):
            from telethon import types
            return types.InputPeerUser(uid, 0)

    ctx = SimpleNamespace(store=store)
    text = "🙂 @Ann and @Bob!"
    out, entities = asyncio.run(telegram_live._entities(ctx, Client(), text, [
        {"start": 11, "length": 4, "address_id": ids["302"]}, {"start": 2, "length": 4, "address_id": ids["301"]}], 0))
    assert out == "🙂 Ann and Bob!"
    units = out.encode("utf-16-le")
    said = [(units[e.offset * 2:(e.offset + e.length) * 2].decode("utf-16-le"), e.user_id.user_id) for e in entities]
    assert said == [("Ann", 301), ("Bob", 302)]


def test_viber_mention_after_an_emoji(store, tmp_path):
    """Viber counts where a mention is in UTF-16 units: an emoji before it is two."""
    path = tmp_path / "viber.sqlite"
    viber_db(path)
    db = sqlite3.connect(path)
    text = "😀 ‪@Nick‬ hi"
    db.execute("UPDATE ZVIBERMESSAGE SET ZTEXT = ?, ZMETADATA = ? WHERE ZTOKEN = 2001",
               (text, json.dumps({"textMetaInfo": [{"type": 0, "memberId": "mid-nick", "start": 3, "end": 10},
                                                   {"type": 0, "memberId": "mid-nick"}]})))
    db.commit()
    db.close()
    a = Archive(store.path)
    try:
        viber.run(a, iphone_db=str(path), desktop_db=None)
        a.db.commit()
    finally:
        a.db.close()
    token = store.read().execute("SELECT n.token FROM mention n JOIN message m ON m.id = n.message_id "
                                 "WHERE m.key = '2001'").fetchone()[0]
    assert token == "‪@Nick‬"


def test_all_who_got_it_are_those_the_service_named_then(store):
    """In a group, "all" is whoever got the user's messages there about then; one who left, or came
    later, is not waited for; a person by two addresses counts once."""
    from chronika.core import queries
    r = store.read()
    conv, mid, ts = r.execute("SELECT m.conversation_id, m.id, m.ts FROM message m JOIN conversation c ON c.id = m.conversation_id "
                              "JOIN receipt x ON x.message_id = m.id WHERE c.is_group AND m.outgoing "
                              "GROUP BY m.id HAVING count(*) > 2 ORDER BY m.ts DESC LIMIT 1").fetchone()
    members = [a for (a,) in r.execute("SELECT address_id FROM conversation_member WHERE conversation_id = ? "
                                       "AND address_id NOT IN (SELECT address_id FROM account)", (conv,))]
    gone = members[0]
    with store.write() as w:                # one never got anything there: as one who left
        w.execute("DELETE FROM receipt WHERE address_id = ? AND message_id IN "
                  "(SELECT id FROM message WHERE conversation_id = ?)", (gone, conv))
        w.execute("UPDATE receipt SET read_at = coalesce(read_at, ?) WHERE message_id = ?", (ts + 1000, mid))
    m = queries.hydrate(store, [("m", (mid, ts))])[0]
    assert m["receipts"]["to"] == len(members) - 1 and m["receipts"]["read"] == m["receipts"]["to"]
    who = queries.receipts(store, mid)
    assert len(who) == len(members) - 1 and all(x["read_at"] for x in who)
