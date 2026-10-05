"""The app's server: the API over the core, the WebSocket of live events, the PWA's files.

Guards on every request:
- Host must name this server (its origin's host, or localhost): no DNS rebinding.
- Everything under /api needs a session (the cookie), except the login steps.
- A change (any method but GET/HEAD) needs the header `X-Chronika: 1`, which another site's page
  cannot send without a CORS preflight this server never grants; and its Origin, when sent, must be
  this server's. With SameSite=Strict cookies this closes CSRF.
- Responses carry a strict Content-Security-Policy (only this server's own scripts), no framing,
  no referrer, no sniffing; HSTS over HTTPS.
- Login attempts are limited per address.
"""
import asyncio
from contextlib import asynccontextmanager
from datetime import datetime, timedelta
import io
import json
import mimetypes
import os
import shutil
import subprocess
import time
from urllib.parse import urlparse

from fastapi import Body, FastAPI, Request, WebSocket, WebSocketDisconnect
from fastapi.responses import FileResponse, JSONResponse, Response
from starlette.middleware.base import BaseHTTPMiddleware

from .. import config, plugins
from ..errors import UserError
from ..plugins.i18n import tr
from ..core import Store, changes, queries
from ..core.names import name_order, name_sources_present, people
from .auth import COOKIE, Auth, totp_check, totp_secret, totp_uri
from .host import Host
from .push import Push

WEB = os.path.join(os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))), "web", "dist")
THUMBS = os.path.join(config.CACHE, "thumbs")
AVATARS = os.path.join(config.CACHE, "avatars")
OPEN = {"/api/auth/status", "/api/auth/login/options", "/api/auth/login/verify", "/api/auth/register/options",
        "/api/auth/register/verify", "/api/auth/recover", "/api/health", "/api/auth/password/options",
        "/api/auth/password/set", "/api/auth/password/login"}

CSP = ("default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; "
       "media-src 'self' blob:; font-src 'self' data:; connect-src 'self' {ws}; worker-src 'self'; "
       "manifest-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")


def origins():
    main = config.SERVER_ORIGIN
    extra = [o.strip().rstrip("/") for o in os.environ.get("CHRONIKA_EXTRA_ORIGINS", "").split(",") if o.strip()]
    return [main, *extra]


def rp_id():
    return urlparse(config.SERVER_ORIGIN).hostname


class Guard(BaseHTTPMiddleware):
    def __init__(self, app, auth):
        super().__init__(app)
        self.auth = auth
        self.hosts = {urlparse(o).netloc for o in origins()} | {
            f"localhost:{config.SERVER_PORT}", f"127.0.0.1:{config.SERVER_PORT}"}
        self.origins = set(origins()) | {f"http://localhost:{config.SERVER_PORT}", f"http://127.0.0.1:{config.SERVER_PORT}"}
        self.https = config.SERVER_ORIGIN.startswith("https://")

    async def dispatch(self, request, call_next):
        if request.headers.get("host", "") not in self.hosts:
            return JSONResponse({"detail": {"code": "auth.unknown_host", "params": {}}}, 421)
        path = request.url.path
        if path.startswith("/api/"):
            if request.method not in ("GET", "HEAD", "OPTIONS"):
                if request.headers.get("x-chronika") != "1":
                    return JSONResponse({"detail": {"code": "auth.header_missing", "params": {}}}, 403)
                origin = request.headers.get("origin")
                if origin and origin not in self.origins:
                    return JSONResponse({"detail": {"code": "auth.foreign_origin", "params": {}}}, 403)
            if path not in OPEN:
                s = self.auth.session(request.cookies.get(COOKIE))
                if not s:
                    return JSONResponse({"detail": {"code": "auth.sign_in_needed", "params": {}}}, 401)
                request.state.uid, request.state.session = s
        response = await call_next(request)
        ws = " ".join({"ws" + o[4:] for o in self.origins})
        response.headers["Content-Security-Policy"] = CSP.format(ws=ws)
        response.headers["X-Content-Type-Options"] = "nosniff"
        response.headers["X-Frame-Options"] = "DENY"
        response.headers["Referrer-Policy"] = "no-referrer"
        response.headers["Permissions-Policy"] = "camera=(), microphone=(), geolocation=(), payment=()"
        response.headers["Cross-Origin-Opener-Policy"] = "same-origin"
        if self.https:
            response.headers["Strict-Transport-Security"] = "max-age=31536000"
        if path.startswith("/api/") and "cache-control" not in response.headers:
            response.headers["Cache-Control"] = "no-store"
        return response


def create_app(archive_path=None, auth_path=None):
    from .auth import DB as AUTH_DB
    from ..archive import DB as ARCHIVE_DB
    auth = Auth(auth_path or AUTH_DB)
    push = Push(auth)
    archive_path = os.path.abspath(archive_path or ARCHIVE_DB)
    store = Store(archive_path)
    host = Host(store, push)

    @asynccontextmanager
    async def lifespan(app):
        await host.startup()
        if not auth.users():
            token = auth.setup_link()
            print(f"\n  Πρώτη ρύθμιση: άνοιξε {config.SERVER_ORIGIN}/setup#{token}\n  (ισχύει μία ώρα)\n", flush=True)  # console
        yield
        await host.shutdown()

    app = FastAPI(title="Chronika", lifespan=lifespan, docs_url=None, redoc_url=None, openapi_url="/api/openapi.json")
    app.add_middleware(Guard, auth=auth)
    app.state.auth, app.state.store, app.state.host, app.state.push = auth, store, host, push

    @app.exception_handler(UserError)
    async def user_error(request: Request, e: UserError):
        """A code (and parameters) the interface says in the user's language; a plugin's English text
        said here in the language the interface asks for (header X-Lang)."""
        detail = {"code": e.code, "params": e.params}
        if e.text:
            detail["text"] = tr(e.text, request.headers.get("x-lang", "en"))
        return JSONResponse({"detail": detail}, e.status)

    def cookie(response, token):
        response.set_cookie(COOKIE, token, max_age=30 * 86400, httponly=True, samesite="strict",
                            secure=config.SERVER_ORIGIN.startswith("https://"), path="/")

    def client_ip(request):
        return request.client.host if request.client else None

    # ---- auth ------------------------------------------------------------------------------------
    from webauthn import (generate_authentication_options, generate_registration_options, options_to_json,
                          verify_authentication_response, verify_registration_response)
    from webauthn.helpers import base64url_to_bytes
    from webauthn.helpers.structs import (AuthenticatorSelectionCriteria, PublicKeyCredentialDescriptor,
                                          ResidentKeyRequirement, UserVerificationRequirement)

    @app.get("/api/health")
    def health():
        return {"ok": True}

    @app.get("/api/auth/status")
    def auth_status(request: Request):
        s = auth.session(request.cookies.get(COOKIE))
        u = auth.user(s[0]) if s else None
        return {"logged_in": bool(u), "user": {"id": u["id"], "name": u["name"]} if u else None,
                "needs_setup": not auth.users(), "rp_id": rp_id()}

    def changer(request, token):
        """The user a change to the ways in is for: a valid setup link's (None: a new user), else a
        session that signed in within the last minutes."""
        ok, uid = auth.take_setup(token)
        if ok:
            return uid
        s = auth.session(request.cookies.get(COOKIE))
        if s and auth.recent(request.cookies.get(COOKIE)):
            return s[0]
        if s:
            raise UserError("auth.recent_sign_in", 403)
        raise UserError("auth.bad_link", 403)

    @app.post("/api/auth/register/options")
    def register_options(request: Request, body: dict = Body(...)):
        """A new passkey: with a setup token (the first user, or a link from `chronika user link`),
        or from a logged-in session (another device)."""
        if not auth.allow(client_ip(request)):
            raise UserError("too_many", 429)
        uid = changer(request, body.get("token"))
        name = (body.get("name") or "").strip() or (auth.user(uid)["name"] if uid else "Chronika")
        user = auth.user(uid) if uid else None
        handle = user["handle"] if user else os.urandom(32)
        opts = generate_registration_options(
            rp_id=rp_id(), rp_name="Chronika", user_name=name, user_display_name=name, user_id=handle,
            authenticator_selection=AuthenticatorSelectionCriteria(
                resident_key=ResidentKeyRequirement.REQUIRED, user_verification=UserVerificationRequirement.PREFERRED),
            exclude_credentials=[PublicKeyCredentialDescriptor(id=bytes.fromhex(p["id"])) for p in auth.passkeys(uid)] if uid else None)
        nonce = auth.challenge(opts.challenge, "register", {"uid": uid, "name": name, "handle": handle.hex(),
                                                            "token": body.get("token")})
        return {"nonce": nonce, "options": json.loads(options_to_json(opts))}

    @app.post("/api/auth/register/verify")
    def register_verify(request: Request, body: dict = Body(...)):
        got = auth.take_challenge(body.get("nonce"), "register")
        if not got:
            raise UserError("auth.challenge_expired", 400)
        challenge, info = got
        try:
            v = verify_registration_response(credential=body["credential"], expected_challenge=challenge,
                                             expected_rp_id=rp_id(), expected_origin=origins())
        except Exception as e:
            raise UserError("auth.passkey_failed", 400, reason=str(e))
        uid = info["uid"]
        codes = None
        if uid is None:
            if info.get("token") and not auth.take_setup(info["token"], consume=True)[0]:
                raise UserError("auth.bad_link", 403)
            uid = auth.create_user(info["name"], archive_path)
            auth.x("UPDATE user SET handle = ? WHERE id = ?", (bytes.fromhex(info["handle"]), uid))
            codes = auth.new_recovery_codes(uid)
        elif info.get("token"):
            auth.take_setup(info["token"], consume=True)
        auth.add_passkey(uid, v.credential_id, v.credential_public_key, v.sign_count,
                         body["credential"].get("response", {}).get("transports"),
                         (body.get("label") or request.headers.get("user-agent", "")[:60]))
        token = auth.new_session(uid, request.headers.get("user-agent"), client_ip(request), "setup" if codes else "passkey")
        r = JSONResponse({"ok": True, "recovery_codes": codes})
        cookie(r, token)
        return r

    @app.post("/api/auth/login/options")
    def login_options(request: Request):
        if not auth.allow(client_ip(request)):
            raise UserError("too_many", 429)
        opts = generate_authentication_options(rp_id=rp_id(), user_verification=UserVerificationRequirement.PREFERRED)
        return {"nonce": auth.challenge(opts.challenge, "login"), "options": json.loads(options_to_json(opts))}

    @app.post("/api/auth/login/verify")
    def login_verify(request: Request, body: dict = Body(...)):
        got = auth.take_challenge(body.get("nonce"), "login")
        if not got:
            raise UserError("auth.challenge_expired", 400)
        cred = body["credential"]
        found = auth.passkey(base64url_to_bytes(cred.get("rawId") or cred.get("id")))
        if not found:
            raise UserError("auth.unknown_passkey", 403)
        uid, public_key, count = found
        try:
            v = verify_authentication_response(credential=cred, expected_challenge=got[0], expected_rp_id=rp_id(),
                                               expected_origin=origins(), credential_public_key=public_key,
                                               credential_current_sign_count=count)
        except Exception as e:
            auth.log(uid, "login failed", str(e)[:200])
            raise UserError("auth.passkey_rejected", 403)
        auth.used_passkey(base64url_to_bytes(cred.get("rawId") or cred.get("id")), v.new_sign_count)
        r = JSONResponse({"ok": True})
        cookie(r, auth.new_session(uid, request.headers.get("user-agent"), client_ip(request)))
        return r

    # ---- password with an authenticator code: the way in where a passkey cannot be made ----------
    @app.post("/api/auth/password/options")
    def password_options(request: Request, body: dict = Body(...)):
        """A new authenticator secret for setting a password: with a setup link (a new user, or a way
        back in) or from a signed-in session. The secret is kept only until the code confirms it."""
        if not auth.allow(client_ip(request)):
            raise UserError("too_many", 429)
        token = body.get("token")
        uid = changer(request, token)
        name = (auth.user(uid)["name"] if uid else (body.get("name") or "").strip()) or "Chronika"
        secret = totp_secret()
        # long enough to scan the code and choose a password (mistakes do not use it up), and never
        # longer than the setup link it came with
        ttl = min(1800, auth.setup_left(token)) if auth.setup_left(token) else 1800
        nonce = auth.challenge(secret, "password", {"uid": uid, "name": name, "token": token if auth.setup_left(token) else None}, ttl=ttl)
        return {"nonce": nonce, "secret": secret, "uri": totp_uri(secret, name), "name": name}

    @app.post("/api/auth/password/set")
    def password_set(request: Request, body: dict = Body(...)):
        got = auth.take_challenge(body.get("nonce"), "password", keep=True)
        if not got:
            raise UserError("auth.setup_expired", 410)
        secret, info = got
        password = body.get("password") or ""
        if len(password) < 12:
            raise UserError("auth.password_short", 400)
        step = totp_check(secret, body.get("code"))
        if step is None:
            raise UserError("auth.code_mismatch", 400)
        if info.get("token") and not auth.setup_left(info["token"]):
            raise UserError("auth.link_expired", 403)
        if not auth.take_challenge(body["nonce"], "password"):      # one of two at once gets it
            raise UserError("auth.setup_expired", 410)
        uid, codes = info["uid"], None
        if uid is None:
            if not auth.take_setup(info.get("token"), consume=True)[0]:
                raise UserError("auth.bad_link", 403)
            uid = auth.create_user(info["name"], archive_path)
            codes = auth.new_recovery_codes(uid)
        elif info.get("token"):
            auth.take_setup(info["token"], consume=True)
        auth.set_password(uid, password, secret, step)
        r = JSONResponse({"ok": True, "recovery_codes": codes, "name": auth.user(uid)["name"]})
        if not auth.session(request.cookies.get(COOKIE)):
            cookie(r, auth.new_session(uid, request.headers.get("user-agent"), client_ip(request), "password"))
        return r

    @app.post("/api/auth/password/login")
    def password_login(request: Request, body: dict = Body(...)):
        if not auth.allow(client_ip(request), limit=10):
            raise UserError("too_many", 429)
        uid = auth.check_password(body.get("name"), body.get("password"), body.get("code"), client_ip(request))
        if not uid:
            raise UserError("auth.bad_login", 403)
        r = JSONResponse({"ok": True})
        cookie(r, auth.new_session(uid, request.headers.get("user-agent"), client_ip(request), "password"))
        return r

    @app.delete("/api/auth/password")
    def password_remove(request: Request):
        changer(request, None)
        try:
            auth.clear_password(request.state.uid)
        except ValueError as e:
            raise UserError("failed", 400, reason=str(e))
        return {"ok": True}

    @app.post("/api/auth/recover")
    def recover(request: Request, body: dict = Body(...)):
        if not auth.allow(client_ip(request), limit=5):
            raise UserError("too_many", 429)
        uid = auth.use_recovery_code(body.get("code"))
        if not uid:
            raise UserError("auth.bad_recovery", 403)
        r = JSONResponse({"ok": True, "left": auth.recovery_left(uid)})
        cookie(r, auth.new_session(uid, request.headers.get("user-agent"), client_ip(request), "recovery"))
        return r

    @app.post("/api/auth/logout")
    def logout(request: Request):
        auth.end_session_hash(request.state.session)
        r = JSONResponse({"ok": True})
        r.delete_cookie(COOKIE, path="/")
        return r

    @app.get("/api/auth/account")
    def account(request: Request):
        uid = request.state.uid
        return {"user": {k: v for k, v in auth.user(uid).items() if k != "handle"}, "passkeys": auth.passkeys(uid),
                "sessions": [dict(s, current=request.state.session.startswith(s["id"])) for s in auth.sessions(uid)],
                "recovery_left": auth.recovery_left(uid), "audit": auth.audit(uid, 50),
                "has_password": auth.has_password(uid)}

    @app.delete("/api/auth/sessions/{sid}")
    def end_session(sid: str, request: Request):
        auth.end_session(request.state.uid, sid)
        return {"ok": True}

    @app.delete("/api/auth/passkeys/{pid}")
    def remove_passkey(pid: str, request: Request):
        changer(request, None)
        try:
            auth.remove_passkey(request.state.uid, pid)
        except ValueError as e:
            raise UserError("failed", 400, reason=str(e))
        return {"ok": True}

    @app.patch("/api/auth/passkeys/{pid}")
    def rename_passkey(pid: str, request: Request, body: dict = Body(...)):
        auth.rename_passkey(request.state.uid, pid, (body.get("name") or "")[:80])
        return {"ok": True}

    @app.post("/api/auth/recovery-codes")
    def new_codes(request: Request):
        changer(request, None)          # codes are a way in
        return {"codes": auth.new_recovery_codes(request.state.uid)}

    @app.post("/api/auth/mcp-token")
    def mcp_token(request: Request, body: dict = Body(...)):
        changer(request, None)          # a lasting key to the archive
        return {"token": auth.new_mcp_token(request.state.uid, (body.get("label") or "MCP")[:60])}

    # ---- chats and messages ----------------------------------------------------------------------
    def nf(x, code="not_found"):
        if x is None:
            raise UserError(code, 404)
        return x

    @app.get("/api/chats")
    def chats(kind: str | None = None, q: str | None = None, archived: bool = False, limit: int | None = None,
              offset: int = 0):
        return {"items": queries.chats(store, include_archived=archived, kind=kind, q=q, limit=limit, offset=offset)}

    @app.get("/api/chats/{chat_id}")
    def chat(chat_id: str):
        c = nf(queries.chat(store, chat_id))
        can = host.sendable()
        answer = host.replyable()
        return {**c, "sendable": [s for s in c["services"] if s in can],
                "replyable": [s for s in c["services"] if s in answer]}

    @app.get("/api/chats/{chat_id}/stream")
    def stream(chat_id: str, before: str | None = None, after: str | None = None, around: int | None = None,
               limit: int = 60):
        try:
            return queries.stream(store, chat_id, before=before, after=after, around=around, limit=min(limit, 200))
        except KeyError:
            raise UserError("not_found", 404)

    @app.patch("/api/chats/{chat_id}")
    def chat_state(chat_id: str, body: dict = Body(...)):
        fields = {k: body[k] for k in ("pinned", "muted", "archived", "read_until") if k in body}
        try:
            changes.set_chat_state(store, chat_id, always=bool(body.get("always")), **fields)
        except KeyError:
            raise UserError("not_found", 404)
        return {"ok": True}

    @app.post("/api/chats/{chat_id}/read")
    def read(chat_id: str):
        changes.set_chat_state(store, chat_id, read_until="now")
        return {"ok": True}

    @app.post("/api/chats/{chat_id}/send")
    async def send(chat_id: str, body: dict = Body(...)):
        text = (body.get("text") or "").strip()
        if not text:
            raise UserError("empty_message", 400)
        try:
            return await host.send(chat_id, text, body.get("conversation_id"), body.get("service"), body.get("reply_to"))
        except KeyError:
            raise UserError("not_found", 404)
        except (PermissionError, ValueError, RuntimeError) as e:
            raise UserError("failed", 409, reason=str(e))

    @app.get("/api/messages/{mid}")
    def message(mid: int):
        return nf(queries.message(store, mid))

    @app.get("/api/messages/{mid}/context")
    def context(mid: int, n: int = 15):
        return nf(queries.context(store, mid, min(n, 100)))

    @app.get("/api/search")
    def search(q: str, chat: str | None = None, service: str | None = None, kind: str | None = None,
               since: int | None = None, until: int | None = None, outgoing: bool | None = None,
               limit: int = 50, offset: int = 0, case: bool = False, whole: bool = False):
        return queries.search(store, q, chat_id=chat, service=service, kind=kind, since=since, until=until,
                              outgoing=outgoing, limit=min(limit, 200), offset=offset, case=case, whole=whole)

    # ---- people ----------------------------------------------------------------------------------
    @app.get("/api/people")
    def people_list(q: str | None = None, limit: int = 100, offset: int = 0):
        return queries.people_list(store, q, min(limit, 500), offset)

    @app.get("/api/people/suggestions")
    def suggestions():
        return {"items": queries.merge_suggestions(store)}

    @app.post("/api/people/suggestions/dismiss")
    def merge_dismiss(body: dict = Body(...)):
        changes.dismiss_merge(store, body.get("people") or [])
        return {"ok": True}

    @app.get("/api/people/{pid}")
    def person(pid: int):
        return nf(queries.person(store, pid))

    @app.patch("/api/people/{pid}")
    def person_edit(pid: int, body: dict = Body(...)):
        kw = {k: body[k] for k in ("name", "note", "name_source") if k in body}
        try:
            changes.set_person(store, pid, **kw)
        except KeyError:
            raise UserError("not_found", 404)
        except ValueError as e:
            raise UserError("failed", 400, reason=str(e))
        return queries.person(store, pid)

    @app.post("/api/people/{pid}/merge")
    def merge(pid: int, body: dict = Body(...)):
        try:
            changes.merge_people(store, pid, int(body["other"]))
        except (KeyError, ValueError) as e:
            raise UserError("failed", 400, reason=str(e))
        return queries.person(store, pid)

    @app.post("/api/addresses/{aid}/split")
    def split(aid: int):
        try:
            return {"person_id": changes.split_address(store, aid)}
        except KeyError:
            raise UserError("not_found", 404)

    @app.get("/api/avatar/{pid}")
    def avatar(pid: int):
        f = people(store).avatar(pid)
        path = os.path.join(AVATARS, f) if f else None
        if not path or not os.path.exists(path):
            raise UserError("no_photo", 404)
        return FileResponse(path, headers={"Cache-Control": "private, max-age=86400"})

    # ---- calls, media, timeline, stats ------------------------------------------------------------
    @app.get("/api/calls")
    def calls(chat: str | None = None, missed: bool = False, service: str | None = None, before: int | None = None,
              limit: int = 60):
        return queries.calls(store, chat_id=chat, missed=missed, service=service, before=before, limit=min(limit, 200))

    @app.get("/api/media")
    def media(chat: str | None = None, kind: str = "all", before: int | None = None, limit: int = 60,
              available: bool = False):
        return queries.media(store, chat_id=chat, kind=kind, before=before, limit=min(limit, 200),
                             available_only=available)

    @app.post("/api/media/{sha}/decision")
    def media_decision(sha: str, body: dict = Body(...)):
        try:
            changes.decide_media(store, sha, body.get("decision"), body.get("date_ms"))
        except (KeyError, ValueError) as e:
            raise UserError("failed", 400, reason=str(e))
        return {"ok": True}

    @app.post("/api/media/{sha}/library")
    async def media_library(sha: str, body: dict = Body(default={})):
        try:
            r = await asyncio.to_thread(host.to_library, sha, body.get("instance_id"), body.get("date_ms"))
        except Exception as e:
            raise UserError("failed", 409, reason=str(e))
        changes.decide_media(store, sha, "library")
        return r

    @app.get("/api/media/{sha}/{size}")
    async def media_file(sha: str, size: str):
        if size not in ("thumb", "preview", "original") or not sha.isalnum():
            raise UserError("not_found", 404)
        local = host.local_file(sha)
        cache = {"Cache-Control": "private, max-age=604800, immutable"}
        if local:
            if size == "original":
                return FileResponse(local, headers=cache, media_type=mimetypes.guess_type(local)[0])
            thumb = await asyncio.to_thread(make_thumb, local, sha, size)
            if thumb:
                return FileResponse(thumb, headers=cache, media_type="image/webp")
            raise UserError("no_preview", 404)
        got = await asyncio.to_thread(host.fetch, sha, "original" if size == "original" else
                                      ("thumbnail" if size == "thumb" else "preview"))
        if not got:
            raise UserError("file_gone", 404)
        if got[0] == "path":
            if size == "original":
                return FileResponse(got[1], headers=cache)
            thumb = await asyncio.to_thread(make_thumb, got[1], sha, size)
            return FileResponse(thumb or got[1], headers=cache)
        return Response(got[1], media_type=got[2] or "application/octet-stream", headers=cache)

    @app.get("/api/timeline")
    def timeline(day: str):
        try:
            d = datetime.strptime(day, "%Y-%m-%d").replace(tzinfo=config.TIMEZONE)
        except ValueError:
            raise UserError("bad_date", 400)
        start = int(d.timestamp() * 1000)
        return queries.timeline(store, start, int((d + timedelta(days=1)).timestamp() * 1000))

    @app.get("/api/stats")
    def stats():
        return queries.stats(store)

    # ---- plugins, devices, settings ----------------------------------------------------------------
    @app.get("/api/plugins/catalog")
    def catalog(lang: str = "en"):
        return {"items": plugins.catalog(lang)}

    @app.get("/api/plugins")
    def instances(lang: str = "en"):
        return {"items": [host.status(r["id"], lang) for r in plugins.instances(store)]}

    @app.post("/api/plugins")
    def add_instance(body: dict = Body(...)):
        p = plugins.get(body.get("plugin"))
        if not p:
            raise UserError("unknown_plugin", 400)
        label = (body.get("label") or p.name).strip()
        try:
            iid = plugins.create(store, p.id, label, {k: v for k, v in (body.get("settings") or {}).items()
                                                      if k in {s.key for s in p.settings if s.type != "secret"}})
        except Exception as e:
            raise UserError("failed", 409, reason=str(e))
        for s in p.settings:
            if s.type == "secret" and (body.get("secrets") or {}).get(s.key):
                host.ctx(iid).save_secret(s.key, body["secrets"][s.key])
        return host.status(iid)

    @app.get("/api/plugins/{iid}")
    def instance(iid: int):
        if not plugins.instance(store, iid):
            raise UserError("not_found", 404)
        return host.status(iid)

    @app.patch("/api/plugins/{iid}")
    def edit_instance(iid: int, body: dict = Body(...)):
        row = nf(plugins.instance(store, iid))
        p = plugins.get(row["plugin"])
        allowed = {s.key for s in (p.settings if p else ()) if s.type != "secret"}
        settings = {k: v for k, v in (body.get("settings") or {}).items() if k in allowed}
        plugins.update(store, iid, label=body.get("label"), settings=settings or None, enabled=body.get("enabled"),
                       is_default=body.get("is_default"))
        for k, v in (body.get("secrets") or {}).items():
            if p and k in {s.key for s in p.settings if s.type == "secret"} and v:
                host.ctx(iid).save_secret(k, v)
        if body.get("enabled") is False:
            host.stop_live(iid, remember=False)
        return host.status(iid)

    @app.delete("/api/plugins/{iid}")
    def delete_instance(iid: int):
        nf(plugins.instance(store, iid))
        host.stop_live(iid, remember=False)
        plugins.remove(store, iid)
        return {"ok": True}

    @app.post("/api/plugins/{iid}/run")
    async def run_instance(iid: int, body: dict = Body(default={})):
        nf(plugins.instance(store, iid))
        try:
            await host.run(iid, body.get("action"))
        except RuntimeError as e:
            raise UserError("failed", 409, reason=str(e))
        return {"ok": True}

    @app.get("/api/plugins/{iid}/chats")
    def plugin_chats(iid: int):
        row = nf(plugins.instance(store, iid))
        p = plugins.get(row["plugin"])
        return {"items": p.chats(host.ctx(iid)) if p else []}

    @app.put("/api/plugins/{iid}/chats")
    def plugin_chats_set(iid: int, body: dict = Body(...)):
        """The user's choice: which chats are imported, and whose media are downloaded."""
        nf(plugins.instance(store, iid))
        plugins.update(store, iid, settings={"skip_chats": [int(c) for c in body.get("skip") or []],
                                             "media_chats": [int(c) for c in body.get("media") or []]})
        return {"ok": True}

    @app.post("/api/plugins/{iid}/live")
    def live(iid: int, body: dict = Body(...)):
        nf(plugins.instance(store, iid))
        try:
            host.start_live(iid) if body.get("on") else host.stop_live(iid)
        except ValueError as e:
            raise UserError("failed", 400, reason=str(e))
        return host.status(iid)

    @app.get("/api/devices")
    def devices():
        return {"items": changes.devices(store)}

    @app.patch("/api/devices/{did}")
    def device(did: int, body: dict = Body(...)):
        kw = {k: body[k] for k in ("used_from", "used_until") if k in body}
        try:
            changes.set_device_period(store, did, **kw)
        except KeyError:
            raise UserError("not_found", 404)
        return {"items": changes.devices(store)}

    @app.get("/api/settings")
    def settings_get():
        return {**changes.settings(store), "name_order": name_order(store.read())}

    @app.get("/api/names")
    def name_sources(lang: str = "en"):
        """The sources of names: in the order in use, and the plugins' default order."""
        known = {x["id"]: x for x in plugins.name_sources(lang)}
        present = name_sources_present(store.read())        # only what this archive has, or may have
        mine = changes.settings(store).get("name_order")
        return {"order": [known[k] for k in name_order(store.read()) if k in present],
                "default": [x for x in known.values() if x["id"] in present], "custom": mine is not None}

    @app.get("/api/services")
    def services(lang: str = "en"):
        return plugins.services(lang)

    @app.put("/api/settings")
    def settings_put(body: dict = Body(...)):
        for k, v in body.items():
            if k in ("theme", "language", "push_preview", "density", "send_enter", "unread_since"):
                changes.set_setting(store, k, v)
            elif k == "name_order" and (v is None or isinstance(v, list) and all(isinstance(x, str) for x in v)
                                        and len(set(v)) == len(v) and set(v) <= set(plugins.name_weights())):
                changes.set_setting(store, k, v)          # None: back to the plugins' defaults
        return changes.settings(store)

    # ---- push --------------------------------------------------------------------------------------
    @app.get("/api/push/key")
    def push_key():
        return {"key": push.key()[1]}

    @app.post("/api/push/subscribe")
    def push_subscribe(request: Request, body: dict = Body(...)):
        if not str(body.get("endpoint", "")).startswith("https://"):
            raise UserError("push.bad_subscription", 400)
        push.subscribe(request.state.uid, body)
        return {"ok": True}

    @app.post("/api/push/unsubscribe")
    def push_unsubscribe(request: Request, body: dict = Body(...)):
        push.unsubscribe(request.state.uid, body.get("endpoint"))
        return {"ok": True}

    if os.environ.get("CHRONIKA_DEMO"):
        @app.post("/api/demo/incoming")
        async def demo_incoming(body: dict = Body(...)):
            """Only in the demo: a message arrives in a chat, from the other side (for trying and tests)."""
            from ..demo import demo_message
            c = nf(queries.chat(store, body.get("chat", "")))
            convs = [cid for cid in c["conversations"] if store.read().execute(
                "SELECT 1 FROM conversation x JOIN service s ON s.id = x.service_id WHERE x.id = ? AND s.name = ?",
                (cid, body.get("service"))).fetchone()] or c["conversations"]
            mid = await asyncio.to_thread(demo_message, host, convs[0], str(body.get("text") or "…"), False)
            return {"id": mid}

    @app.post("/api/push/test")
    def push_test(request: Request):
        subs = push.subscriptions(archive_path)
        if not subs:
            raise UserError("push.no_devices", 409)
        push._send(subs, [{"title": "Chronika", "body": tr("Test notification", store.setting("language") or "en"),
                           "chat": None, "tag": "test"}])
        return {"ok": True, "devices": len(subs)}

    # ---- live events -------------------------------------------------------------------------------
    @app.websocket("/api/events")
    async def events(ws: WebSocket):
        origin = ws.headers.get("origin")
        allowed = set(origins()) | {f"http://localhost:{config.SERVER_PORT}", f"http://127.0.0.1:{config.SERVER_PORT}"}
        if (origin and origin not in allowed) or not auth.session(ws.cookies.get(COOKIE)):
            await ws.close(code=4401)
            return
        await ws.accept()
        q = host.listen()
        try:
            await ws.send_json({"type": "hello", "ts": int(time.time() * 1000)})
            while True:
                try:
                    event = await asyncio.wait_for(q.get(), timeout=25)
                    await ws.send_json(event)
                except asyncio.TimeoutError:
                    await ws.send_json({"type": "ping"})
        except (WebSocketDisconnect, RuntimeError):
            pass
        finally:
            host.unlisten(q)

    # ---- the PWA -----------------------------------------------------------------------------------
    @app.get("/{path:path}")
    def spa(path: str):
        if path.startswith("api/"):
            raise UserError("not_found", 404)
        if not os.path.isdir(WEB):
            return JSONResponse({"detail": "The interface is not built yet: cd web && pnpm install && pnpm build"}, 503)
        f = os.path.normpath(os.path.join(WEB, path))
        if f.startswith(WEB + os.sep) and os.path.isfile(f):
            immutable = "/assets/" in f
            return FileResponse(f, headers={"Cache-Control": "public, max-age=31536000, immutable" if immutable
                                            else "no-cache"})
        return FileResponse(os.path.join(WEB, "index.html"), headers={"Cache-Control": "no-cache"})

    return app


def make_thumb(path, sha, size):
    """A WebP preview of a picture (or of a video's first second, with ffmpeg), cached."""
    os.makedirs(THUMBS, exist_ok=True)
    out = os.path.join(THUMBS, f"{sha}-{size}.webp")
    if os.path.exists(out):
        return out
    edge = 360 if size == "thumb" else 1600
    mime = mimetypes.guess_type(path)[0] or ""
    try:
        from PIL import Image, ImageOps
        try:
            import pillow_heif
            pillow_heif.register_heif_opener()
        except ImportError:
            pass
        if mime.startswith("video/"):
            ff = shutil.which("ffmpeg")
            if not ff:
                return None
            r = subprocess.run([ff, "-v", "quiet", "-ss", "1", "-i", path, "-frames:v", "1", "-f", "image2pipe",
                                "-vcodec", "png", "-"], capture_output=True, timeout=60)
            if not r.stdout:
                r = subprocess.run([ff, "-v", "quiet", "-i", path, "-frames:v", "1", "-f", "image2pipe",
                                    "-vcodec", "png", "-"], capture_output=True, timeout=60)
            img = Image.open(io.BytesIO(r.stdout))
        else:
            img = Image.open(path)
        img = ImageOps.exif_transpose(img)
        img.thumbnail((edge, edge))
        if img.mode not in ("RGB", "RGBA"):
            img = img.convert("RGB")
        tmp = out + ".part"
        img.save(tmp, "WEBP", quality=80)
        os.replace(tmp, out)
        return out
    except Exception:
        return None
