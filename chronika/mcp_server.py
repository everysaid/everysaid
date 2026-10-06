"""`chronika mcp`: the archive for an assistant, as an MCP server over stdio.

    chronika mcp [--archive PATH]

Every tool is a call into the core (the same answers the app gives). Times are given in the user's
time zone (config `[owner] timezone`), as ISO text. Reading is free; the changes an assistant can
make are a person's name and note, and storing a file in the photo library (media on demand: found
with find_media, checked against the library first), each only with the user's approval.
"""
import argparse
from datetime import datetime, timedelta

from . import config


def when(ms):
    return datetime.fromtimestamp(ms / 1000, config.TIMEZONE).isoformat(timespec="minutes") if ms else None


def slim(item):
    """A stream item as the assistant needs it."""
    if item["type"] == "call":
        out = {"type": "call", "id": item["id"], "time": when(item["ts"]), "service": item["service"],
               "direction": "out" if item["outgoing"] else "in", "answered": item["answered"],
               "duration_s": item["duration"], "video": item["video"]}
        if item.get("detail"):
            out["detail"] = item["detail"]
        return out
    out = {"id": item["id"], "time": when(item["ts"]), "service": item["service"],
           "from": "me" if item["outgoing"] else (item.get("sender") or "them"), "kind": item["kind"]}
    if item.get("text"):
        out["text"] = item["text"]
    for k in ("subtype", "edited", "deleted", "forwarded"):
        if item.get(k):
            out[k] = item[k]
    if item.get("reply"):
        out["reply_to"] = {"id": item["reply"]["id"], "text": item["reply"]["text"][:120]}
    if item.get("reactions"):
        out["reactions"] = [r["emoji"] or r["code"] for r in item["reactions"]]
    if item.get("attachments"):
        out["files"] = [{"mime": a["mime"], "size": a["size"]} for a in item["attachments"]]
    if item.get("location"):
        out["location"] = item["location"]
    if item.get("chat_id"):
        out["chat"] = item["chat_id"]
    return out


def build(archive_path=None):
    from mcp.server.mcpserver import MCPServer
    from .archive import DB
    from .core import Store, changes, queries
    from .plugins import services
    store = Store(archive_path or DB)
    known = ", ".join(f"{v['name']} ({k})" for k, v in services().items())
    mcp = MCPServer("chronika", instructions=(
        f"The user's personal archive of messages and calls, from the services its plugins bring: {known}. "
        "A person is one chat (id p<number>) whatever services they were reached on; groups are "
        "c<number>. Find people or chats first (find_people, list_chats), then read (read_chat, search_messages). "
        "Times are in the user's time zone."))

    @mcp.tool()
    def search_messages(query: str, chat: str | None = None, service: str | None = None, since: str | None = None,
                        until: str | None = None, limit: int = 30, offset: int = 0, match_case: bool = False,
                        whole_words: bool = False) -> dict:
        """Messages containing every word of `query`, newest first: anywhere, inside words too, accents
        and case ignored; match_case: as typed; whole_words: whole words only (a word ending in * a
        prefix). chat: a chat id to search within; service: a service id (as in the instructions or
        list_chats); since/until: YYYY-MM-DD."""
        day = lambda d: int(datetime.strptime(d, "%Y-%m-%d").replace(tzinfo=config.TIMEZONE).timestamp() * 1000) if d else None
        r = queries.search(store, query, chat_id=chat, service=service, since=day(since), until=day(until),
                           limit=min(limit, 100), offset=offset, case=match_case, whole=whole_words)
        return {"total": r["total"], "items": [dict(slim(i), chat_title=i.get("chat_title")) for i in r["items"]]}

    @mcp.tool()
    def list_chats(query: str | None = None, kind: str | None = None, limit: int = 50) -> list:
        """Chats, most recent first: people (one chat per person across services), groups, notes.
        query: part of the name; kind: person, group or conversation."""
        return [{"id": c["id"], "title": c["title"], "type": c["type"], "services": c["services"],
                 "last": when(c["last_ts"]), "unread": c["unread"]}
                for c in queries.chats(store, kind=kind, q=query, limit=min(limit, 500))]

    @mcp.tool()
    def read_chat(chat: str, before: str | None = None, around_date: str | None = None, limit: int = 50) -> dict:
        """A page of a chat, oldest first: messages and calls. Without before/around_date: the latest.
        before: the `cursor` of the oldest item seen, for the page before it; around_date: YYYY-MM-DD."""
        around = None
        if around_date:
            around = int(datetime.strptime(around_date, "%Y-%m-%d").replace(tzinfo=config.TIMEZONE).timestamp() * 1000)
        page = queries.stream(store, chat, before=before, around=around, limit=min(limit, 200))
        items = page["items"]
        return {"chat": chat, "items": [slim(i) for i in items], "has_older": page["has_older"],
                "older_cursor": items[0]["cursor"] if items else None}

    @mcp.tool()
    def message_context(message_id: int, n: int = 10) -> dict:
        """A message with the n messages before and after it in its chat."""
        ctx = queries.context(store, message_id, min(n, 50))
        if not ctx:
            return {"error": "no such message"}
        return {"chat": ctx["chat_id"], "items": [slim(i) for i in ctx["items"]]}

    @mcp.tool()
    def find_people(query: str, limit: int = 20) -> list:
        """People whose name or handle (number, username, email) contains `query`."""
        return queries.people_list(store, query, min(limit, 100))["items"]

    @mcp.tool()
    def get_person(person_id: int) -> dict:
        """A person: names, numbers and handles per service, message and call counts, first and last
        contact, the groups they are in, the user's note."""
        p = queries.person(store, person_id)
        if not p:
            return {"error": "no such person"}
        s = p["stats"]
        return {"id": p["id"], "name": p["name"], "chat": f"p{p['id']}", "note": p["note"],
                "handles": [{"kind": h["kind"], "value": h["label"], "service": h["service"]} for h in p["handles"]],
                "messages": s["messages"], "messages_by_service": s["by_service"], "calls": s["calls"],
                "first": when(s["first"]), "last": when(s["last"]), "groups": p["groups"]}

    @mcp.tool()
    def list_calls(chat: str | None = None, missed_only: bool = False, limit: int = 50) -> list:
        """Calls of every service, newest first, optionally of one chat."""
        r = queries.calls(store, chat_id=chat, missed=missed_only, limit=min(limit, 200))
        return [dict(slim(i), **{"with": i.get("with"), "chat": i.get("chat_id")}) for i in r["items"]]

    @mcp.tool()
    def day_timeline(date: str) -> dict:
        """Everything of one day (YYYY-MM-DD) across all chats, in order."""
        d = datetime.strptime(date, "%Y-%m-%d").replace(tzinfo=config.TIMEZONE)
        start = int(d.timestamp() * 1000)
        r = queries.timeline(store, start, int((d + timedelta(days=1)).timestamp() * 1000))
        return {"items": [slim(i) for i in r["items"]], "truncated": r["truncated"]}

    @mcp.tool()
    def statistics() -> dict:
        """Counts: messages and calls by service and year, people, groups, the most written-to people."""
        s = dict(queries.stats(store, include_archived=True))
        s["first"], s["last"] = when(s["first"]), when(s["last"])
        return s

    @mcp.tool()
    def find_media(chat: str, kind: str = "image", since: str | None = None, until: str | None = None,
                   limit: int = 10) -> list:
        """Files of one chat, newest first ("the last two pictures X sent"): kind image, video, voice,
        file or all; since/until YYYY-MM-DD. Each says whether its file is still here and whether the
        photo library already holds it."""
        day = lambda d: int(datetime.strptime(d, "%Y-%m-%d").replace(tzinfo=config.TIMEZONE).timestamp() * 1000) if d else None
        before = day(until) + 86400_000 if until else None
        items = queries.media(store, chat_id=chat, kind=kind, before=before, limit=min(limit, 100) * 2)["items"]
        lo = day(since)
        db = store.read()
        out = []
        for m in items:
            if lo and m["ts"] < lo:
                break
            linked = db.execute("SELECT library FROM library_link WHERE sha256 = ?", (m["sha256"],)).fetchone()
            out.append({"sha256": m["sha256"], "time": when(m["ts"]), "mime": m["mime"], "size": m["size"],
                        "file_here": m["available"] == "local", "in_library": linked[0] if linked else None,
                        "message_id": m["message_id"]})
            if len(out) >= limit:
                break
        return out

    @mcp.tool()
    def send_media_to_library(sha256: str, date: str | None = None) -> dict:
        """Store one file in the default photo library, unless it is already there (checked first).
        Its date: the file's own EXIF date if it has one, else `date` (YYYY-MM-DD HH:MM) if given, else
        the message's. Only with the user's explicit approval of this very file."""
        from .server.host import Host
        when_ms = None
        if date:
            when_ms = int(datetime.strptime(date, "%Y-%m-%d %H:%M").replace(tzinfo=config.TIMEZONE).timestamp() * 1000)
        try:
            r = Host(store).to_library(sha256, date_ms=when_ms)
        except Exception as e:
            return {"error": str(e)}
        changes.decide_media(store, sha256, "library", when_ms)
        return r

    @mcp.tool()
    def set_person_note(person_id: int, note: str) -> dict:
        """Write the user's note about a person (replaces it). Ask the user before using."""
        changes.set_person(store, person_id, note=note)
        return {"ok": True}

    @mcp.tool()
    def rename_person(person_id: int, name: str) -> dict:
        """Give a person the name the user wants shown (empty: back to the contact's or the service's).
        Ask the user before using."""
        changes.set_person(store, person_id, name=name)
        return {"ok": True, "name": queries.person(store, person_id)["name"]}

    return mcp


def main(argv=None):
    ap = argparse.ArgumentParser(prog="chronika mcp", description="The archive as an MCP server (stdio).")
    ap.add_argument("--archive")
    args = ap.parse_args(argv)
    build(args.archive).run()
