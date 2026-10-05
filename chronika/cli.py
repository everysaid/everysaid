"""The `chronika` command.

    chronika import [--db PATH] [IMPORTER...]   fill the archive (the importers, in their order)
    chronika serve [--host H] [--port P]        the app: API, UI, plugins, live connections
    chronika mcp [--db PATH]                    the MCP server for an assistant (stdio)
    chronika demo [--dir DIR]                   a demo archive of invented people, for trying the app
    chronika user ...                           users, passkeys, recovery (see `chronika user -h`)

`python -m chronika sms calls ...` (importer names alone) still runs the importers.
"""
import argparse
import os
import sys

COMMANDS = ("import", "serve", "mcp", "demo", "user")


def cmd_import(argv):
    from . import importers
    from .archive import DB, Archive
    ap = argparse.ArgumentParser(prog="chronika import", description="Fill the archive from the sources.")
    ap.add_argument("--db", default=DB, help=f"archive database (default {DB})")
    ap.add_argument("importers", nargs="*",
                    help=f"{', '.join(importers.IMPORTERS)}; default: config [import] importers, else all")
    args = ap.parse_args(argv)
    chosen = importers.chosen(args.importers)
    unknown = [n for n in chosen if n not in importers.IMPORTERS]
    if unknown:
        ap.error(f"άγνωστα: {', '.join(unknown)} (υπάρχουν: {', '.join(importers.IMPORTERS)})")
    importers.run(Archive(args.db), chosen)


def cmd_serve(argv):
    from .server import main
    main(argv)


def cmd_mcp(argv):
    from .mcp_server import main
    main(argv)


def cmd_demo(argv):
    from .demo import main
    main(argv)


def cmd_user(argv):
    from .server.users import main
    main(argv)


def main(argv=None):
    argv = list(sys.argv[1:] if argv is None else argv)
    if argv and argv[0] in COMMANDS:
        return globals()[f"cmd_{argv[0]}"](argv[1:])
    if argv and argv[0] in ("-h", "--help"):
        print(__doc__)
        return
    return cmd_import(argv)         # the importers by name, as before


if __name__ == "__main__":
    os.umask(0o077)
    main()
