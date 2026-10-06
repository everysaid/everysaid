"""The core: every question about the archive and every change to it, for the app, the MCP server,
the command line and the tests alike. No web framework here.

    store = Store(path)                 # one per archive
    queries.chats(store), queries.stream(store, "p12"), queries.search(store, "καλημέρα"), ...
    changes.set_person(store, 12, name="..."), changes.merge_people(store, 12, 40), ...
"""
from .store import Store

__all__ = ["Store"]
