import asyncio
import json


def call(mcp, name, **args):
    out = asyncio.run(mcp.call_tool(name, args))
    sc = getattr(out, "structuredContent", None)
    if isinstance(sc, dict) and "result" in sc:
        return sc["result"]
    parsed = [json.loads(b.text) for b in out.content]
    return parsed[0] if len(parsed) == 1 and not name.startswith(("list_", "find_")) else parsed


def test_tools(store):
    from everysaid.mcp_server import build
    mcp = build(store.path)
    names = {t.name for t in asyncio.run(mcp.list_tools())}
    assert {"search_messages", "read_chat", "get_person", "day_timeline"} <= names
    chats = call(mcp, "list_chats", limit=5)
    assert chats and chats[0]["id"]
    page = call(mcp, "read_chat", chat=chats[0]["id"], limit=10)
    assert page["items"]
    r = call(mcp, "search_messages", query="καλημερα", limit=3)
    assert r["total"] > 0 and r["items"][0]["time"]
    people = call(mcp, "find_people", query=chats[0]["title"][:4])
    assert people


def test_media_on_demand(store, tmp_path):
    from everysaid import plugins
    from everysaid.mcp_server import build
    lib = tmp_path / "lib"
    lib.mkdir()
    with store.write() as db:
        db.execute("UPDATE plugin_instance SET is_default = 0 WHERE kind = 'library'")
    iid = plugins.create(store, "folder", "Test", {"path": str(lib)})
    plugins.update(store, iid, is_default=True)
    mcp = build(store.path)
    chat = next(c for c in call(mcp, "list_chats", limit=50) if c["type"] == "person")
    found = []
    for c in call(mcp, "list_chats", limit=50):
        found = call(mcp, "find_media", chat=c["id"], kind="image", limit=2)
        if found:
            break
    assert found and found[0]["file_here"] and found[0]["in_library"] is None
    r = call(mcp, "send_media_to_library", sha256=found[0]["sha256"])
    assert r.get("already") is False, r
    again = call(mcp, "find_media", chat=c["id"], kind="image", limit=2)
    assert again[0]["in_library"] == "Test"
    assert chat
