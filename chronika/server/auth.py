"""Who may use the app: users, their passkeys, sessions and recovery codes, in `<data>/server.db`
(apart from the archives: one server, later more than one user, each with an archive of their own).

- Passkeys (WebAuthn) are the first way in: a fingerprint, a face, a security key or a password
  manager; discoverable, so logging in asks for nothing first.
- Where a passkey cannot be used (a browser or password manager that refuses it), a password
  together with a 6-digit code from an authenticator app (TOTP, RFC 6238) is the other way: both are
  required. Passwords are kept as scrypt hashes; a code is accepted once.
- The first passkey of a new server is made through a one-time setup link printed on the terminal
  (`chronika serve` prints it while there is no user; `chronika user link` prints a new one any time,
  also for adding a passkey on a new device or after losing one).
- Recovery codes (ten, each once) let the user in without a passkey, to add a new one.
- A session is a random token in an HttpOnly, SameSite=Strict cookie (Secure over HTTPS); only its
  hash is stored. Sessions are listed and can be ended one by one; idle ones end after 30 days.
"""
import base64
import hashlib
import hmac
import json
import struct
import os
import secrets
import sqlite3
import threading
import time

from .. import config
from ..errors import UserError

DB = os.path.join(config.DATA, "server.db")
SESSION_DAYS = 30
COOKIE = "chronika_session"

SCHEMA = """
CREATE TABLE IF NOT EXISTS user (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    handle BLOB NOT NULL UNIQUE,            -- WebAuthn user handle (random)
    archive TEXT NOT NULL,                  -- the user's archive
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS passkey (
    id BLOB PRIMARY KEY,                    -- credential id
    user_id INTEGER NOT NULL REFERENCES user,
    public_key BLOB NOT NULL,
    sign_count INTEGER NOT NULL,
    transports TEXT,
    name TEXT,
    created_at INTEGER NOT NULL,
    last_used INTEGER
);
CREATE TABLE IF NOT EXISTS session (
    hash TEXT PRIMARY KEY,                  -- sha256 of the token
    user_id INTEGER NOT NULL REFERENCES user,
    created_at INTEGER NOT NULL,
    last_seen INTEGER NOT NULL,
    agent TEXT,
    ip TEXT,
    via TEXT                                -- passkey, recovery, setup
);
CREATE TABLE IF NOT EXISTS recovery_code (
    user_id INTEGER NOT NULL REFERENCES user,
    hash TEXT NOT NULL,
    used_at INTEGER,
    PRIMARY KEY (user_id, hash)
);
CREATE TABLE IF NOT EXISTS push_subscription (
    endpoint TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES user,
    keys TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS mcp_token (
    hash TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES user,
    label TEXT,
    created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
    ts INTEGER NOT NULL,
    user_id INTEGER,
    event TEXT NOT NULL,
    detail TEXT
);
CREATE TABLE IF NOT EXISTS kv (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
"""


def h(token):
    return hashlib.sha256(token.encode()).hexdigest()


class Auth:
    def __init__(self, path=DB):
        os.makedirs(os.path.dirname(path), exist_ok=True)
        self.path = path
        self.lock = threading.Lock()
        self.db = sqlite3.connect(path, check_same_thread=False, isolation_level=None)
        self.db.execute("PRAGMA journal_mode = WAL")
        self.db.execute("PRAGMA busy_timeout = 10000")
        self.db.executescript(SCHEMA)
        self._upgrade()
        os.chmod(path, 0o600)
        self.setup_tokens = {}      # token -> (expires, user id or None)
        self.challenges = {}        # nonce -> (expires, challenge, purpose, user id)
        self.attempts = {}          # ip -> [times]

    def _upgrade(self):
        have = {r[1] for r in self.db.execute("PRAGMA table_info(user)")}
        for col in ("password TEXT", "totp TEXT", "totp_step INTEGER"):
            if col.split()[0] not in have:
                self.db.execute(f"ALTER TABLE user ADD COLUMN {col}")

    def q(self, sql, args=()):
        with self.lock:
            return self.db.execute(sql, args).fetchall()

    def x(self, sql, args=()):
        with self.lock:
            cur = self.db.execute(sql, args)
            return cur.lastrowid

    # users
    def users(self):
        return [{"id": i, "name": n, "archive": a, "created_at": c}
                for i, n, a, c in self.q("SELECT id, name, archive, created_at FROM user ORDER BY id")]

    def user(self, uid):
        r = self.q("SELECT id, name, handle, archive FROM user WHERE id = ?", (uid,))
        return {"id": r[0][0], "name": r[0][1], "handle": r[0][2], "archive": r[0][3]} if r else None

    def create_user(self, name, archive):
        uid = self.x("INSERT INTO user (name, handle, archive, created_at) VALUES (?, ?, ?, ?)",
                     (name, secrets.token_bytes(32), archive, int(time.time())))
        self.log(uid, "user created", name)
        return uid

    # setup links (first passkey, or another one)
    def setup_link(self, uid=None, minutes=60):
        token = secrets.token_urlsafe(32)
        self.setup_tokens[token] = (time.time() + minutes * 60, uid)
        self.x("INSERT OR REPLACE INTO kv VALUES (?, ?)",
               (f"setup:{h(token)}", json.dumps([time.time() + minutes * 60, uid])))
        return token

    def setup_left(self, token):
        """Seconds a setup link has left (0: none, or used, or expired)."""
        exp_uid = self.setup_tokens.get(token) if token else None
        if exp_uid is None and token:
            r = self.q("SELECT value FROM kv WHERE key = ?", (f"setup:{h(token)}",))
            exp_uid = tuple(json.loads(r[0][0])) if r else None
        return max(0, int(exp_uid[0] - time.time())) if exp_uid else 0

    def take_setup(self, token, consume=False):
        """(valid, user id or None). Links made by `chronika user link` live in the database."""
        if not token:
            return False, None
        exp_uid = self.setup_tokens.get(token)
        if exp_uid is None:
            r = self.q("SELECT value FROM kv WHERE key = ?", (f"setup:{h(token)}",))
            exp_uid = tuple(json.loads(r[0][0])) if r else None
        if not exp_uid or exp_uid[0] < time.time():
            return False, None
        if consume:
            self.setup_tokens.pop(token, None)
            self.x("DELETE FROM kv WHERE key = ?", (f"setup:{h(token)}",))
        return True, exp_uid[1]

    # challenges
    def challenge(self, challenge, purpose, uid=None, ttl=300):
        nonce = secrets.token_urlsafe(24)
        now = time.time()
        self.challenges = {k: v for k, v in self.challenges.items() if v[0] > now}
        self.challenges[nonce] = (now + ttl, challenge, purpose, uid)
        return nonce

    def take_challenge(self, nonce, purpose, keep=False):
        """(challenge, info), or None if unknown or expired; `keep` leaves it for another try."""
        v = self.challenges.get(nonce or "") if keep else self.challenges.pop(nonce or "", None)
        if not v or v[0] < time.time() or v[2] != purpose:
            return None
        return v[1], v[3]

    # passkeys
    def passkeys(self, uid):
        return [{"id": i.hex(), "name": n, "created_at": c, "last_used": u, "transports": json.loads(t or "[]")}
                for i, n, c, u, t in self.q("SELECT id, name, created_at, last_used, transports FROM passkey "
                                             "WHERE user_id = ? ORDER BY created_at", (uid,))]

    def add_passkey(self, uid, cred_id, public_key, sign_count, transports, name):
        self.x("INSERT INTO passkey (id, user_id, public_key, sign_count, transports, name, created_at) "
               "VALUES (?, ?, ?, ?, ?, ?, ?)", (cred_id, uid, public_key, sign_count, json.dumps(transports or []),
                                                name, int(time.time())))
        self.log(uid, "passkey added", name)

    def passkey(self, cred_id):
        r = self.q("SELECT user_id, public_key, sign_count FROM passkey WHERE id = ?", (cred_id,))
        return r[0] if r else None

    def used_passkey(self, cred_id, sign_count):
        self.x("UPDATE passkey SET sign_count = ?, last_used = ? WHERE id = ?", (sign_count, int(time.time()), cred_id))

    def remove_passkey(self, uid, cred_hex):
        if len(self.passkeys(uid)) <= 1 and not self.has_password(uid):
            raise UserError("auth.last_passkey")
        self.x("DELETE FROM passkey WHERE user_id = ? AND id = ?", (uid, bytes.fromhex(cred_hex)))
        self.log(uid, "passkey removed", cred_hex[:12])

    def rename_passkey(self, uid, cred_hex, name):
        self.x("UPDATE passkey SET name = ? WHERE user_id = ? AND id = ?", (name, uid, bytes.fromhex(cred_hex)))

    # recovery codes
    def new_recovery_codes(self, uid, n=10):
        codes = ["-".join(secrets.token_hex(2) for _ in range(4)) for _ in range(n)]
        with self.lock:
            self.db.execute("DELETE FROM recovery_code WHERE user_id = ?", (uid,))
            self.db.executemany("INSERT INTO recovery_code (user_id, hash) VALUES (?, ?)", [(uid, h(c)) for c in codes])
        self.log(uid, "recovery codes made")
        return codes

    def use_recovery_code(self, code):
        code = (code or "").strip().lower()
        r = self.q("SELECT user_id FROM recovery_code WHERE hash = ? AND used_at IS NULL", (h(code),))
        if not r:
            return None
        self.x("UPDATE recovery_code SET used_at = ? WHERE hash = ?", (int(time.time()), h(code)))
        self.log(r[0][0], "recovery code used")
        return r[0][0]

    def recovery_left(self, uid):
        return self.q("SELECT count(*) FROM recovery_code WHERE user_id = ? AND used_at IS NULL", (uid,))[0][0]

    # sessions
    def new_session(self, uid, agent=None, ip=None, via="passkey"):
        token = secrets.token_urlsafe(32)
        now = int(time.time())
        self.x("INSERT INTO session VALUES (?, ?, ?, ?, ?, ?, ?)", (h(token), uid, now, now, (agent or "")[:200], ip, via))
        self.log(uid, "login", via)
        return token

    def recent(self, token, minutes=15):
        """Whether this session signed in within the last minutes: changing the ways in asks for it,
        so that a stolen session cannot add one of its own."""
        r = self.q("SELECT created_at FROM session WHERE hash = ?", (h(token),)) if token else None
        return bool(r) and r[0][0] >= time.time() - minutes * 60

    def session(self, token):
        """(user id, session hash) of a live session, renewing it; None otherwise."""
        if not token:
            return None
        hs = h(token)
        r = self.q("SELECT user_id, last_seen FROM session WHERE hash = ?", (hs,))
        if not r:
            return None
        uid, last = r[0]
        now = int(time.time())
        if now - last > SESSION_DAYS * 86400:
            self.x("DELETE FROM session WHERE hash = ?", (hs,))
            return None
        if now - last > 60:
            self.x("UPDATE session SET last_seen = ? WHERE hash = ?", (now, hs))
        return uid, hs

    def sessions(self, uid):
        return [{"id": hs[:16], "created_at": c, "last_seen": s, "agent": a, "ip": ip, "via": v}
                for hs, c, s, a, ip, v in self.q("SELECT hash, created_at, last_seen, agent, ip, via FROM session "
                                                  "WHERE user_id = ? ORDER BY last_seen DESC", (uid,))]

    def end_session(self, uid, short_id):
        self.x("DELETE FROM session WHERE user_id = ? AND substr(hash, 1, 16) = ?", (uid, short_id))
        self.log(uid, "session ended", short_id)

    def end_session_hash(self, hs):
        self.x("DELETE FROM session WHERE hash = ?", (hs,))

    # MCP tokens
    def new_mcp_token(self, uid, label):
        token = "chk_" + secrets.token_urlsafe(32)
        self.x("INSERT INTO mcp_token VALUES (?, ?, ?, ?)", (h(token), uid, label, int(time.time())))
        self.log(uid, "mcp token made", label)
        return token

    def mcp_user(self, token):
        r = self.q("SELECT user_id FROM mcp_token WHERE hash = ?", (h(token or ""),))
        return r[0][0] if r else None

    # rate limit and audit
    def allow(self, ip, limit=20, window=300):
        now = time.time()
        seen = [t for t in self.attempts.get(ip, []) if t > now - window]
        if len(seen) >= limit:
            self.attempts[ip] = seen
            return False
        self.attempts[ip] = seen + [now]
        return True

    def log(self, uid, event, detail=None):
        self.x("INSERT INTO audit VALUES (?, ?, ?, ?)", (int(time.time()), uid, event, detail))

    def audit(self, uid, limit=200):
        return [{"ts": t, "event": e, "detail": d} for t, e, d in
                self.q("SELECT ts, event, detail FROM audit WHERE user_id = ? ORDER BY ts DESC LIMIT ?", (uid, limit))]

    def kv(self, key, default=None):
        r = self.q("SELECT value FROM kv WHERE key = ?", (key,))
        return json.loads(r[0][0]) if r else default

    def set_kv(self, key, value):
        self.x("INSERT OR REPLACE INTO kv VALUES (?, ?)", (key, json.dumps(value)))

    # password and authenticator code
    LOCK_FAILURES, LOCK_MINUTES = 5, 15

    def locked(self, uid, ip=None):
        """Five failed password sign-ins from one address within 15 minutes lock the password way
        for that address for the rest of that time (others, passkeys and recovery codes still work:
        whoever knows the name cannot lock the user out). Each address is also rate limited."""
        since = int(time.time()) - self.LOCK_MINUTES * 60
        n = self.q("SELECT count(*) FROM audit WHERE user_id = ? AND event = 'password login failed' AND ts >= ? "
                   "AND detail IS ? AND ts > ifnull((SELECT max(ts) FROM audit WHERE user_id = ? AND event = 'login'), 0)",
                   (uid, since, ip, uid))[0][0]
        return n >= self.LOCK_FAILURES

    def set_password(self, uid, password, totp_secret, used_step=None):
        """used_step: the time step of the code that confirmed the secret (not accepted again)."""
        self.x("UPDATE user SET password = ?, totp = ?, totp_step = ? WHERE id = ?",
               (hash_password(password), totp_secret, used_step, uid))
        self.log(uid, "password set")

    def clear_password(self, uid):
        if not self.passkeys(uid):
            raise UserError("auth.only_password")
        self.x("UPDATE user SET password = NULL, totp = NULL, totp_step = NULL WHERE id = ?", (uid,))
        self.log(uid, "password removed")

    def has_password(self, uid):
        r = self.q("SELECT password IS NOT NULL FROM user WHERE id = ?", (uid,))
        return bool(r and r[0][0])

    def check_password(self, name, password, code, ip=None):
        """The user whose name, password and current code these are, or None (the same work either way)."""
        rows = self.q("SELECT id, password, totp, totp_step FROM user WHERE lower(name) = lower(?) AND password IS NOT NULL",
                      ((name or "").strip(),))
        uid, stored, secret, last = rows[0] if rows else (None, DUMMY_HASH, None, None)
        if rows and self.locked(uid, ip):
            verify_password(password or "", DUMMY_HASH)         # the same time spent
            self.log(uid, "password login refused", "too many failures")
            return None
        ok = verify_password(password or "", stored)
        step = totp_check(secret, code, last) if secret else None
        if not (rows and ok and step is not None):
            if rows:
                self.log(uid, "password login failed", ip)
            return None
        self.x("UPDATE user SET totp_step = ? WHERE id = ?", (step, uid))
        return uid


def hash_password(password, n=2 ** 15, r=8, p=1):
    salt = os.urandom(16)
    h = hashlib.scrypt(password.encode(), salt=salt, n=n, r=r, p=p, maxmem=64 * 1024 * 1024, dklen=32)
    return f"scrypt${n}${r}${p}${salt.hex()}${h.hex()}"


def verify_password(password, stored):
    try:
        _, n, r, p, salt, h = stored.split("$")
        got = hashlib.scrypt(password.encode(), salt=bytes.fromhex(salt), n=int(n), r=int(r), p=int(p),
                             maxmem=64 * 1024 * 1024, dklen=32)
        return hmac.compare_digest(got.hex(), h)
    except (ValueError, AttributeError):
        return False


DUMMY_HASH = hash_password(secrets.token_hex(8))     # for unknown names: the same time spent


def totp_secret():
    return base64.b32encode(os.urandom(20)).decode().rstrip("=")


def totp_at(secret, step):
    key = base64.b32decode(secret + "=" * (-len(secret) % 8))
    digest = hmac.new(key, struct.pack(">Q", step), hashlib.sha1).digest()
    o = digest[-1] & 15
    return f"{(struct.unpack('>I', digest[o:o + 4])[0] & 0x7FFFFFFF) % 1_000_000:06d}"


def totp_check(secret, code, last_step=None, window=1):
    """The time step a 6-digit code belongs to (now, or one step either side), if not used before."""
    code = "".join(c for c in str(code or "") if c.isdigit())
    if len(code) != 6:
        return None
    now = int(time.time()) // 30
    for step in range(now - window, now + window + 1):
        if (last_step is None or step > last_step) and hmac.compare_digest(totp_at(secret, step), code):
            return step
    return None


def totp_uri(secret, name, issuer="Chronika"):
    from urllib.parse import quote
    return f"otpauth://totp/{quote(issuer)}:{quote(name)}?secret={secret}&issuer={quote(issuer)}&digits=6&period=30"

