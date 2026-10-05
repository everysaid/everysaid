"""The importers, in the order they run (each needs the ones before it): sms, calls, viber,
whatsapp, telegram, voip (WhatsApp/Viber calls, carrier notices), media. Every source is optional:
an importer whose sources are not there says so and does nothing. Each can be run again: rows
already in the archive are skipped.
"""
from . import calls, config, media, sms, telegram, viber, voip, whatsapp

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


def chosen(names=None):
    """The importers to run: those named, else config `[import] importers`, else all."""
    return list(names or config.get("import", "importers") or IMPORTERS)


def run(archive, names, out=print):
    for name in sorted(names, key=list(IMPORTERS).index):
        out(f"== {name}: {IMPORTERS[name][0]}")
        IMPORTERS[name][1](archive)
