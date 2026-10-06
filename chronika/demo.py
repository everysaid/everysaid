"""A demo archive of invented people, for trying the app and for its tests: nothing in it is real.

    chronika demo [--dir DIR] [--seed N] [--serve]

Everything goes under DIR (default `./demo`): `data/` (the archive, the media), `cache/`, `config/`
(its own config.toml). The command runs itself again with CHRONIKA_DATA, CHRONIKA_CACHE and
CHRONIKA_CONFIG pointing there, so neither the user's archive nor their settings are touched.
With --serve it then starts the app on it.
"""
import argparse
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
import random
import subprocess
import sys

FIRST = ["Ελένη", "Νίκος", "Μαρία", "Γιάννης", "Κατερίνα", "Δημήτρης", "Σοφία", "Αλέξης", "Άννα", "Κώστας",
         "Δέσποινα", "Στέλιος", "Ειρήνη", "Θανάσης", "Χριστίνα", "Μιχάλης", "Emma", "Lucas", "Olivia", "Noah",
         "Mia", "Leo", "Clara", "Hugo", "Ingrid", "Mateo"]
LAST = ["Παπαδοπούλου", "Γεωργίου", "Οικονόμου", "Αντωνίου", "Νικολάου", "Βασιλείου", "Μακρή", "Ιωάννου",
        "Schmidt", "Rossi", "Dubois", "Novak", "Larsen", "García"]
LINES = {
    "el": ["Καλημέρα! Τι κάνεις;", "Όλα καλά, εσύ;", "Θα βρεθούμε το Σάββατο;", "Ναι, στις 8 στο γνωστό μέρος",
           "Τέλεια 👍", "Πήρες τα εισιτήρια;", "Ακόμα όχι, αύριο", "Χρόνια πολλά!! 🎉", "Ευχαριστώ πολύ ❤️",
           "Πού είσαι;", "Έρχομαι σε 10'", "Δες αυτό 😂", "Πολύ ωραίο!", "Θα σε πάρω τηλέφωνο αργότερα",
           "Καληνύχτα", "Έφτασες;", "Ναι, μόλις", "Μην ξεχάσεις το ψωμί", "Το απόγευμα είμαι ελεύθερος",
           "Τι ώρα κλείνει το φαρμακείο;", "Νομίζω στις 9", "Φιλιά στα παιδιά", "Εντάξει, τα λέμε",
           "Ο καιρός αύριο λέει βροχή", "Πάμε για καφέ;", "Συγγνώμη, ήμουν σε σύσκεψη", "Μπράβο σου!",
           "Στείλε μου τη διεύθυνση", "Κλείσαμε για Ιούλιο στη Νάξο", "Πόσο έκανε τελικά;"],
    "en": ["Good morning! How are you?", "All good, you?", "Are we meeting on Saturday?", "Yes, 8pm at the usual place",
           "Perfect 👍", "Did you get the tickets?", "Not yet, tomorrow", "Happy birthday!! 🎉", "Thanks so much ❤️",
           "Where are you?", "Coming in 10", "Look at this 😂", "Lovely!", "I'll call you later", "Good night",
           "Did you arrive?", "Yes, just now", "Don't forget the bread", "I'm free in the afternoon",
           "Coffee?", "Sorry, I was in a meeting", "Well done!", "Send me the address", "How much was it in the end?"],
}
GROUPS = [("Οικογένεια", "whatsapp", "el"), ("Ποδόσφαιρο Τετάρτης", "viber", "el"),
          ("Book club", "telegram", "en"), ("Γειτονιά", "whatsapp", "el")]
SERVICES = ["whatsapp", "viber", "sms", "telegram", "imessage"]
REACTIONS = ["❤️", "😂", "👍", "😮", "🙏", "🎉"]
DAY = 86400_000


def picture(path, seed, label):
    """A made-up picture: a gradient with a few shapes and a label."""
    from PIL import Image, ImageDraw
    rnd = random.Random(seed)
    w, h = rnd.choice([(1200, 900), (900, 1200), (1080, 1080)])
    a = [rnd.randrange(40, 220) for _ in range(3)]
    b = [rnd.randrange(40, 220) for _ in range(3)]
    img = Image.new("RGB", (w, h))
    px = ImageDraw.Draw(img)
    for y in range(h):
        t = y / h
        px.line([(0, y), (w, y)], fill=tuple(int(a[i] * (1 - t) + b[i] * t) for i in range(3)))
    for _ in range(rnd.randrange(3, 8)):
        x0, y0 = rnd.randrange(w), rnd.randrange(h)
        r = rnd.randrange(40, 260)
        c = tuple(rnd.randrange(255) for _ in range(3))
        (px.ellipse if rnd.random() < .5 else px.rectangle)([x0 - r, y0 - r, x0 + r, y0 + r], fill=c)
    px.text((40, h - 80), label, fill=(255, 255, 255))
    img.save(path, "JPEG", quality=82)


def avatar(path, seed, initials):
    from PIL import Image, ImageDraw
    rnd = random.Random(seed)
    img = Image.new("RGB", (256, 256), tuple(rnd.randrange(60, 200) for _ in range(3)))
    d = ImageDraw.Draw(img)
    d.ellipse([60, 40, 196, 176], fill=tuple(rnd.randrange(150, 255) for _ in range(3)))
    d.ellipse([20, 170, 236, 380], fill=tuple(rnd.randrange(150, 255) for _ in range(3)))
    d.text((110, 100), initials, fill=(30, 30, 30))
    img.save(path, "JPEG", quality=85)


def build(seed=7):
    from . import config, media as media_mod
    from .archive import Archive
    rnd = random.Random(seed)
    path = os.path.join(config.DATA, "archive.db")
    if os.path.exists(path):
        os.remove(path)
    a = Archive(path)
    a.account(("phone", "+15550000000"))
    now = datetime.now(timezone.utc).replace(minute=0, second=0, microsecond=0)
    start = now - timedelta(days=3 * 365)
    store = media_mod.Store(a)
    pics = os.path.join(config.CACHE, "demo-src")
    os.makedirs(pics, exist_ok=True)
    instances = {}

    def instance(plugin, label, kind="source"):
        if (plugin, label) not in instances:
            instances[(plugin, label)] = a.db.execute(
                "INSERT INTO plugin_instance (plugin, kind, label, created_at, last_run, last_status) VALUES (?, ?, ?, ?, ?, 'ok')",
                (plugin, kind, label, int(now.timestamp()), int(now.timestamp()))).lastrowid
        return instances[(plugin, label)]

    def source(service):
        plugin = {"sms": "iphone-backup", "imessage": "iphone-backup", "whatsapp": "whatsapp-bridge",
                  "viber": "iphone-backup", "telegram": "telegram"}[service]
        sid = a.source(f"demo/{service}", "demo", "demo-phone", pics)
        a.db.execute("UPDATE source SET instance_id = ? WHERE id = ?", (instance(plugin, "Demo phone"), sid))
        return sid

    people_ = []
    used = set()
    for i in range(26):
        while True:
            name = f"{rnd.choice(FIRST)} {rnd.choice(LAST)}"
            if name not in used:
                used.add(name)
                break
        number = f"+1555{1000000 + i * 7919:07d}"
        lang = "el" if any("Ͱ" <= ch <= "Ͽ" for ch in name) else "en"
        services = rnd.sample(SERVICES, rnd.choice([1, 2, 2, 3]))
        people_.append({"name": name, "number": number, "lang": lang, "services": services,
                        "weight": rnd.choice([1, 1, 2, 3, 8, 20]), "contact": i < 20})
    # contacts: an address book instance with most people in it, some with photos
    instance("demo-sender", "Demo sender", "source")         # sending, in the demo (see DemoSender)
    book = instance("vcard-file", "Contacts", "contacts")
    avatars = os.path.join(config.CACHE, "avatars")
    os.makedirs(avatars, exist_ok=True)
    for i, p in enumerate(people_):
        if not p["contact"]:
            continue
        photo = None
        if i % 3 != 2:
            f = os.path.join(avatars, f"demo{i}.jpg")
            avatar(f, seed + i, "".join(w[0] for w in p["name"].split()))
            with open(f, "rb") as fh:
                h = hashlib.sha256(fh.read()).hexdigest()
            photo = f"{h}.jpg"
            os.replace(f, os.path.join(avatars, photo))
        cid = a.db.execute("INSERT INTO contact (instance_id, uid, name, photo, updated_at) VALUES (?, ?, ?, ?, ?)",
                           (book, f"demo-{i}", p["name"], photo, int(now.timestamp()))).lastrowid
        a.db.execute("INSERT INTO contact_address (contact_id, address_id, label) VALUES (?, ?, 'mobile')",
                     (cid, a.address("phone", p["number"])))
    # the names services show: WhatsApp's copy of the address book (for those in it) and the names
    # people chose (a first name), Telegram profile names, so the ones not in the book have a name too
    for p in people_:
        a.address("phone", p["number"])
        if "whatsapp" in p["services"]:
            if p["contact"]:
                a.handle_name(("phone", p["number"]), "whatsapp", p["name"], "book")
            a.handle_name(("phone", p["number"]), "whatsapp", p["name"].split()[0], "profile")
        if "telegram" in p["services"]:
            a.handle_name(("phone", p["number"]), "telegram", p["name"].split()[0], "profile")
    pic_n = 0

    def add(service, conv, ts, outgoing, sender, lang, row, kind="text", text=None, extras=None):
        nonlocal pic_n
        src = source(service)
        x = dict(extras or {})
        key = None if service == "sms" else f"demo-{service}-{row}"
        txt = text if text is not None else rnd.choice(LINES[lang])
        if kind == "image":
            txt = rnd.choice([None, None, "📷", rnd.choice(LINES[lang])])
        mid = a.add_message(src, f"r{row}", service=service, conversation_id=conv, ts=ts, outgoing=outgoing,
                            sender_id=sender, kind=kind, text=txt, key=key, extras=x)
        if kind == "image":
            pic_n += 1
            f = os.path.join(pics, f"p{pic_n}.jpg")
            picture(f, seed * 1000 + pic_n, datetime.fromtimestamp(ts / 1000).strftime("%d/%m/%Y"))
            store.link(f"demo/{service}", src, f, f"p{pic_n}.jpg", mid)
        return mid

    row = 0
    for p in people_:
        aid = a.address("phone", p["number"])
        for service in p["services"]:
            conv = a.conversation(service, [("phone", p["number"])])
            n = p["weight"] * rnd.randrange(20, 60)
            t = start + timedelta(days=rnd.randrange(0, 700))
            prev = None
            for _ in range(n):
                t += timedelta(minutes=rnd.choice([1, 2, 5, 30, 120, 600, 1440, 4000]))
                if t > now:
                    break
                ts = int(t.timestamp() * 1000)
                out = rnd.random() < .45
                kind = "image" if service != "sms" and rnd.random() < .04 else "text"
                x = {}
                r = rnd.random()
                if prev and r < .06 and service != "sms":
                    x["reply_key"] = f"demo-{service}-{prev}"
                if service in ("whatsapp", "viber", "telegram", "imessage") and rnd.random() < .07:
                    x["reactions"] = [(rnd.choice(REACTIONS), None, 1, None if not out else ("phone", p["number"]),
                                       None if out else 1)]
                if rnd.random() < .01 and service == "whatsapp":
                    x["edited"] = 1
                if rnd.random() < .004 and service != "sms":
                    kind = "location"
                    x.update(lat=37.97 + rnd.random() / 10, lon=23.72 + rnd.random() / 10, place="Αθήνα")
                row += 1
                add(service, conv, ts, out, None if out else aid, p["lang"], row, kind, extras=x)
                prev = row
        # calls
        for _ in range(p["weight"] * rnd.randrange(2, 8)):
            ts = int((start + timedelta(minutes=rnd.randrange(0, 3 * 365 * 1440))).timestamp() * 1000)
            out = rnd.random() < .5
            answered = rnd.random() < .7
            service = rnd.choice(["phone", "phone", "whatsapp", "viber"])
            row += 1
            a.add_call(source("sms"), f"call{row}", service=service, address_id=aid, ts=ts, outgoing=out,
                       answered=answered, duration=rnd.randrange(20, 1800) if answered else 0,
                       detail=None if answered else ("unanswered" if out else "missed"),
                       video=service != "phone" and rnd.random() < .3, key=f"demo-call-{row}" if service != "phone" else None)
    # groups
    for title, service, lang in GROUPS:
        members = rnd.sample(people_, rnd.randrange(4, 9))
        conv = a.conversation(service, [("phone", m["number"]) for m in members], key=f"demo-group-{title}", title=title)
        a.db.execute("UPDATE conversation SET is_group = 1 WHERE id = ?", (conv,))
        t = start + timedelta(days=rnd.randrange(0, 300))
        for _ in range(rnd.randrange(300, 900)):
            t += timedelta(minutes=rnd.choice([1, 3, 10, 60, 300, 1440]))
            if t > now:
                break
            out = rnd.random() < .2
            m = rnd.choice(members)
            row += 1
            kind = "image" if rnd.random() < .03 else "text"
            named = rnd.choice([x for x in members if x is not m]) if kind == "text" and rnd.random() < .05 else None
            token = f"@{named['name'].split()[0]}" if named else None
            mid = add(service, conv, int(t.timestamp() * 1000), out, None if out else a.address("phone", m["number"]),
                      lang, row, kind, text=f"{token} {rnd.choice(LINES[lang])}" if named else None)
            if named:
                a.db.execute("INSERT INTO mention VALUES (?, ?, ?)", (mid, a.address("phone", named["number"]), token))
    # a few new messages of the last hours, unread
    for p in rnd.sample(people_, 6):
        service = p["services"][0]
        conv = a.conversation(service, [("phone", p["number"])])
        for k in range(rnd.randrange(1, 4)):
            row += 1
            ts = int((now - timedelta(hours=rnd.randrange(1, 30), minutes=k)).timestamp() * 1000)
            add(service, conv, ts, False, a.address("phone", p["number"]), p["lang"], row)
    # chats' state as services report it: one muted for ever, one pinned, one archived (which starts
    # the app's own archived: Archive.init_archived, at resolve())
    told = [p for p in people_ if "whatsapp" in p["services"] and len(p["services"]) == 1][:3]
    stamp = int(now.timestamp() * 1000)
    for (field, value), p in zip((("muted", -1), ("pinned", 1), ("archived", 1)), told):
        conv = a.conversation("whatsapp", [("phone", p["number"])])
        a.report_state(source("whatsapp"), conv, field, value, stamp - 86400000)
    # who got and read what the owner sent, where the service tells: WhatsApp (when), Telegram (in a
    # chat with one person, read but not when); the latest of each chat delivered, not read yet
    whatsapp, telegram = a.service["whatsapp"], a.service["telegram"]
    last = {c: m for c, m in a.db.execute("SELECT conversation_id, max(id) FROM message WHERE outgoing GROUP BY 1")}
    for mid, conv, ts, sid, group in a.db.execute(
            "SELECT m.id, m.conversation_id, m.ts, m.service_id, c.is_group FROM message m "
            "JOIN conversation c ON c.id = m.conversation_id WHERE m.outgoing AND m.service_id IN (?, ?)",
            (whatsapp, telegram)).fetchall():
        if sid == telegram and group:
            continue
        for (aid,) in a.db.execute("SELECT address_id FROM conversation_member WHERE conversation_id = ? AND "
                                   "address_id NOT IN (SELECT address_id FROM account)", (conv,)).fetchall():
            read = None if last.get(conv) == mid or (group and rnd.random() < .25) else ts + rnd.randrange(5, 3600) * 1000
            if sid == telegram:
                a.db.execute("INSERT INTO receipt (message_id, address_id, read_at) VALUES (?, ?, ?)",
                             (mid, aid, None if read is None else 0))
            else:
                a.db.execute("INSERT INTO receipt VALUES (?, ?, ?, ?, NULL)", (mid, aid, ts + 2000, read))
    # notes to self
    conv = a.conversation("viber", [("phone", "+15550000000")], key="demo-notes", title=None)
    for i, txt in enumerate(["Λίστα: γάλα, αυγά, καφές", "Κωδικός Wi-Fi γραφείου στο συρτάρι", "Ιδέα για δώρο: βιβλίο μαγειρικής"]):
        row += 1
        add("viber", conv, int((now - timedelta(days=30 - i * 7)).timestamp() * 1000), True, None, "el", row, text=txt)
    # a library: a folder
    lib = os.path.join(config.DATA, "library")
    os.makedirs(lib, exist_ok=True)
    a.db.execute("INSERT INTO plugin_instance (plugin, kind, label, settings, is_default, created_at) VALUES "
                 "('folder', 'library', 'Photos folder', ?, 1, ?)", (json.dumps({"path": lib}), int(now.timestamp())))
    a.resolve()
    a.db.execute("INSERT OR REPLACE INTO setting VALUES ('unread_since', ?)",
                 (json.dumps(int((now - timedelta(days=2)).timestamp() * 1000)),))
    a.db.commit()
    n = a.db.execute("SELECT count(*) FROM message").fetchone()[0]
    c = a.db.execute("SELECT count(*) FROM call").fetchone()[0]
    print(f"demo: {n} μηνύματα, {c} κλήσεις, {len(people_)} πρόσωπα, {len(GROUPS)} ομάδες, {pic_n} εικόνες -> {path}")
    a.db.close()
    return path


def demo_message(host, conversation_id, text, outgoing, reply_key=None, mentions=None, file=None):
    """Write a message into the demo archive as if a service had brought it (sent by the user, or
    from the other side of the conversation), and tell the apps; returns its id. mentions: [{start,
    length, address_id}] in the text; file: {data, filename, mime_type}, attached."""
    import time
    from . import config, media as media_mod
    from .archive import Archive
    with host.import_lock:
        a = Archive(host.store.path)
        try:
            src = a.source("demo/live", "demo")
            service = a.db.execute("SELECT s.name FROM conversation c JOIN service s ON s.id = c.service_id "
                                   "WHERE c.id = ?", (conversation_id,)).fetchone()[0]
            who = None if outgoing else a.db.execute(
                "SELECT address_id FROM conversation_member WHERE conversation_id = ? AND "
                "address_id NOT IN (SELECT address_id FROM account) LIMIT 1", (conversation_id,)).fetchone()
            m0 = a.db.execute("SELECT max(id) FROM message").fetchone()[0]
            n = time.time_ns()
            kind = "text"
            if file:
                mime = file.get("mime_type") or ""
                kind = "image" if mime.startswith("image/") else "video" if mime.startswith("video/") else "file"
            mid = a.add_message(src, f"live-{n}", service=service, conversation_id=conversation_id, ts=n // 1_000_000,
                                outgoing=outgoing, sender_id=who[0] if who else None, kind=kind, text=text or None,
                                key=None if service == "sms" else f"demo-live-{n}",
                                extras={"reply_key": reply_key} if reply_key else None)
            for m in mentions or ():
                a.db.execute("INSERT OR IGNORE INTO mention VALUES (?, ?, ?)",
                             (mid, m["address_id"], text[m["start"]:m["start"] + m["length"]]))
            if file:
                folder = os.path.join(config.CACHE, "demo-src")
                os.makedirs(folder, exist_ok=True)
                rel = f"live-{n}{os.path.splitext(file.get('filename') or '')[1].lower()}"
                with open(os.path.join(folder, rel), "wb") as f:
                    f.write(file["data"])
                media_mod.Store(a).link("demo/live", src, os.path.join(folder, rel), rel, mid)
            a.resolve()
            a.db.commit()
            m1 = a.db.execute("SELECT max(id) FROM message").fetchone()[0]
        finally:
            a.db.close()
    host.emit({"type": "new", "messages": [m0, m1], "calls": [0, 0]})
    return m1


def demo_receipt(host, message_id, field):
    """The members of the message's conversation got (field "delivered") or read ("read") it now."""
    import time
    from .archive import Archive
    with host.import_lock:
        a = Archive(host.store.path)
        try:
            for (aid,) in a.db.execute("SELECT address_id FROM conversation_member WHERE conversation_id = "
                                       "(SELECT conversation_id FROM message WHERE id = ?) AND address_id NOT IN "
                                       "(SELECT address_id FROM account)", (message_id,)).fetchall():
                a.db.execute("INSERT OR IGNORE INTO receipt (message_id, address_id) VALUES (?, ?)", (message_id, aid))
                a.db.execute(f"UPDATE receipt SET {field}_at = ? WHERE message_id = ? AND address_id = ?",
                             (int(time.time() * 1000), message_id, aid))
            a.db.commit()
        finally:
            a.db.close()
    host.emit({"type": "changed"})


class DemoSender:
    """A source that "sends" by writing into the demo archive, and gets an answer a moment later: only
    in the demo (CHRONIKA_DEMO), so that sending and receiving, and what the interface does after
    them, can be tried and tested. (The demo also takes incoming messages at /api/demo/incoming.)"""

    @staticmethod
    def plugin():
        from .plugins.base import Plugin

        class Sender(Plugin):
            id = "demo-sender"
            name = "Demo (sends into the demo archive)"
            services = ("whatsapp", "viber", "sms", "telegram")   # not iMessage: a chat it cannot send to
            description = "Invented: what is sent is only written into the demo archive."
            can_send = True
            can_reply = True
            can_mention = True
            can_mark_read = True
            can_send_files = True

            def check(self, ctx):
                return True, "ready"

            async def send(self, ctx, conversation, text, reply_to=None, mentions=None, file=None):
                import asyncio
                import threading
                await asyncio.sleep(0.8)        # as a real service takes a moment
                mid = await asyncio.to_thread(demo_message, ctx.host, conversation["id"], text, True,
                                              reply_to["key"] if reply_to else None, mentions, file)
                if conversation["service"] in ("whatsapp", "telegram"):     # they tell who got and read it
                    threading.Timer(1.0, demo_receipt, (ctx.host, mid, "delivered")).start()
                    threading.Timer(2.5, demo_receipt, (ctx.host, mid, "read")).start()
                if not text.startswith("quiet:"):   # the other side answers (a test may want silence)
                    threading.Timer(1.5, demo_message, (ctx.host, conversation["id"], f"↩ {text}", False)).start()
                return {"id": mid}

            async def mark_read(self, ctx, conversation, until):
                return 1

        return Sender()


def env_for(d):
    d = os.path.abspath(d)
    env = dict(os.environ, CHRONIKA_DATA=os.path.join(d, "data"), CHRONIKA_CACHE=os.path.join(d, "cache"),
               CHRONIKA_CONFIG=os.path.join(d, "config"), CHRONIKA_STATE=os.path.join(d, "state"),
               CHRONIKA_KEYRING="chronika-demo", CHRONIKA_DEMO="1")
    for k in ("CHRONIKA_DATA", "CHRONIKA_CACHE", "CHRONIKA_CONFIG", "CHRONIKA_STATE"):
        os.makedirs(env[k], exist_ok=True)
    cfg = os.path.join(env["CHRONIKA_CONFIG"], "config.toml")
    if not os.path.exists(cfg):
        with open(cfg, "w", encoding="utf-8") as f:
            f.write('[owner]\nnumbers = ["+15550000000"]\nregion = "US"\nname = "Demo"\n\n[server]\nport = 8530\n')
    return env


def main(argv=None):
    ap = argparse.ArgumentParser(prog="chronika demo", description="A demo archive of invented people.")
    ap.add_argument("--dir", default="demo")
    ap.add_argument("--seed", type=int, default=7)
    ap.add_argument("--serve", action="store_true", help="then start the app on it")
    ap.add_argument("--build-here", action="store_true", help=argparse.SUPPRESS)
    args = ap.parse_args(argv)
    if args.build_here:
        build(args.seed)
        return
    env = env_for(args.dir)
    subprocess.run([sys.executable, "-m", "chronika", "demo", "--build-here", "--seed", str(args.seed)],
                   env=env, check=True)
    if args.serve:
        os.execve(sys.executable, [sys.executable, "-m", "chronika", "serve"], env)
