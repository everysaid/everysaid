"""Missed-call notices of Greek carriers, sent as SMS when a call came while the phone was off or busy:
"ΕΙΧΑΤΕ 2 ΚΛΗΣΕΙΣ: ...", "ΚΛΗΣΕΙΣ: ...", "ΔΩΡΕΑΝ ΕΝΗΜΕΡΩΣΗ: ΕΙΧΑΤΕ 1 ΚΛΗΣΗ ΑΠΟ ΤΟ ...", with each
caller's number, the time (Greek time) and how many times they called.
"""
from datetime import datetime
from zoneinfo import ZoneInfo
import re

SOURCE = "sms-alerts"           # the archive's source name for the calls found here
TZ = ZoneInfo("Europe/Athens")
# The carrier writes Greek in capitals, often with Latin look-alikes (KΛHΣEIΣ): compare in Latin.
LOOKALIKE = str.maketrans("ΑΒΕΖΗΙΚΜΝΟΡΤΥΧ", "ABEZHIKMNOPTYX")
ALERT = re.compile(r"^\s*(?:ΔΩPEAN ENHMEPΩΣH:\s*)?(?:EIXATE \d+ KΛHΣ|KΛHΣEIΣ:)")
NUMBER = re.compile(r"\+?\d[\d ]{6,}\d")
WHEN = re.compile(r"(?P<d>\d{1,2})[/-](?P<m>\d{1,2})(?:[/-](?P<y>\d{2,4}))?\s*(?:,\s*|\s+)(?P<H>\d{1,2}):(?P<M>\d{2})"
                  r"|(?P<H2>\d{1,2}):(?P<M2>\d{2}),(?P<d2>\d{1,2})/(?P<m2>\d{1,2})/(?P<y2>\d{2})")
COUNT = re.compile(r"\((\d+)\)|(\d+)\s*ΦOPEΣ")


def alerts(text, sent):
    """(number, when as datetime in Greek time, attempts, busy) for each call in a notice; sent:
    when the notice came, an aware datetime."""
    t = text.upper().translate(LOOKALIKE)
    sent = sent.astimezone(TZ)
    if not ALERT.match(t):
        return []
    busy = "KATEIΛHMMENOΣ" in t
    total = re.search(r"EIXATE (\d+) KΛHΣ", t)
    found = []
    for num in NUMBER.finditer(t):
        when = WHEN.search(t, num.end())
        if not when:
            break
        nxt = NUMBER.search(t, num.end())
        if nxt and nxt.start() < when.start():
            continue                    # another number before the time: this one is not a caller
        if found and found[-1][1] == when.start():
            continue
        found.append((num, when.start(), when))
    out = []
    for num, _, when in found:
        g = when.groupdict()
        day, month = int(g["d"] or g["d2"]), int(g["m"] or g["m2"])
        hour, minute = int(g["H"] or g["H2"]), int(g["M"] or g["M2"])
        year = g["y"] or g["y2"]
        year = (2000 + int(year) % 100) if year else (sent.year - (month > sent.month))
        try:
            dt = datetime(year, month, day, hour, minute, tzinfo=TZ)
        except ValueError:
            continue
        near = COUNT.search(t[num.end():when.end() + 12])
        attempts = (int(near.group(1) or near.group(2)) if near and (near.group(1) or near.group(2))
                    else int(total.group(1)) if total and len(found) == 1 else 1)
        out.append((num.group().replace(" ", ""), dt, attempts, busy))
    return out
