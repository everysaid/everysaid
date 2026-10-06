#!/usr/bin/env python3
"""Back up the iPhone over the cable, then decrypt SMS, call history, Viber and WhatsApp into a folder.

    uv run --extra iphone python scripts/iphone-sync.py [-o DIR] [--no-backup] [--full]
    uv run --extra iphone python scripts/iphone-sync.py --save-password | --move-to-keyring

The password (secret `backup-password`) is read from the system's keyring, else from its file in the
config folder (mode 600); --save-password asks for it once, checks it and stores it in the keyring
(the file where there is none), --move-to-keyring moves an existing file into the keyring. Without
it, it is asked first (hidden), so the rest runs unattended. It is never printed.
"""
import argparse
import getpass
import os
import subprocess
import sys

from iphone_backup_decrypt import EncryptedBackup, RelativePath
from iphone_backup_decrypt.exceptions import IncorrectPassphraseError

import common
from common import config

BACKUP_ROOT = config.IPHONE_BACKUP_ROOT
OUT = os.path.join(config.CACHE, "iphone")   # made again from the backup: cache
SECRET = "backup-password"

FILES = {   # output name: (relative path, domain)
    "sms.db": (RelativePath.TEXT_MESSAGES, None),
    "CallHistory.storedata": (RelativePath.CALL_HISTORY, None),
    "viber.sqlite": ("com.viber/database/Contacts.data", "AppDomainGroup-group.viber.share.container"),
    "whatsapp.sqlite": ("ChatStorage.sqlite", "AppDomainGroup-group.net.whatsapp.WhatsApp.shared"),
    "whatsapp-contacts.sqlite": ("ContactsV2.sqlite", "AppDomainGroup-group.net.whatsapp.WhatsApp.shared"),
    # WhatsApp's call log, kept apart from its chats (Viber's calls are in viber.sqlite, ZRECENT)
    "whatsapp-calls.sqlite": ("CallHistory.sqlite", "AppDomainGroup-group.net.whatsapp.WhatsApp.shared"),
}
# Media, copied incrementally: each file has a unique name and never changes once written; files the
# archive has already taken are not copied again (see archived_media).
# output folder: (domain, path prefixes, prefix stripped from the output path)
MEDIA = {
    "viber-media": ("AppDomain-com.viber",
                    ("Documents/Attachments/", "Documents/FileMessages/", "Documents/VoiceMessages/"), "Documents/"),
    "whatsapp-media": ("AppDomainGroup-group.net.whatsapp.WhatsApp.shared", ("Message/Media/",), "Message/Media/"),
}

ARCHIVE_DB = os.path.join(config.DATA, "archive.db")
ARCHIVE_SOURCES = {"whatsapp-media": "iphone/whatsapp", "viber-media": "iphone/viber"}


def message_files(out):
    """media folder -> the file paths that belong to a message (what the archive links), read from the
    databases just extracted: thumbnails, favicons and link previews beside them are not copied."""
    wa = config.read_only(os.path.join(out, 'whatsapp.sqlite'))
    vb = config.read_only(os.path.join(out, 'viber.sqlite'))
    names = [n for (n,) in vb.execute("SELECT ZNAME FROM ZATTACHMENT WHERE ZNAME IS NOT NULL")]
    return {"whatsapp-media": {p.removeprefix("Media/") for (p,) in wa.execute(
                "SELECT ZMEDIALOCALPATH FROM ZWAMEDIAITEM WHERE ZMEDIALOCALPATH IS NOT NULL")},
            "viber-media": {f"{folder}/{n}" for folder in ("Attachments", "FileMessages", "VoiceMessages")
                            for n in names}}


def archived_media():
    """media folder -> set of the file paths the archive has already taken (read only)."""
    if not os.path.exists(ARCHIVE_DB):
        return {}
    db = config.read_only(ARCHIVE_DB)
    known = {}
    for folder, source in ARCHIVE_SOURCES.items():
        known[folder] = {p for (p,) in db.execute(
            "SELECT a.source_path FROM attachment a JOIN source s ON s.id = a.source_id WHERE s.name = ?", (source,))}
    return known


ap = argparse.ArgumentParser(description="Back up the iPhone and decrypt its databases.")
ap.add_argument("-o", "--out", default=OUT, help=f"folder for the decrypted files (default {OUT})")
ap.add_argument("--no-backup", action="store_true", help="only decrypt the existing backup")
ap.add_argument("--backup-root", help="the folder the backups are in (default: [iphone] backup_root)")
ap.add_argument("--udid", help="which iPhone (default: [iphone] udid, else the only one)")
ap.add_argument("--password-stdin", action="store_true", help="read the backup password from the standard input")
ap.add_argument("--full", action="store_true", help="force a full backup instead of an incremental one")
ap.add_argument("--only", nargs="+", metavar="NAME", help="decrypt only these databases (e.g. whatsapp-calls.sqlite), no media")
ap.add_argument("--save-password", action="store_true",
                help="ask for the password, check it and store it in the keyring (else a file, mode 600)")
ap.add_argument("--move-to-keyring", action="store_true",
                help=f"move the password from {config.secret_file(SECRET)} into the keyring")
args = ap.parse_args()
os.umask(0o077)   # everything written here is private: folders 700, files 600
if args.move_to_keyring:
    config.move_to_keyring(SECRET)
    sys.exit()
BACKUP_ROOT = args.backup_root or BACKUP_ROOT
UDID = args.udid or config.iphone_udid(BACKUP_ROOT)
BACKUP = os.path.join(BACKUP_ROOT, UDID)


def open_backup(pw):
    backup = EncryptedBackup(backup_directory=BACKUP, passphrase=pw)
    backup.test_decryption()
    return backup


have_backup = os.path.exists(os.path.join(BACKUP, "Manifest.plist"))
if (args.no_backup or args.save_password) and not have_backup:
    sys.exit("There is no complete backup yet (its Manifest.plist is missing).")

if args.password_stdin:            # given for this run only (the app asks for it): never stored
    pw = sys.stdin.readline().rstrip("\r\n")
    if have_backup:
        try:
            open_backup(pw)
        except IncorrectPassphraseError:
            sys.exit("Wrong backup password.")
else:
    pw = None if args.save_password else config.secret(SECRET)
if pw is not None and have_backup and not args.password_stdin:
    try:
        open_backup(pw)
    except IncorrectPassphraseError:
        sys.exit("The stored backup password is wrong: store it again with --save-password.")

while pw is None:
    pw = getpass.getpass("Backup password (empty to quit): ")
    if not pw:
        sys.exit(1)
    if not have_backup:     # nothing to check it against yet; the backup itself will tell
        break
    try:
        open_backup(pw)
    except IncorrectPassphraseError:
        print("Wrong password.")
        pw = None
        continue
    except Exception as e:
        print("Another error (not a wrong password):", type(e).__name__, e)
        pw = None
        continue

if args.save_password:
    sys.exit(f"Stored in the {config.save_secret(SECRET, pw)}.")

def terminal():
    """A pseudo-terminal (reader, writer) where the system has them, its lines as written (no \r
    added before \n); None elsewhere."""
    try:
        import pty
        import termios
        reader, writer = pty.openpty()
    except (ImportError, OSError):
        return None
    mode = termios.tcgetattr(writer)
    mode[1] &= ~termios.OPOST
    termios.tcsetattr(writer, termios.TCSANOW, mode)
    return reader, writer


def read(fd):
    try:
        return os.read(fd, 4096)
    except OSError:                     # a terminal whose writer has closed
        return b""


if not args.no_backup:
    cmd = ["idevicebackup2", "-u", UDID, "backup"] + (["--full"] if args.full else []) + [BACKUP_ROOT]
    print("Backup:", " ".join(cmd), flush=True)
    print("The iPhone may ask for its passcode: type it there.", flush=True)
    os.makedirs(BACKUP_ROOT, exist_ok=True)
    # its output as it comes (a backup takes minutes), and kept to say why if it fails
    # (bytes as they come, its \r of a progress bar kept: the reader draws them as a terminal does).
    # Into a pipe it would hold its lines back for long, so it gets a terminal where there is one.
    out = terminal()
    proc = subprocess.Popen([common.tool(cmd[0]), *cmd[1:]], stdin=subprocess.DEVNULL,
                            stdout=out[1] if out else subprocess.PIPE, stderr=subprocess.STDOUT)
    if out:
        os.close(out[1])
    said = b""
    while chunk := read(out[0] if out else proc.stdout.fileno()):
        sys.stdout.buffer.write(chunk)
        sys.stdout.flush()
        said = (said + chunk)[-20000:]
    said = said.decode("utf-8", "replace")
    if proc.wait() != 0:
        # said in the user's words by the app (plugins/i18n.py), so each one whole
        sys.exit("The backup failed: the iPhone is locked. Unlock it, and type its passcode there when it asks."
                 if "Device locked" in said or "ErrorCode 208" in said
                 else "The backup failed: no iPhone found. Connect it with a cable, unlock it and tap Trust."
                 if "No device found" in said or "ERROR: Could not connect" in said
                 else "The backup failed: idevicebackup2 did not finish (see the whole log).")

backup = open_backup(pw)
os.makedirs(args.out, mode=0o700, exist_ok=True)
for name, (rel, domain) in FILES.items():
    if args.only and name not in args.only:
        continue
    dest = os.path.join(args.out, name)
    tmp = dest + ".part"
    backup.extract_file(relative_path=rel, domain_like=domain, output_filename=tmp)
    os.chmod(tmp, 0o600)
    os.replace(tmp, dest)
    for side in ("-wal", "-shm"):   # left by readers of the previous copy; they don't belong to this one
        if os.path.exists(dest + side):
            os.remove(dest + side)
    print("OK", name)

if args.only:
    sys.exit()

# Media are copied when they belong to a message, and unless the archive already has them: it keeps a
# record of every file it took (`attachment.source_path`) even after the file itself has gone to the
# photo library or been removed, so those are not brought back.
known = archived_media()
wanted = message_files(args.out)
for folder, (domain, prefixes, strip) in MEDIA.items():
    media = os.path.join(args.out, folder)
    os.makedirs(media, mode=0o700, exist_ok=True)   # makedirs gives the mode only to the last level
    with backup.manifest_db_cursor() as cur:
        cur.execute("SELECT relativePath FROM Files WHERE flags = 1 AND domain = ?", (domain,))
        paths = [p for (p,) in cur if p.startswith(prefixes)]
    new = skipped = 0
    for rel in paths:
        out = rel.removeprefix(strip)
        dest = os.path.join(media, *out.split("/"))
        if os.path.exists(dest) or out not in wanted[folder]:
            continue
        if out in known.get(folder, ()):
            skipped += 1
            continue
        os.makedirs(os.path.dirname(dest), mode=0o700, exist_ok=True)
        backup.extract_file(relative_path=rel, domain_like=domain, output_filename=dest + ".part")
        os.chmod(dest + ".part", 0o600)
        os.replace(dest + ".part", dest)
        new += 1
    print(f"OK {folder}: {new} new, {skipped} already in the archive, {len(paths)} in all")
print("Done:", args.out)
