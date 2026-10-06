"""Import the logs of Adium and Pidgin (Gaim) into the archive: `chronika/imlogs.py`.

    uv run python scripts/imlogs-import.py --adium "/path/to/Adium 2.0" --pidgin /path/to/.purple
    uv run python scripts/imlogs-import.py ... --dry-run

--dry-run imports into a copy of the archive in a temporary folder and prints the same report,
touching neither the archive nor its media; what it says "new" is what a real run would add.
"""
import argparse
import os
import shutil
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from chronika import imlogs
from chronika.archive import DB, Archive


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--adium", help="Adium's folder (Adium 2.0, Users/Default or Logs) (default: config [imlogs] adium)")
    ap.add_argument("--pidgin", help="Pidgin's .purple folder or its logs, (default: config [imlogs] pidgin)")
    ap.add_argument("--db", default=DB, help=f"archive database (default {DB})")
    ap.add_argument("--dry-run", action="store_true", help="report only, on a throwaway copy of the archive")
    args = ap.parse_args()
    if args.dry_run:
        tmp = tempfile.mkdtemp(prefix="chronika-imlogs-")
        path = os.path.join(tmp, "archive.db")
        if os.path.exists(args.db):
            shutil.copy(args.db, path)
        try:
            a = Archive(path)
            stats = imlogs.run(a, args.adium, args.pidgin, media=False)
            a.db.close()
        finally:
            shutil.rmtree(tmp, ignore_errors=True)
        print("(dry run: nothing written)")
    else:
        stats = imlogs.run(Archive(args.db), args.adium, args.pidgin)
    return 0 if stats is not None else 1


if __name__ == "__main__":
    sys.exit(main())
