"""Import the logs of the old multi-protocol messengers: Adium (macOS) and Pidgin or Gaim
(libpurple). Both kept one file per conversation session, under the account and the contact:

    Adium:  <Logs>/<Service>.<account>/<contact>/<contact> (<date>).chatlog/<same>.xml
            (and, from 2006, <contact> (<day>).AdiumHTMLLog), the chatlog folder also holding
            the pictures the messages showed
    Pidgin: <.purple>/logs/<protocol>/<account>/<contact>/<date>.txt (or .html), a group chat
            being a contact folder ending in `.chat`; `blist.xml` has the names the owner gave
            to buddies, `accounts.xml` the accounts

The services are those of their time: msn, icq, aim, yahoo, jabber (Google Talk included: the
same people, by the same addresses), skype, irc; Facebook chat (over XMPP, or Adium's own plugin)
is `messenger`, where Facebook's ids meet a later import of Messenger. A handle is an email
(MSN, Jabber: shared with every service, so a gmail or hotmail address joins the person's other
services and their address book) or an id within the service (ICQ numbers, AIM, Yahoo and Skype
names, Facebook ids), or a phone (Adium's WhatsApp plugin).

Messages have no ids: a message's key in the archive is its fingerprint (second, direction, kind,
text), the way SMS are told apart, and its origin the file and its index in it. The two programs
were used by turns, never at once, so the same message is not expected in both; a message whose
fingerprint its conversation already has is skipped and counted, whichever source it comes from.
Status lines (online, away, the window opened) are not messages and are left out.

Pidgin writes who said what by display name, not by handle: a message is the owner's when its
sender is one of the account's names (the account itself, its alias, and any name that speaks in
three or more of the account's conversations, which only the owner does); every other sender in a
one-to-one log is the contact. Adium writes handles, and the display names as `alias`, which go to
`handle_name` as the names the service showed (`chat`); Pidgin's buddy list gives the owner's own
names for people (`book`). Both programs also kept the owner's own grouping of handles into one
person (Adium's metacontacts in `Contact List.plist`, Pidgin's `<contact>` with several buddies):
the people of those handles are merged, as the owner had merged them then.
"""
import html
import os
import plistlib
import re
import urllib.parse
import xml.etree.ElementTree as ET
from collections import defaultdict
from datetime import datetime, timedelta, timezone

from . import config
from .archive import address, fingerprint
from .core import labels

TZ = config.TIMEZONE
DEVICES = {"adium": "adium", "pidgin": "pidgin"}
# the programs' names for the services -> ours
SERVICES = {"msn": "msn", "icq": "icq", "aim": "aim", "yahoo": "yahoo", "yahoo!": "yahoo", "jabber": "jabber",
            "gtalk": "jabber", "xmpp": "jabber", "skype": "skype", "irc": "irc", "facebook": "messenger",
            "whatsapp": "whatsapp", "mac": "aim", "bonjour": "bonjour", "novell": "novell", "sametime": "sametime"}
PROTOCOLS = {"prpl-msn": "msn", "prpl-icq": "icq", "prpl-aim": "aim", "prpl-oscar": "aim", "prpl-yahoo": "yahoo",
             "prpl-jabber": "jabber", "prpl-bigbrownchunx-skype": "skype", "prpl-skype": "skype",
             "prpl-skypeweb": "skype", "prpl-irc": "irc", "prpl-facebook": "messenger"}
FACEBOOK = re.compile(r"^-?(\d+)@chat\.facebook\.com$")
TAG = re.compile(r"<[^>]+>")
BR = re.compile(r"<br\s*/?>|</div>|</p>|</pre>", re.IGNORECASE)
IMG = re.compile(r'<img\b[^>]*\bsrc="([^"]+)"', re.IGNORECASE)
# Adium's chatlog: a message, its attributes and its body (self-closing ones are empty)
MESSAGE = re.compile(r"<message\b([^>]*?)(?:/>|>(.*?)</message>)", re.DOTALL)
ATTR = re.compile(r'(\w+)="([^"]*)"')
HTML_LINE = re.compile(r'<div class="(send|receive)"><span class="timestamp">([^<]*)</span> ?<span class="sender">'
                       r"([^<]*?):? ?</span> ?<pre class=\"message\">(.*?)</pre></div>", re.DOTALL)
# Pidgin: the file's date, and a line of a conversation
PIDGIN_FILE = re.compile(r"(\d{4})-(\d\d)-(\d\d)\.(\d{6})([+-]\d{4})?")
PIDGIN_LINE = re.compile(r"\((\d{1,2}):(\d\d):(\d\d)(?: ?([AP]M|πμ|μμ))?\) (.*)")
PIDGIN_SAID = re.compile(r"([^:]+?): (.*)", re.DOTALL)
# lines Pidgin writes as "(time) <phrase>: <detail>", which are not a message
PIDGIN_NOTICE = ("Unable to send message", "Αδυναμία αποστολής μηνύματος", "The privacy status of the current conversation",
                 "OTR Error", "Το μήνυμα δεν ήταν δυνατό να σταλεί", "Message could not be sent",
                 "Error", "Σφάλμα", "Unverified", "Private conversation", "Μη επιβεβαιωμένη", "Ιδιωτική συνομιλία")


def service_of(name):
    return SERVICES.get(name.lower())


def handle(service, raw):
    """(kind, value[, service]) of a handle as the programs name it: a JID without its resource,
    Facebook's XMPP ids as Messenger ids, phones for WhatsApp, else an id within the service."""
    s = urllib.parse.unquote(raw).strip()
    if "/" in s and "@" in s:               # a JID's resource
        s = s.split("/", 1)[0]
    fb = FACEBOOK.match(s.lower())
    if fb or service == "messenger":
        return "id", (fb.group(1) if fb else s.lstrip("-")), "messenger"
    if service == "whatsapp":
        return address("+" + s.lstrip("+"))
    if service in ("msn", "jabber") or "@" in s:
        return "email", s.lower()
    return "id", s.lower().replace(" ", ""), service


def is_group(service, raw):
    s = urllib.parse.unquote(raw).lower()
    if s.endswith(".chat"):
        return True
    if "@groupchat." in s or "@conference." in s or s.endswith("@thread.skype") or s.startswith("-") and FACEBOOK.match(s):
        return True
    return service == "aim" and re.fullmatch(r"chat\d+", s) is not None or service == "irc" and s.startswith("#")


def clean(markup):
    """Text out of a message's HTML: line breaks kept, tags gone, entities read."""
    text = BR.sub("\n", markup or "")
    text = TAG.sub("", text)
    return html.unescape(text).replace("\xa0", " ").replace("\ufeff", "").strip()


def with_zone(dt, offset=None):
    """A naive local time as an aware one: of the offset written, else the owner's zone."""
    if offset:
        sign = -1 if offset[0] == "-" else 1
        return dt.replace(tzinfo=timezone(sign * timedelta(hours=int(offset[1:3]), minutes=int(offset[3:5]))))
    return dt.replace(tzinfo=TZ)


def clock(h, m, s, ampm):
    if ampm in ("PM", "μμ") and h < 12:
        h += 12
    if ampm in ("AM", "πμ") and h == 12:
        h = 0
    return h, m, s


class Record:
    """One message as a parser gives it."""
    __slots__ = ("alias", "images", "outgoing", "row_key", "sender", "text", "ts")

    def __init__(self, row_key, ts, outgoing, sender, text, alias=None, images=()):
        self.row_key, self.ts, self.outgoing, self.sender = row_key, ts, outgoing, sender
        self.text, self.alias, self.images = text, alias, images


# ---------------------------------------------------------------- Adium

def find_dir(root, test, depth=4):
    """The first folder under root (root itself first, then by name) for which test holds."""
    if test(root):
        return root
    if depth == 0:
        return None
    for d in sorted(os.listdir(root)):
        sub = os.path.join(root, d)
        if os.path.isdir(sub) and not d.startswith("."):
            found = find_dir(sub, test, depth - 1)
            if found:
                return found
    return None


def is_adium_logs(folder):
    """A folder of <Service>.<uid> account folders holding contacts' logs (the Contact Album has
    the same account folders, with pictures)."""
    for d in os.listdir(folder):
        acc = os.path.join(folder, d)
        if "." in d and service_of(d.partition(".")[0]) and os.path.isdir(acc):
            for contact in os.listdir(acc):
                c = os.path.join(acc, contact)
                if os.path.isdir(c) and any(f.endswith((".chatlog", ".AdiumHTMLLog")) for f in os.listdir(c)):
                    return True
    return False


def adium_logs(root):
    """The Logs folder of an Adium folder given as the Logs folder itself, Users/Default or Adium 2.0."""
    return find_dir(root, is_adium_logs) if os.path.isdir(root) else None


def adium_conversations(logs):
    """(service, account uid, contact folder name, [files]) for every contact folder."""
    for accdir in sorted(os.listdir(logs)):
        name, _, uid = accdir.partition(".")
        service = service_of(name)
        if not uid or not service or not os.path.isdir(os.path.join(logs, accdir)):
            continue
        for contact in sorted(os.listdir(os.path.join(logs, accdir))):
            folder = os.path.join(logs, accdir, contact)
            if not os.path.isdir(folder):
                continue
            files = []
            for entry in sorted(os.listdir(folder)):
                path = os.path.join(folder, entry)
                if entry.endswith(".chatlog") and os.path.isdir(path):
                    files += [os.path.join(path, f) for f in sorted(os.listdir(path)) if f.endswith(".xml")]
                elif entry.endswith((".chatlog", ".xml")) and os.path.isfile(path) or entry.endswith(".AdiumHTMLLog"):
                    files.append(path)
            yield service, uid, contact, files


def adium_metacontacts(logs):
    """The owner's grouping of handles into people: [[(kind, value[, service]), ...]] from
    Contact List.plist beside the Logs folder, the groups of more than one handle."""
    path = os.path.join(os.path.dirname(logs), "Contact List.plist")
    if not os.path.exists(path):
        return []
    try:
        with open(path, "rb") as f:
            owned = plistlib.load(f).get("MetaContact Ownership", {})
    except (OSError, ValueError, plistlib.InvalidFileException):
        return []
    out = []
    for handles in owned.values():
        group = []
        for h in handles:
            service = service_of(h.get("ServiceID", ""))
            if service and h.get("UID"):
                group.append(handle(service, h["UID"]))
        if len(set(group)) > 1:
            out.append(sorted(set(group)))
    return out


def adium_time(value):
    """Adium's ISO time, with +0300 or +03:00; Unix ms."""
    v = re.sub(r"([+-]\d\d)(\d\d)$", r"\1:\2", value.strip())
    try:
        return int(datetime.fromisoformat(v).timestamp() * 1000)
    except ValueError:
        return None


def adium_file(path, logs, uid, stats):
    """The messages of one chatlog (XML) or HTML log, in order."""
    rel = os.path.relpath(path, logs)
    folder = os.path.dirname(path)
    with open(path, encoding="utf-8", errors="replace") as f:
        data = f.read()
    me = uid.lower()
    if path.endswith(".AdiumHTMLLog"):
        m = re.search(r"\((\d{4})-(\d\d)-(\d\d)\)", os.path.basename(path)) or \
            re.search(r" on (\d\d)-(\d\d)-(\d\d)\.", os.path.basename(path))      # the oldest: "on 06-10-24"
        if not m:
            stats["files without a date"] += 1
            return
        y, mo, d = map(int, m.groups())
        day = datetime(y if y > 99 else 2000 + y, mo, d, tzinfo=TZ)
        prev = None
        for n, mm in enumerate(HTML_LINE.finditer(data)):
            direction, stamp, sender, body = mm.groups()
            t = re.match(r"(\d{1,2}):(\d\d):(\d\d) ?([AP]M)?", stamp.strip())
            if not t:
                stats["lines not read"] += 1
                continue
            h, mi, s = clock(int(t.group(1)), int(t.group(2)), int(t.group(3)), t.group(4))
            dt = with_zone(day.replace(hour=h, minute=mi, second=s))
            if prev and dt < prev - timedelta(hours=1):     # past midnight
                dt += timedelta(days=1)
            prev = dt
            yield Record(f"{rel}#{n}", int(dt.timestamp() * 1000), direction == "send", sender.strip(), clean(body))
        return
    for n, mm in enumerate(MESSAGE.finditer(data)):
        attrs = dict(ATTR.findall(mm.group(1)))
        body = mm.group(2) or ""
        ts = adium_time(attrs.get("time", ""))
        sender = attrs.get("sender", "").strip()
        if ts is None or not sender:
            stats["lines not read"] += 1
            continue
        images = [os.path.relpath(os.path.join(folder, src), logs) for src in IMG.findall(body)
                  if os.path.isfile(os.path.join(folder, src))]
        yield Record(f"{rel}#{n}", ts, sender.lower() == me or sender.lower().startswith(me + "/"),
                     sender, clean(body), html.unescape(attrs.get("alias", "")).strip() or None, images)


# ---------------------------------------------------------------- Pidgin

def is_pidgin_logs(folder):
    return any(os.path.isdir(os.path.join(folder, d)) and service_of(d) for d in os.listdir(folder))


def pidgin_root(root):
    """(.purple folder or None, its logs folder) for a .purple folder or a logs folder."""
    if not os.path.isdir(root):
        return None, None
    purple = find_dir(root, lambda d: os.path.isdir(os.path.join(d, "logs")) and is_pidgin_logs(os.path.join(d, "logs")))
    if purple:
        return purple, os.path.join(purple, "logs")
    return None, find_dir(root, is_pidgin_logs)


def pidgin_accounts(purple):
    """{(protocol dir name, account uid): alias} from accounts.xml, where there is one."""
    out = {}
    path = purple and os.path.join(purple, "accounts.xml")
    if not path or not os.path.exists(path):
        return out
    for acc in ET.parse(path).iter("account"):
        proto, name, alias = (acc.findtext(t) for t in ("protocol", "name", "alias"))
        if proto and name:
            out[(proto.removeprefix("prpl-"), name.split("/", 1)[0].lower())] = (alias or "").strip()
    return out


def pidgin_buddies(purple):
    """[(service, buddy name, alias)] from blist.xml: the names the owner gave."""
    path = purple and os.path.join(purple, "blist.xml")
    if not path or not os.path.exists(path):
        return []
    out = []
    for b in ET.parse(path).iter("buddy"):
        service = PROTOCOLS.get(b.get("proto", ""))
        name, alias = b.findtext("name"), b.findtext("alias")
        if service and name and alias and alias.strip():
            out.append((service, name.strip(), alias.strip()))
    return out


def pidgin_contacts(purple):
    """The owner's grouping of buddies into one contact, from blist.xml: the contacts of more than
    one handle, as [[(kind, value[, service]), ...]]."""
    path = purple and os.path.join(purple, "blist.xml")
    if not path or not os.path.exists(path):
        return []
    out = []
    for c in ET.parse(path).iter("contact"):
        group = set()
        for b in c.iter("buddy"):
            service, name = PROTOCOLS.get(b.get("proto", "")), b.findtext("name")
            if service and name:
                group.add(handle(service, name.strip()))
        if len(group) > 1:
            out.append(sorted(group))
    return out


def pidgin_conversations(logs):
    """(protocol dir, service, account uid, contact folder name, [files])."""
    for proto in sorted(os.listdir(logs)):
        service = service_of(proto)
        if not service or not os.path.isdir(os.path.join(logs, proto)):
            continue
        for acc in sorted(os.listdir(os.path.join(logs, proto))):
            accdir = os.path.join(logs, proto, acc)
            if not os.path.isdir(accdir):
                continue
            for contact in sorted(os.listdir(accdir)):
                folder = os.path.join(accdir, contact)
                if not os.path.isdir(folder):
                    continue
                files = [os.path.join(folder, f) for f in sorted(os.listdir(folder)) if f.endswith((".txt", ".html"))]
                yield proto, service, urllib.parse.unquote(acc).split("/", 1)[0], contact, files


def pidgin_lines(path):
    """The log's lines as text, the HTML variant read the same way."""
    with open(path, encoding="utf-8", errors="replace") as f:
        data = f.read()
    if path.endswith(".html"):
        data = re.sub(r"<title>.*?</title>", "", data, flags=re.DOTALL)
        data = clean(BR.sub("\n", data).replace("</h3>", "\n"))
    return data.split("\n")


def pidgin_file(path, logs, stats):
    """(row_key, ts ms, sender name, text) of each message; a line without a time continues the
    one before (a message of several lines)."""
    rel = os.path.relpath(path, logs)
    m = PIDGIN_FILE.search(os.path.basename(path))
    if not m:
        stats["files without a date"] += 1
        return
    day = datetime(int(m.group(1)), int(m.group(2)), int(m.group(3)), tzinfo=TZ)
    offset = m.group(5)
    out = []
    prev = None
    for line in pidgin_lines(path):
        mm = PIDGIN_LINE.match(line)
        if not mm:
            if out and line.strip() and not line.startswith("Conversation with "):
                out[-1][3] += "\n" + line.replace("\ufeff", "").rstrip()
            continue
        said = PIDGIN_SAID.match(mm.group(5))
        if not said or said.group(1).startswith(PIDGIN_NOTICE) or said.group(1).startswith("http"):
            continue            # a status line: left out
        h, mi, s = clock(int(mm.group(1)), int(mm.group(2)), int(mm.group(3)), mm.group(4))
        dt = with_zone(day.replace(hour=h, minute=mi, second=s), offset)
        if prev and dt < prev - timedelta(hours=1):     # past midnight
            dt += timedelta(days=1)
        prev = dt
        out.append([f"{rel}#{len(out)}", int(dt.timestamp() * 1000), said.group(1).strip(),
                    said.group(2).replace("\ufeff", "").rstrip()])
    yield from out


def pidgin_owner_names(logs, proto, uid, alias):
    """The names the owner's messages carry on this account: the account, its alias, and any name
    that speaks in three or more of the account's one-to-one conversations."""
    names = {uid.lower(), uid.split("@")[0].lower()}
    if alias:
        names.add(alias.lower())
    seen = defaultdict(set)
    accdir = next((os.path.join(logs, proto, d) for d in os.listdir(os.path.join(logs, proto))
                   if urllib.parse.unquote(d).split("/", 1)[0].lower() == uid.lower()), None)
    if not accdir:
        return names
    for contact in os.listdir(accdir):
        folder = os.path.join(accdir, contact)
        if not os.path.isdir(folder) or contact.endswith(".chat"):
            continue
        for f in os.listdir(folder):
            if f.endswith((".txt", ".html")):
                for _, _, sender, _ in pidgin_file(os.path.join(folder, f), logs, defaultdict(int)):
                    seen[sender.lower()].add(contact)
    names |= {s for s, contacts in seen.items() if len(contacts) >= 3}
    return names


# ---------------------------------------------------------------- import

class Stats:
    def __init__(self):
        self.added = defaultdict(int)         # (device, service) -> messages
        self.dupes = defaultdict(int)         # (device, service) -> skipped as already there (fingerprint)
        self.seen = defaultdict(int)          # (device, service) -> origins already in the archive
        self.chats = defaultdict(set)         # (device, service) -> conversation ids
        self.span = {}                        # (device, service) -> (first ts, last ts)
        self.outgoing = defaultdict(int)
        self.images = defaultdict(int)
        self.names = defaultdict(int)         # (device, kind) -> handle names recorded
        self.merged = defaultdict(int)        # device -> people merged into another, by the owner's old groupings
        self.problems = defaultdict(int)      # files/lines the parsers could not read

    def note(self, key, ts, outgoing):
        self.added[key] += 1
        self.outgoing[key] += int(outgoing)
        lo, hi = self.span.get(key, (ts, ts))
        self.span[key] = (min(lo, ts), max(hi, ts))

    def report(self, out=print):
        keys = sorted(set(self.added) | set(self.dupes) | set(self.seen))
        out(f"{'source':18} {'new':>8} {'sent':>7} {'already':>8} {'dupes':>6} {'chats':>6}  period")
        for k in keys:
            lo, hi = self.span.get(k, (None, None))
            period = (f"{datetime.fromtimestamp(lo / 1000, TZ).date()} .. {datetime.fromtimestamp(hi / 1000, TZ).date()}"
                      if lo else "")
            out(f"{k[0] + '/' + k[1]:18} {self.added[k]:8} {self.outgoing[k]:7} {self.seen[k]:8} {self.dupes[k]:6} "
                f"{len(self.chats[k]):6}  {period}")
        out(f"{'total':18} {sum(self.added.values()):8} {sum(self.outgoing.values()):7} {sum(self.seen.values()):8} "
            f"{sum(self.dupes.values()):6} {sum(len(c) for c in self.chats.values()):6}")
        for k, n in sorted(self.images.items()):
            out(f"pictures: {n:6} {k}")
        for k, n in sorted(self.names.items()):
            out(f"names:    {n:6} {k[0]} ({k[1]})")
        for k, n in sorted(self.merged.items()):
            out(f"merged:   {n:6} people into others, as grouped in {k}")
        for k, n in sorted(self.problems.items()):
            out(f"not read: {n} {k}")


class Importer:
    def __init__(self, archive, stats, media=True):
        self.archive, self.stats, self.media = archive, stats, media
        self.sources = {}
        self.aliases = {}       # (handle, service) -> (name, kind, ts): the latest display name seen
        self.store = None

    def source(self, device, service, root):
        k = (device, service)
        if k not in self.sources:
            self.sources[k] = self.archive.source(f"{device}/{service}", root, device, root)
        return self.sources[k]

    def chat(self, service, group, key, peer):
        """The conversation, made when its first message is added: a log with none makes no chat."""
        made = []

        def conv():
            if not made:
                a = self.archive
                if group:
                    made.append(a.conversation(service, [], key=key, title=None))
                    a.db.execute("UPDATE conversation SET is_group = 1 WHERE id = ?", (made[0],))
                else:
                    made.append(a.conversation(service, [peer]))
            return made[0]
        conv.made, conv.key = made, key if group else peer[1]
        return conv

    def account_in(self, service, conv, own):
        """The owner's account the logs were written by is a member of the conversation: which of the
        owner's accounts a chat was on (one with a person may have been on several). Also on a later
        run, when every message is already there."""
        a = self.archive
        cid = conv.made[0] if conv.made else (a.db.execute(
            "SELECT id FROM conversation WHERE service_id = ? AND key = ?", (a.service[service], conv.key)).fetchone() or [None])[0]
        if cid is not None:
            a.db.execute("INSERT OR IGNORE INTO conversation_member VALUES (?, ?)", (cid, a.address(*own)))

    def add(self, device, service, src, chat, rec, sender_handle):
        """One message into the archive, unless its origin or its fingerprint is already there."""
        a, key = self.archive, (device, service)
        if a.has_origin(src, rec.row_key):
            self.stats.seen[key] += 1
            return None
        kind = "image" if rec.images and not rec.text else "text"
        if not rec.text and not rec.images:
            return None
        conv = chat()
        fp = fingerprint(rec.ts, rec.outgoing, kind, rec.text or None)
        if a.db.execute("SELECT 1 FROM message WHERE conversation_id = ? AND fingerprint = ?", (conv, fp)).fetchone():
            self.stats.dupes[key] += 1
            return None
        sender_id = None if rec.outgoing else a.address(*sender_handle)
        mid = a.add_message(src, rec.row_key, service=service, conversation_id=conv, ts=rec.ts, outgoing=rec.outgoing,
                            sender_id=sender_id, kind=kind, text=rec.text or None)
        self.stats.note(key, rec.ts, rec.outgoing)
        self.stats.chats[key].add(conv)
        if rec.alias and not rec.outgoing:
            cur = self.aliases.get((sender_handle, service))
            if not cur or cur[2] < rec.ts:
                self.aliases[(sender_handle, service)] = (rec.alias, "chat", rec.ts)
        return mid

    def picture(self, device, service, src, root, rel, mid):
        self.stats.images[(device, service)] += 1
        if not self.media or mid is None:
            return
        if self.store is None:
            from .media import Store
            self.store = Store(self.archive)
        self.store.link(device, src, os.path.join(root, rel), rel, mid)

    def merge(self, device, groups):
        """Each group of handles the owner had grouped as one person: the people of those handles
        the archive has become one (the lowest id), the owner's own handles left out."""
        a = self.archive
        own = a.own()
        for group in groups:
            handles = [h for h in group if h not in own and a.known(h)]
            people = sorted({a.db.execute("SELECT person_id FROM person_address WHERE address_id = ?",
                                          (a.address(*h),)).fetchone()[0] for h in handles})
            for other in people[1:]:
                a.db.execute("UPDATE person_address SET person_id = ?, how = 'manual' WHERE person_id = ?", (people[0], other))
                labels.moved_person(a.db, people[0], other)
                a.db.execute("DELETE FROM person WHERE id = ?", (other,))
                self.stats.merged[device] += 1

    def finish_names(self, device):
        for (h, service), (name, kind, ts) in self.aliases.items():
            self.archive.handle_name(h, service, name, kind, seen_at=ts // 1000)
            self.stats.names[(device, kind)] += 1
        self.aliases.clear()

    # -- Adium

    def adium(self, root):
        logs = adium_logs(root)
        if not logs:
            print("no Adium folder:", root)
            return
        a, device = self.archive, DEVICES["adium"]
        for service, uid, contact, files in adium_conversations(logs):
            own = handle(service, uid)
            a.account(own, service)
            src = self.source(device, service, logs)
            group = is_group(service, contact)
            peer = None if group else handle(service, contact)
            conv = self.chat(service, group, urllib.parse.unquote(contact).lower(), peer)
            for path in files:
                for rec in adium_file(path, logs, uid, self.stats.problems):
                    sender = handle(service, rec.sender) if group else (own if rec.outgoing else peer)
                    mid = self.add(device, service, src, conv, rec, sender)
                    for rel in rec.images:
                        self.picture(device, service, src, logs, rel, mid)
            self.account_in(service, conv, own)
            a.db.commit()
        self.finish_names(device)
        self.merge(device, adium_metacontacts(logs))
        for k in [k for k in self.sources if k[0] == device]:
            a.imported(self.sources[k])
        a.db.commit()

    # -- Pidgin

    def pidgin(self, root):
        purple, logs = pidgin_root(root)
        if not logs:
            print("no Pidgin folder:", root)
            return
        a, device = self.archive, DEVICES["pidgin"]
        accounts = pidgin_accounts(purple)
        owners = {}
        for proto, service, uid, contact, files in pidgin_conversations(logs):
            own = handle(service, uid)
            a.account(own, service)
            src = self.source(device, service, logs)
            if (proto, uid) not in owners:
                owners[(proto, uid)] = pidgin_owner_names(logs, proto, uid, accounts.get((proto, uid.lower())))
            names = owners[(proto, uid)]
            group = is_group(service, contact)
            peer = None if group else handle(service, contact)
            conv = self.chat(service, group, urllib.parse.unquote(contact).lower().removesuffix(".chat"), peer)
            for path in files:
                for row_key, ts, sender, text in pidgin_file(path, logs, self.stats.problems):
                    s = sender.lower()
                    outgoing = s in names or s.startswith(uid.lower() + "/")
                    if group:
                        who = own if outgoing else ("name", sender, service)
                    else:
                        who = own if outgoing else peer
                    rec = Record(row_key, ts, outgoing, sender, text)
                    self.add(device, service, src, conv, rec, who)
            self.account_in(service, conv, own)
            a.db.commit()
        for service, name, alias in pidgin_buddies(purple):
            h = handle(service, name)
            if a.known(h):
                a.handle_name(h, service, alias, "book")
                self.stats.names[(device, "book")] += 1
        self.merge(device, pidgin_contacts(purple))
        for k in [k for k in self.sources if k[0] == device]:
            a.imported(self.sources[k])
        a.db.commit()


def run(archive, adium=None, pidgin=None, media=True, out=print):
    """Import what is given: an Adium folder (Adium 2.0, Users/Default or Logs) and a .purple
    folder (or its logs). Adium first: its files are the fuller record."""
    adium = adium or config.get("imlogs", "adium")
    pidgin = pidgin or config.get("imlogs", "pidgin")
    if not adium and not pidgin:
        out("no source: neither [imlogs] adium nor [imlogs] pidgin")
        return None
    stats = Stats()
    imp = Importer(archive, stats, media=media)
    if adium:
        imp.adium(os.path.expanduser(adium))
    if pidgin:
        imp.pidgin(os.path.expanduser(pidgin))
    archive.resolve()
    archive.db.commit()
    stats.report(out)
    return stats
