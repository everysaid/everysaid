"""Where Chronika keeps its files, and the settings of `config.toml` in the config folder.

The folders follow the platform's conventions (platformdirs): on Linux the XDG ones,
`~/.local/share/chronika` for what cannot be made again (the archive, the review decisions),
`~/.cache/chronika` for what can, `~/.config/chronika` for the settings and the secrets. The
environment variables CHRONIKA_DATA, CHRONIKA_CACHE and CHRONIKA_CONFIG move them (a demo or a test
archive is kept wholly apart this way). Every setting has a general default, so the file is needed
only to change one:

    [owner]
    numbers = ["+15551234567"]      # the owner's own numbers, in international form
    region = "US"                   # for numbers written without a country code
    timezone = "America/New_York"   # default: the system's

    [iphone]
    udid = "..."                    # default: the only backup in backup_root, else the only phone on the cable
    backup_root = "..."             # default: <data>/iphone-backup

    [android]
    export = "..."                  # android-export.py's folder; default: <data>/android

    [viber]
    desktop_export = "..."          # a decrypted Viber Desktop database (viber-desktop-export.cpp)

    [whatsapp]
    bridge = "..."                  # the WhatsApp bridge's store folder (messages.db, whatsapp.db)

    [telegram]
    media = true                    # false: telegram-sync.py --media downloads nothing
    no_media = [-100123]            # chats (ids, as its --survey gives them) whose media are not wanted

    [immich]
    url = "http://host:2283"
    data_folder = "..."             # immich's upload folder, read directly (backups, thumbs)
    container_prefix = "/usr/src/app/upload/"   # the same folder as immich's paths name it
    make = "Chronika"               # the camera make written into uploaded files

    [ollama]
    url = "http://localhost:11434"  # media-vlm.py's local vision model
    model = "qwen2.5vl:7b"          # that model (also the one the review pages show first)

    [media]
    store = "..."                   # the archive's own media files; default: <data> (media/<ab>/...)
    aside = "..."                   # media-aside.py's folder; default: <data>/aside

    [server]
    origin = "https://chronika.example.org"   # the address the app is reached at; default: http://localhost:8520
    host = "127.0.0.1"              # where `chronika serve` listens; behind a reverse proxy keep it local
    port = 8520

    [review]
    person = "..."                  # the one person most pictures come from: a filter of their own
    me = "εγώ"                      # the owner's face label (media-faces.py)
    dog_tag = "..."                 # media-tags.py's label for a dog, and its threshold
    dog_threshold = 0.0005
    reference_model = "claude-sonnet"   # vlm.db's reference answers, for vlm-review.py's comparison
"""
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo
import os
import sqlite3
import subprocess
import sys
import tomllib

import platformdirs
import tzlocal

APP = "chronika"
# The keyring's service name for secrets; a demo or a test sets its own, so that it never sees the
# user's real secrets (and never connects to their accounts).
KEYRING = os.environ.get("CHRONIKA_KEYRING") or APP

DATA = os.environ.get("CHRONIKA_DATA") or platformdirs.user_data_dir(APP, appauthor=False)
CACHE = os.environ.get("CHRONIKA_CACHE") or platformdirs.user_cache_dir(APP, appauthor=False)
CONFIG = os.environ.get("CHRONIKA_CONFIG") or platformdirs.user_config_dir(APP, appauthor=False)

CONFIG_FILE = os.path.join(CONFIG, "config.toml")


def read_only(path):
    """A SQLite database opened read only. The URI is built from the path (any characters, `#`, `?`
    and `%` included, and Windows drive letters), so it always names that file."""
    return sqlite3.connect(Path(path).resolve().as_uri() + "?mode=ro", uri=True)


def _load():
    if not os.path.exists(CONFIG_FILE):
        return {}
    with open(CONFIG_FILE, "rb") as f:
        return tomllib.load(f)


_settings = _load()


def get(section, key, default=None):
    return _settings.get(section, {}).get(key, default)


def _path(section, key, default=None):
    value = get(section, key)
    return os.path.expanduser(value) if value else default


def _system_zone():
    """The system's zone names, most specific first: TZ (also as a path, TZ=:/etc/localtime), the
    zone /etc/localtime links to, /etc/timezone (Debian), then tzlocal's answer (Windows, where
    none of those exist, and any system they miss)."""
    names = []
    for n in (os.environ.get("TZ", "").lstrip(":"), "/etc/localtime"):
        if os.path.isabs(n):            # a zoneinfo file: its name is the part after .../zoneinfo/
            n = os.path.realpath(n).partition("/zoneinfo/")[2]
        names.append(n)
    try:
        with open("/etc/timezone", encoding="utf-8") as f:
            names.append(f.read().strip())
    except OSError:
        pass
    try:
        names.append(tzlocal.get_localzone_name())
    except Exception:                   # tzlocal raises its own errors on odd systems
        pass
    return [n for n in names if n]


def _zone(name):
    """(name, tzinfo): the one set, else the system's, else the current offset. Never stops: a name
    zoneinfo does not know is passed over (a wrong one in the config is said on stderr)."""
    for n in ([name] if name else []) + _system_zone():
        try:
            return n, ZoneInfo(n)
        except (ValueError, OSError, KeyError):     # ZoneInfoNotFoundError is a KeyError
            pass
        if n == name:
            print(f"Άγνωστη ζώνη ώρας [owner] timezone = {name!r} στο {CONFIG_FILE}· "
                  f"χρησιμοποιείται του συστήματος.", file=sys.stderr)
    return None, datetime.now().astimezone().tzinfo     # Windows: no zone name, only today's offset


OWN_NUMBERS = list(get("owner", "numbers", []))
REGION = get("owner", "region")
TIMEZONE_NAME, TIMEZONE = _zone(get("owner", "timezone"))

IPHONE_BACKUP_ROOT = _path("iphone", "backup_root", os.path.join(DATA, "iphone-backup"))
ANDROID_EXPORT = _path("android", "export", os.path.join(DATA, "android"))
VIBER_DESKTOP = _path("viber", "desktop_export")
WHATSAPP_BRIDGE = _path("whatsapp", "bridge")

IMMICH_URL = (get("immich", "url") or "").rstrip("/") or None
IMMICH_DATA = _path("immich", "data_folder")
IMMICH_PREFIX = get("immich", "container_prefix", "/usr/src/app/upload/")
IMMICH_MAKE = get("immich", "make", "Chronika")
OLLAMA_URL = (get("ollama", "url") or "http://localhost:11434").rstrip("/")
OLLAMA_MODEL = get("ollama", "model", "qwen2.5vl:7b")

MEDIA_STORE = _path("media", "store", DATA)
ASIDE = _path("media", "aside", os.path.join(DATA, "aside"))

SERVER_ORIGIN = (get("server", "origin") or f"http://localhost:{get('server', 'port', 8520)}").rstrip("/")
SERVER_HOST = get("server", "host", "127.0.0.1")
SERVER_PORT = int(get("server", "port", 8520))

REVIEW_PERSON = get("review", "person")
REVIEW_ME = get("review", "me", "εγώ")
DOG_TAG = get("review", "dog_tag")
DOG_THRESHOLD = get("review", "dog_threshold", 0.0005)
REVIEW_REFERENCE = get("review", "reference_model", "claude-sonnet")


def iphone_udid():
    """The iPhone's UDID: the one set, else the only backup in IPHONE_BACKUP_ROOT, else the only
    phone on the cable."""
    if udid := get("iphone", "udid"):
        return udid
    root = IPHONE_BACKUP_ROOT
    found = [d for d in os.listdir(root)
             if os.path.exists(os.path.join(root, d, "Manifest.plist"))] if os.path.isdir(root) else []
    if not found:
        try:
            found = subprocess.run(["idevice_id", "-l"], capture_output=True, text=True, encoding="utf-8").stdout.split()
        except FileNotFoundError:
            pass
    if len(found) == 1:
        return found[0]
    sys.exit(f"Δεν βρέθηκε ένα μόνο iPhone ({len(found)}): γράψε το UDID του στο {CONFIG_FILE}, "
             f"[iphone] udid = \"...\".")


def iphone_backup():
    return os.path.join(IPHONE_BACKUP_ROOT, iphone_udid())


def exposed(path):
    """Whether others may read a secret file. Windows does not say in st_mode: not checked there."""
    return os.name != "nt" and bool(os.stat(path).st_mode & 0o077)


# Secrets (the backup password, the immich key, a new source's token) are kept in the system's
# keyring (Secret Service on Linux, the Keychain on macOS, the Credential Manager on Windows), under
# the service APP and the secret's name. Where there is no keyring (a headless Linux without a
# Secret Service), a file of that name in CONFIG, mode 600, is used instead.

def _keyring(action, name, value=None):
    """The keyring's answer, or None where there is no usable keyring."""
    try:
        import keyring
        from keyring.errors import KeyringError
    except ImportError:
        return None
    try:
        if action == "get":
            return keyring.get_password(KEYRING, name)
        keyring.set_password(KEYRING, name, value)
        return keyring.get_password(KEYRING, name) == value
    except KeyringError:
        return None


def secret_file(name):
    return os.path.join(CONFIG, name)


def secret(name):
    """A secret by name: from the keyring, else from its file in CONFIG; None if neither has it."""
    value = _keyring("get", name)
    if value:
        return value
    path = secret_file(name)
    if not os.path.exists(path):
        return None
    if exposed(path):
        sys.exit(f"Το {path} διαβάζεται και από άλλους· chmod 600 και ξανά.")
    with open(path, encoding="utf-8") as f:
        return f.read().rstrip("\r\n")


def save_secret(name, value):
    """Into the keyring, else into its file (600). Returns where it went, for the message."""
    if _keyring("set", name, value):
        return "keyring"
    path = secret_file(name)
    os.makedirs(CONFIG, mode=0o700, exist_ok=True)
    try:                                # one left by an interrupted write: never written through
        os.unlink(path + ".part")
    except FileNotFoundError:
        pass
    fd = os.open(path + ".part", os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0), 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write(value)
    os.replace(path + ".part", path)
    return path


def move_to_keyring(name):
    """Copy a file secret into the keyring, check it reads back the same, then offer to remove the
    file (it is removed only on "y"). The value is never shown."""
    path = secret_file(name)
    if not os.path.exists(path):
        sys.exit(f"Δεν υπάρχει το {path}.")
    if exposed(path):
        sys.exit(f"Το {path} διαβάζεται και από άλλους· chmod 600 και ξανά.")
    with open(path, encoding="utf-8") as f:
        value = f.read().rstrip("\r\n")
    if not _keyring("set", name, value):
        sys.exit("Δεν υπάρχει keyring σε αυτό το σύστημα (ή αρνήθηκε): το αρχείο μένει όπως είναι.")
    print(f"Το {name} είναι στο keyring ({APP}/{name}) και διαβάζεται σωστά.")
    if input(f"Να σβηστεί το {path}; [y/N] ").strip().lower() == "y":
        os.remove(path)
        print("Σβήστηκε.")
    else:
        print("Το αρχείο έμεινε· το keyring προηγείται, οπότε δεν χρησιμοποιείται πια.")


def require(value, section, key):
    """A setting a script cannot do without: its value, or the way out with what to set."""
    if value is None:
        sys.exit(f"Χρειάζεται η ρύθμιση [{section}] {key} στο {CONFIG_FILE}.")
    return value
