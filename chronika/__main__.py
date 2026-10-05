"""Fill the archive from the extracted phone data.

    uv run python -m chronika [--db PATH] [IMPORTER...]

Importers, in the order they run (each needs the ones before it): sms, calls, viber, whatsapp,
telegram, voip (WhatsApp/Viber calls, carrier notices), media. Without names: the ones config
`[import] importers` lists, else all. Every source is optional: an importer whose sources are not there says
so and does nothing. Each can be run again: rows already in the archive are skipped.
"""
import argparse

from . import calls, config, media, sms, telegram, viber, voip, whatsapp
from .archive import DB, Archive

# name -> (what it brings, its function); the order is the order they run in: `media` finds the
# messages by key and origin, `voip` reads the SMS and the WhatsApp chats, and comes after `calls`.
IMPORTERS = {
    "sms": ("SMS, MMS, iMessage, RCS", sms.run),
    "calls": ("the phones' call logs", calls.run),
    "viber": ("Viber messages", viber.run),
    "whatsapp": ("WhatsApp messages", whatsapp.run),
    "telegram": ("Telegram messages and calls", telegram.run),
    "voip": ("WhatsApp and Viber calls, carrier notices", voip.run),
    "media": ("the files of messages", media.run),
}
CONFIGURED = getattr(config, "IMPORTERS", None) or config.get("import", "importers")

ap = argparse.ArgumentParser(description="Fill the archive from the extracted phone data.")
ap.add_argument("--db", default=DB, help=f"archive database (default {DB})")
ap.add_argument("importers", nargs="*", help=f"{', '.join(IMPORTERS)}; default: config [import] importers, else all")
args = ap.parse_args()
chosen = args.importers or CONFIGURED or list(IMPORTERS)
unknown = [n for n in chosen if n not in IMPORTERS]
if unknown:
    ap.error(f"άγνωστα: {', '.join(unknown)} (υπάρχουν: {', '.join(IMPORTERS)})")

archive = Archive(args.db)
for name in sorted(chosen, key=list(IMPORTERS).index):
    print(f"== {name}: {IMPORTERS[name][0]}")
    IMPORTERS[name][1](archive)
