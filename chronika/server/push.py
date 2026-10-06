"""Push notifications (Web Push): new incoming messages reach the user's devices while the app is
closed. The payload is end-to-end encrypted for each subscription (RFC 8291), so the browser's push
service (Apple's, Google's, Mozilla's) carries it without reading it. The server's VAPID key is a
secret (`vapid-private`, in the keyring). Muted chats send nothing; the setting `push_preview`
(default on) decides whether the text is shown or only who wrote.
"""
import base64
import json
import threading
import time

from .. import config
from ..core import queries
from ..plugins.i18n import tr

KIND_LABEL = {"image": "📷 Photo", "video": "🎬 Video", "voice": "🎤 Voice message", "file": "📎 File",
              "sticker": "Sticker", "location": "📍 Location", "contact": "👤 Contact"}     # said through tr()


class Push:
    def __init__(self, auth):
        self.auth = auth
        self._key = None

    def key(self):
        """(the VAPID key, its public half as the browser wants it: base64url of the raw point)."""
        if self._key:
            return self._key
        from cryptography.hazmat.primitives import serialization
        from py_vapid import Vapid02
        pem = config.secret("vapid-private")
        if not pem:
            v = Vapid02()
            v.generate_keys()
            pem = v.private_pem().decode()
            config.save_secret("vapid-private", pem)
        v = Vapid02.from_pem(pem.encode())
        raw = v.public_key.public_bytes(serialization.Encoding.X962, serialization.PublicFormat.UncompressedPoint)
        self._key = (v, base64.urlsafe_b64encode(raw).rstrip(b"=").decode())
        return self._key

    def subscribe(self, uid, sub):
        self.auth.x("INSERT OR REPLACE INTO push_subscription VALUES (?, ?, ?, ?)",
                    (sub["endpoint"], uid, json.dumps(sub.get("keys") or {}), int(time.time())))

    def unsubscribe(self, uid, endpoint):
        self.auth.x("DELETE FROM push_subscription WHERE user_id = ? AND endpoint = ?", (uid, endpoint))

    def subscriptions(self, archive_path):
        return self.auth.q("SELECT s.endpoint, s.keys FROM push_subscription s JOIN user u ON u.id = s.user_id "
                           "WHERE u.archive = ?", (archive_path,))

    def alert(self, store, title, body):
        """A notice about the app itself (a plugin's warning), to every device of the archive's users."""
        subs = self.subscriptions(store.path)
        if subs:
            payload = {"title": title, "body": (body or "")[:240], "chat": None, "tag": "alert"}
            threading.Thread(target=self._send, args=(subs, [payload]), daemon=True).start()

    def notify(self, store, incoming):
        """incoming: [(chat id, message id, text, kind)] of one archive."""
        subs = self.subscriptions(store.path)
        if not subs:
            return
        states = queries._states(store)
        preview = store.setting("push_preview", True)
        by_chat = {}
        for cid, mid, txt, kind in incoming:
            if states.get(cid, (0, 0, 0, None, {}))[1]:     # muted
                continue
            by_chat.setdefault(cid, []).append((mid, txt, kind))
        payloads = []
        index, _ = queries._chat_index(store)
        for cid, msgs in by_chat.items():
            if cid not in index:
                continue
            title = queries.chat_title(store, index[cid])
            mid, txt, kind = msgs[-1]
            lang = store.setting("language") or "en"
            new = tr("New message", lang)
            body = (txt or tr(KIND_LABEL[kind], lang) if kind in KIND_LABEL else txt or new) if preview else new
            if len(msgs) > 1:
                body = f"({len(msgs)}) {body}"
            payloads.append({"title": title, "body": body[:240], "chat": cid, "message": mid, "tag": cid})
        if payloads:
            threading.Thread(target=self._send, args=(subs, payloads), daemon=True).start()

    def _send(self, subs, payloads):
        from pywebpush import WebPushException, webpush
        vapid, _ = self.key()
        claims = {"sub": f"mailto:{config.get('server', 'contact', 'chronika@localhost')}"}
        for endpoint, keys in subs:
            for p in payloads:
                try:
                    webpush({"endpoint": endpoint, "keys": json.loads(keys)}, json.dumps(p, ensure_ascii=False),
                            vapid_private_key=vapid, vapid_claims=dict(claims), ttl=3600)
                except WebPushException as e:
                    if e.response is not None and e.response.status_code in (404, 410):     # gone
                        self.auth.x("DELETE FROM push_subscription WHERE endpoint = ?", (endpoint,))
                        break
                except Exception:
                    pass
