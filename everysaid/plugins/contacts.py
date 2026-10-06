"""Contacts plugins: an address book whose names and photos name the people of the archive.

A contact joins the people whose addresses (phone numbers, emails) it lists; it creates no one:
a number never seen in a message or call stays out. Photos are kept in the cache
(`avatars/<sha256>.<ext>`). Two plugins: a CardDAV address book (Nextcloud, iCloud, Fastmail,
Radicale, ...) and a .vcf file.
"""
import base64
import hashlib
import os
import re
import time
import xml.etree.ElementTree as ET

from .. import config
from ..archive import address as normalise
from .base import Plugin, Setting

AVATARS = os.path.join(config.CACHE, "avatars")


def parse_vcards(data):
    """[{uid, name, org, phones: [(value, label)], emails: [...], photo: (bytes, ext) or None}]."""
    text = re.sub(r"\r?\n[ \t]", "", data.replace("\r\n", "\n"))     # unfold
    cards, card = [], None
    for line in text.split("\n"):
        if not line.strip():
            continue
        if line.upper().startswith("BEGIN:VCARD"):
            card = {"uid": None, "name": None, "n": None, "org": None, "phones": [], "emails": [], "photo": None}
            continue
        if line.upper().startswith("END:VCARD"):
            if card:
                if not card["name"] and card["n"]:
                    parts = [p for p in card["n"].split(";") if p]
                    card["name"] = " ".join(reversed(parts[:2])) if parts else None
                card["uid"] = card["uid"] or hashlib.sha1((card["name"] or "") .encode() + repr(card["phones"]).encode()).hexdigest()
                cards.append(card)
            card = None
            continue
        if card is None or ":" not in line:
            continue
        head, value = line.split(":", 1)
        name, *params = head.split(";")
        name = name.split(".")[-1].upper()          # item1.TEL -> TEL
        p = ";".join(params).upper()
        value = value.replace("\\,", ",").replace("\\;", ";").replace("\\n", "\n")
        label = (re.search(r"TYPE=([A-Z,]+)", p) or [None, None])[1]
        if name == "FN":
            card["name"] = value.strip() or None
        elif name == "N":
            card["n"] = value
        elif name == "UID":
            card["uid"] = value.strip()
        elif name == "ORG":
            card["org"] = value.split(";")[0].strip() or None
        elif name == "TEL":
            card["phones"].append((value.removeprefix("tel:").strip(), (label or "").lower() or None))
        elif name == "EMAIL":
            card["emails"].append((value.strip(), (label or "").lower() or None))
        elif name == "PHOTO":
            try:
                if value.startswith("data:"):
                    meta, b64 = value[5:].split(",", 1)
                    ext = meta.split(";")[0].split("/")[-1]
                    card["photo"] = (base64.b64decode(b64), ext)
                elif "ENCODING=B" in p or "ENCODING=BASE64" in p:
                    ext = (re.search(r"TYPE=([A-Z]+)", p) or [None, "jpeg"])[1].lower()
                    card["photo"] = (base64.b64decode(value), ext)
            except (ValueError, base64.binascii.Error):
                pass
    return cards


def save_cards(ctx, cards):
    """The contacts of this instance replaced by `cards`; their addresses joined where the archive
    has them. Returns (contacts, linked addresses)."""
    os.makedirs(AVATARS, exist_ok=True)
    now = int(time.time())
    linked = 0
    with ctx.store.write() as db:
        kinds = dict(db.execute("SELECT name, id FROM address_kind"))
        keep = set()
        for c in cards:
            photo = None
            if c["photo"]:
                data, ext = c["photo"]
                ext = {"jpeg": "jpg"}.get(ext, ext)
                photo = f"{hashlib.sha256(data).hexdigest()}.{ext}"
                path = os.path.join(AVATARS, photo)
                if not os.path.exists(path):
                    with open(path, "wb") as f:
                        f.write(data)
            cid = db.execute(
                "INSERT INTO contact (instance_id, uid, url, name, organization, photo, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?) "
                "ON CONFLICT (instance_id, uid) DO UPDATE SET url = excluded.url, name = excluded.name, "
                "organization = excluded.organization, photo = excluded.photo, updated_at = excluded.updated_at RETURNING id",
                (ctx.id, c["uid"], c.get("url"), c["name"], c["org"], photo, now)).fetchone()[0]
            keep.add(cid)
            db.execute("DELETE FROM contact_address WHERE contact_id = ?", (cid,))
            for raw, label in c["phones"] + c["emails"]:
                kind, value = normalise(raw)
                row = db.execute("SELECT id FROM address WHERE kind_id = ? AND value = ? AND service_id IS NULL",
                                 (kinds.get(kind), value)).fetchone()
                if row:
                    db.execute("INSERT OR IGNORE INTO contact_address VALUES (?, ?, ?)", (cid, row[0], label))
                    linked += 1
        gone = [r[0] for r in db.execute("SELECT id FROM contact WHERE instance_id = ?", (ctx.id,)) if r[0] not in keep]
        for cid in gone:
            db.execute("DELETE FROM contact_address WHERE contact_id = ?", (cid,))
            db.execute("DELETE FROM contact WHERE id = ?", (cid,))
    return len(cards), linked


class VcardFile(Plugin):
    id = "vcard-file"
    name = "Contacts file (.vcf)"
    kind = "contacts"
    name_weights = {"contacts": 100}     # the user's own address book
    description = "A vCard file exported from any address book (phone, Google, Outlook, ...)."
    settings = (Setting("path", ".vcf file", "path", required=True),)

    def sync(self, ctx):
        with open(os.path.expanduser(ctx.settings["path"]), encoding="utf-8", errors="replace") as f:
            cards = parse_vcards(f.read())
        n, linked = save_cards(ctx, cards)
        ctx.log("contacts: {n}, addresses found in the archive: {linked}", n=n, linked=linked)
        return n

    run_import = sync


class CardDav(Plugin):
    id = "carddav"
    name = "Address book (CardDAV)"
    kind = "contacts"
    name_weights = {"contacts": 100}     # the user's own address book
    description = "A CardDAV address book: Nextcloud, iCloud, Fastmail, Radicale and others."
    settings = (
        Setting("url", "Address book URL", "url", required=True,
                help="e.g. https://cloud.example.org/remote.php/dav/addressbooks/users/NAME/contacts/"),
        Setting("username", "User", "text", required=True),
        Setting("password", "Password (an app password)", "secret", required=True),
    )

    def sync(self, ctx):
        import httpx
        body = ('<?xml version="1.0" encoding="utf-8"?><c:addressbook-query xmlns:d="DAV:" '
                'xmlns:c="urn:ietf:params:xml:ns:carddav"><d:prop><d:getetag/><c:address-data/></d:prop>'
                '</c:addressbook-query>')
        r = httpx.request("REPORT", ctx.settings["url"], content=body.encode(),
                          headers={"Depth": "1", "Content-Type": "application/xml; charset=utf-8"},
                          auth=(ctx.settings["username"], ctx.secret("password") or ""), timeout=120)
        r.raise_for_status()
        ns = {"d": "DAV:", "c": "urn:ietf:params:xml:ns:carddav"}
        cards = []
        for resp in ET.fromstring(r.content).findall("d:response", ns):
            href = resp.findtext("d:href", namespaces=ns)
            data = resp.findtext(".//c:address-data", namespaces=ns)
            for card in parse_vcards(data or ""):
                card["url"] = href
                cards.append(card)
        n, linked = save_cards(ctx, cards)
        ctx.log("contacts: {n}, addresses found in the archive: {linked}", n=n, linked=linked)
        return n

    run_import = sync


PLUGINS = (CardDav, VcardFile)
