"""The Adium and Pidgin logs importer, on invented logs of both programs."""
import os
import plistlib

from everysaid import imlogs
from everysaid.archive import Archive

ADIUM_XML = """<?xml version="1.0" encoding="UTF-8" ?>
<chat xmlns="http://purl.org/net/ulf/ns/0.4-02" account="me@hotmail.com" service="MSN">
<event type="windowOpened" sender="me@hotmail.com" time="2008-06-16T13:58:55+03:00"/>
<message sender="friend@hotmail.com" time="2008-06-16T13:58:55+03:00" alias="Ο &quot;Φίλος&quot; &amp; co"><div>hello &amp; welcome<br/>second line</div></message>
<message sender="me@hotmail.com" time="2008-06-16T13:59:01+03:00" alias="me"><div><span>hi</span></div></message>
<status type="away" sender="friend@hotmail.com" time="2008-06-16T14:00:00+03:00"/>
<message sender="friend@hotmail.com" time="2008-06-16T14:01:02+0300"><div><img src="pic.png" alt=""/></div></message>
<message sender="friend@hotmail.com" time="2008-06-16T14:01:02+0300"><div><img src="pic.png" alt=""/></div></message>
</chat>
"""
ADIUM_HTML = ('﻿<div class="receive"><span class="timestamp">11:59:47 PM</span> <span class="sender">pal: </span>'
              '<pre class="message">kalhspera!</pre></div>\n'
              '<div class="send"><span class="timestamp">12:00:01 AM</span> <span class="sender">myaim: </span>'
              '<pre class="message">kalws ton</pre></div>\n'
              '<div class="status"><span class="timestamp">12:00:05 AM</span> pal went away</div>\n')
PIDGIN_TXT = """Conversation with friend@hotmail.com at 2005-06-07 21:06:30 on me@hotmail.com (msn)
(21:06:32) alex: hello
(21:06:35) O Filos: hi there
and a second line
(21:07:14) The privacy status of the current conversation is now: Unverified
(21:08:00) Ο χρήστης O Filos έχει κλείσει το παράθυρο συνομιλίας.
"""
PIDGIN_TXT2 = """Conversation with other@hotmail.com at 2005-06-08 10:00:00 on me@hotmail.com (msn)
(10:00:01) alex: hey
(10:00:05) Other One: yo
"""
PIDGIN_TXT3 = """Conversation with third@hotmail.com at 2005-06-09 10:00:00 on me@hotmail.com (msn)
(10:00:01) alex: hey
(10:00:05) Third: yo
"""
BLIST = """<?xml version='1.0' encoding='UTF-8' ?>
<purple version='1.0'><blist><group name='Buddies'><contact>
<buddy account='me@hotmail.com' proto='prpl-msn'><name>friend@hotmail.com</name><alias>Φίλιππος</alias></buddy>
</contact><contact>
<buddy account='me@hotmail.com' proto='prpl-msn'><name>other@hotmail.com</name></buddy>
<buddy account='me@hotmail.com' proto='prpl-msn'><name>third@hotmail.com</name></buddy>
</contact></group></blist></purple>
"""
CONTACT_LIST = {"MetaContact Ownership": {"MetaContact-1": [{"UID": "friend@hotmail.com", "ServiceID": "MSN"},
                                                             {"UID": "pal", "ServiceID": "AIM"}],
                                          "MetaContact-2": [{"UID": "nobody@hotmail.com", "ServiceID": "MSN"}]}}
ACCOUNTS = """<?xml version='1.0' encoding='UTF-8' ?>
<account version='1.0'><account><protocol>prpl-msn</protocol><name>me@hotmail.com</name><alias>alex</alias></account></account>
"""


def build(tmp_path):
    adium = tmp_path / "Adium 2.0" / "Users" / "Default" / "Logs"
    msn = adium / "MSN.me@hotmail.com" / "friend@hotmail.com"
    log = msn / "friend@hotmail.com (2008-06-16T13.58.55+0300).chatlog"
    log.mkdir(parents=True)
    (log / "friend@hotmail.com (2008-06-16T13.58.55+0300).xml").write_text(ADIUM_XML, encoding="utf-8")
    (log / "pic.png").write_bytes(b"\x89PNG\r\n\x1a\n" + b"0" * 20)
    aim = adium / "AIM.myaim" / "pal"
    aim.mkdir(parents=True)
    (aim / "pal (2006-11-06).AdiumHTMLLog").write_text(ADIUM_HTML, encoding="utf-8")
    (adium.parent / "Contact Album" / "MSN.me@hotmail.com").mkdir(parents=True)     # the same account folders, no logs
    with open(adium.parent / "Contact List.plist", "wb") as f:
        plistlib.dump(CONTACT_LIST, f)
    purple = tmp_path / "purple"
    (purple / "logs" / "msn" / "me@hotmail.com" / "friend@hotmail.com").mkdir(parents=True)
    (purple / "logs" / "msn" / "me@hotmail.com" / "friend@hotmail.com" / "2005-06-07.210630.txt").write_text(PIDGIN_TXT, encoding="utf-8")
    for contact, text in (("other@hotmail.com", PIDGIN_TXT2), ("third@hotmail.com", PIDGIN_TXT3)):
        (purple / "logs" / "msn" / "me@hotmail.com" / contact).mkdir()
        (purple / "logs" / "msn" / "me@hotmail.com" / contact / "2005-06-08.100000+0300EEST.txt").write_text(text, encoding="utf-8")
    (purple / "blist.xml").write_text(BLIST, encoding="utf-8")
    (purple / "accounts.xml").write_text(ACCOUNTS, encoding="utf-8")
    return str(tmp_path / "Adium 2.0"), str(purple)


def test_adium_and_pidgin_logs(tmp_path):
    adium, purple = build(tmp_path)
    a = Archive(str(tmp_path / "archive.db"))
    stats = imlogs.run(a, adium, purple, media=False, out=lambda *x: None)
    assert stats.added[("adium", "msn")] == 3            # two texts and one picture; its repeat is a dupe
    assert stats.dupes[("adium", "msn")] == 1
    assert stats.added[("adium", "aim")] == 2
    assert stats.added[("pidgin", "msn")] == 6
    assert stats.problems == {}
    db = a.db
    # one MSN conversation with the friend, from both programs
    rows = db.execute("SELECT m.ts, m.outgoing, m.text, k.name FROM message m JOIN conversation c ON c.id = m.conversation_id "
                      "JOIN message_kind k ON k.id = m.kind_id WHERE c.key = 'friend@hotmail.com' ORDER BY m.ts").fetchall()
    assert [r[1] for r in rows] == [1, 0, 0, 1, 0]
    assert rows[0][2] == "hello" and rows[1][2] == "hi there\nand a second line"      # Pidgin: owner by alias, lines joined
    assert rows[2][2] == "hello & welcome\nsecond line" and rows[4][3] == "image"      # Adium: entities and breaks
    assert rows[2][0] == 1213613935000                                                  # 13:58:55 +03:00
    # the legacy HTML log: AM/PM, past midnight, direction by class
    aim = db.execute("SELECT m.ts, m.outgoing, m.text FROM message m JOIN service s ON s.id = m.service_id "
                     "WHERE s.name = 'aim' ORDER BY m.ts").fetchall()
    assert [r[1] for r in aim] == [0, 1] and aim[1][0] - aim[0][0] == 14000
    # names: Adium's alias (chat), Pidgin's buddy list (book)
    names = dict(db.execute("SELECT kind, name FROM handle_name").fetchall())
    assert names == {"chat": 'Ο "Φίλος" & co', "book": "Φίλιππος"}     # entities read
    # the owner's old groupings: the friend and the AIM pal are one person, as are "other" and "third"
    def person_of(kind, value, service=None):
        return db.execute("SELECT pa.person_id FROM person_address pa JOIN address a ON a.id = pa.address_id "
                          "JOIN address_kind k ON k.id = a.kind_id LEFT JOIN service s ON s.id = a.service_id "
                          "WHERE k.name = ? AND a.value = ? AND s.name IS ?", (kind, value, service)).fetchone()[0]
    assert person_of("email", "friend@hotmail.com") == person_of("id", "pal", "aim")
    assert person_of("email", "other@hotmail.com") == person_of("email", "third@hotmail.com")
    assert person_of("email", "friend@hotmail.com") != person_of("email", "other@hotmail.com")
    assert stats.merged == {"adium": 1, "pidgin": 1}
    # the owner's accounts
    own = {v for _, v in db.execute("SELECT s.name, ad.value FROM account x JOIN address ad ON ad.id = x.address_id "
                                    "LEFT JOIN service s ON s.id = x.service_id")}
    assert {"me@hotmail.com", "myaim"} <= own
    # a second run adds nothing
    again = imlogs.run(a, adium, purple, media=False, out=lambda *x: None)
    assert sum(again.added.values()) == 0 and sum(again.seen.values()) == 11
    a.db.close()


def test_handles():
    assert imlogs.handle("jabber", "x@gmail.com/Pidgin") == ("email", "x@gmail.com")
    assert imlogs.handle("jabber", "-123@chat.facebook.com") == ("id", "123", "messenger")
    assert imlogs.handle("messenger", "100000000000001") == ("id", "100000000000001", "messenger")
    assert imlogs.handle("icq", "123456789") == ("id", "123456789", "icq")
    assert imlogs.handle("aim", "Some Name") == ("id", "somename", "aim")
    assert imlogs.handle("whatsapp", "15555550123") == ("phone", "+15555550123")
    assert imlogs.is_group("msn", "msn%20chat@hotmail.com.chat")
    assert imlogs.is_group("jabber", "-600000001@chat.facebook.com")
    assert imlogs.is_group("aim", "chat100000000000000000")
    assert not imlogs.is_group("jabber", "friend@jabber.org")


def test_folders_are_found(tmp_path):
    adium, purple = build(tmp_path)
    assert imlogs.adium_logs(adium).endswith(os.path.join("Users", "Default", "Logs"))
    assert imlogs.adium_logs(os.path.join(adium, "Users", "Default", "Logs")).endswith("Logs")
    assert imlogs.adium_logs(str(tmp_path / "nowhere")) is None
    assert imlogs.pidgin_root(purple) == (purple, os.path.join(purple, "logs"))
    assert imlogs.pidgin_root(os.path.join(purple, "logs")) == (None, os.path.join(purple, "logs"))


def test_a_log_with_no_messages_makes_no_chat(tmp_path):
    adium, purple = build(tmp_path)
    quiet = os.path.join(adium, "Users", "Default", "Logs", "MSN.me@hotmail.com", "quiet@hotmail.com",
                         "quiet@hotmail.com (2008-06-17T10.00.00+0300).chatlog")
    os.makedirs(quiet)
    with open(os.path.join(quiet, "quiet@hotmail.com (2008-06-17T10.00.00+0300).xml"), "w", encoding="utf-8") as f:
        f.write('<?xml version="1.0" encoding="UTF-8" ?>\n<chat xmlns="http://purl.org/net/ulf/ns/0.4-02" '
                'account="me@hotmail.com" service="MSN"><event type="windowOpened" sender="me@hotmail.com" '
                'time="2008-06-17T10:00:00+03:00"/></chat>\n')
    a = Archive(str(tmp_path / "archive.db"))
    imlogs.run(a, adium, purple, media=False, out=lambda *x: None)
    read = {c: f for _, _, c, f in imlogs.adium_conversations(imlogs.adium_logs(adium))}
    assert read["quiet@hotmail.com"]                    # read, with nothing in it
    assert not a.db.execute("SELECT 1 FROM conversation_member cm JOIN address ad ON ad.id = cm.address_id "
                            "WHERE ad.value = 'quiet@hotmail.com'").fetchone()
    assert a.db.execute("SELECT count(*) FROM conversation c WHERE NOT EXISTS "
                        "(SELECT 1 FROM message m WHERE m.conversation_id = c.id)").fetchone()[0] == 0
    a.db.close()
