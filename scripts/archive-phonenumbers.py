#!/usr/bin/env python3
"""What normalising numbers with `phonenumbers` (archive.address(), schema v2) would change in an
archive: a dry run, nothing is written to the archive.

    uv run python scripts/archive-phonenumbers.py DB [OUT]

DB is opened read only. OUT (default `<cache>/phonenumbers-dry-run.tsv`) gets one line per change:

- rename: a stored number whose normalised form differs, and no other address has that form;
- merge: two or more stored numbers that would become one address (all of them listed);
- import: a number as a source writes it (the iPhone's databases in the cache, `viber_member`)
  that the old rule (the +30 heuristic) and the new one normalise differently, so that a new
  import would no longer meet the address the archive has.

Each line: category, old value, new value, where (for import lines: the source), and how much
refers to the address (messages sent, calls, conversations, reactions). Applying any of it is the
owner's decision.
"""
import argparse
from collections import defaultdict
import os
import re
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
from chronika import archive, config  # noqa: E402


def v1_address(raw):
    """archive.address() of schema v1: Greek numbers by length and first digit."""
    s = (raw or "").strip()
    if "@" in s:
        return "email", s.lower()
    digits = re.sub(r"[\s\-().]", "", s)
    if not re.fullmatch(r"\+?\d+", digits):
        return "alpha", s
    if digits.startswith("00"):
        digits = "+" + digits[2:]
    bare = digits.lstrip("+")
    if len(bare) == 10 and bare[0] in "26":
        return "phone", "+30" + bare
    if len(bare) == 12 and bare.startswith("30"):
        return "phone", "+" + bare
    if len(bare) < 10:
        return "phone", bare
    return "phone", digits if digits.startswith("+") else "+" + digits


def ro(path):
    return config.read_only(path) if os.path.exists(path) else None


def raw_numbers(db):
    """(source, number as written) from what is on disk."""
    data = archive.IPHONE_DATA
    queries = [("iphone/sms", f"{data}/sms.db", "SELECT id FROM handle"),
               ("iphone/calls", f"{data}/CallHistory.storedata", "SELECT ZADDRESS FROM ZCALLRECORD"),
               ("iphone/calls", f"{data}/CallHistory.storedata", "SELECT ZVALUE FROM ZHANDLE"),
               ("iphone/viber", f"{data}/viber.sqlite", "SELECT coalesce(ZCANONIZEDPHONENUM, ZPHONE) FROM ZPHONENUMBER"),
               ("iphone/viber-calls", f"{data}/viber.sqlite", "SELECT ZPHONENUMBER FROM ZRECENTSLINE"),
               ("iphone/whatsapp", f"{data}/whatsapp.sqlite", "SELECT ZCONTACTJID FROM ZWACHATSESSION "
                "WHERE ZCONTACTJID LIKE '%@s.whatsapp.net' UNION SELECT ZMEMBERJID FROM ZWAGROUPMEMBER "
                "WHERE ZMEMBERJID LIKE '%@s.whatsapp.net'")]
    for source, path, sql in queries:
        src = ro(path)
        for (value,) in src.execute(sql) if src else ():
            if value:
                yield source, str(value).split("@s.whatsapp.net")[0]
    for (number,) in db.execute("SELECT number FROM viber_member"):
        yield "viber_member", number


ap = argparse.ArgumentParser(description="Dry run of phonenumbers normalisation on an archive.")
ap.add_argument("db")
ap.add_argument("out", nargs="?", default=os.path.join(config.CACHE, "phonenumbers-dry-run.tsv"))
args = ap.parse_args()
db = config.read_only(args.db)
print(f"περιοχή: {config.REGION or '(καμία)'}")

uses = defaultdict(lambda: [0, 0, 0, 0])
for i, sql in enumerate(("SELECT sender_id, count(*) FROM message WHERE sender_id IS NOT NULL GROUP BY 1",
                         "SELECT address_id, count(*) FROM call WHERE address_id IS NOT NULL GROUP BY 1",
                         "SELECT address_id, count(*) FROM conversation_member GROUP BY 1",
                         "SELECT address_id, count(*) FROM reaction WHERE address_id IS NOT NULL GROUP BY 1")):
    for aid, n in db.execute(sql):
        uses[aid][i] = n
phone = db.execute("SELECT id FROM address_kind WHERE name = 'phone'").fetchone()[0]
stored = dict(db.execute("SELECT id, value FROM address WHERE kind_id = ?", (phone,)).fetchall())

target = defaultdict(list)
for aid, value in stored.items():
    target[archive.address(value)].append(aid)
rows, counts = [], defaultdict(int)
for (kind, new), aids in sorted(target.items()):
    olds = [stored[a] for a in aids]
    if len(aids) > 1:
        cat = "merge"
    elif olds[0] != new or kind != "phone":
        cat = "rename"
    else:
        continue
    for a in aids:
        counts[cat] += 1
        rows.append((cat, stored[a], f"{kind}:{new}" if kind != "phone" else new, "archive",
                     "/".join(map(str, uses[a]))))

values = set(stored.values())
seen, checked = set(), 0
for source, raw in raw_numbers(db):
    checked += 1
    old, new = v1_address(raw), archive.address(raw)
    if old[0] != "phone" and new[0] != "phone" or old[1] == new[1] or (source, raw) in seen:
        continue
    seen.add((source, raw))
    counts["import"] += 1
    counts["import, the archive has the old form"] += old[1] in values
    rows.append(("import", f"{old[0]}:{old[1]}", f"{new[0]}:{new[1]}", source,
                 "in archive" if old[1] in values else "-"))

os.umask(0o077)
with open(args.out, "w", encoding="utf-8") as f:
    f.write("category\told\tnew\twhere\tuses (sent/calls/conversations/reactions)\n")
    for r in rows:
        f.write("\t".join(r) + "\n")
print(f"αριθμοί στο archive: {len(stored)}, όπως τους γράφουν οι πηγές: {checked}")
for cat in ("rename", "merge", "import", "import, the archive has the old form"):
    print(f"{cat:40} {counts[cat]}")
print(f"γράφτηκε: {args.out}")
