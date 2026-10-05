"""`chronika user`: what is done on the server's own machine.

    chronika user list                      the users
    chronika user link [--user ID]          a one-time link for a passkey: the first user, another
                                            device, or a way back in after losing every passkey
    chronika user mcp-token [--user ID]     a token for the MCP server over HTTP
    chronika user sessions [--user ID]      the open sessions
"""
import argparse

from .. import config
from .auth import Auth


def main(argv=None):
    ap = argparse.ArgumentParser(prog="chronika user", description="Users, passkeys, recovery.")
    ap.add_argument("what", choices=["list", "link", "mcp-token", "sessions"])
    ap.add_argument("--user", type=int)
    ap.add_argument("--minutes", type=int, default=60)
    args = ap.parse_args(argv)
    auth = Auth()
    users = auth.users()
    uid = args.user or (users[0]["id"] if users else None)
    if args.what == "list":
        for u in users:
            print(f"{u['id']}\t{u['name']}\t{u['archive']}\t{len(auth.passkeys(u['id']))} passkeys")
        if not users:
            print("κανένας χρήστης ακόμα: `chronika serve` τυπώνει τον σύνδεσμο πρώτης ρύθμισης")
    elif args.what == "link":
        token = auth.setup_link(uid, args.minutes)
        print(f"{config.SERVER_ORIGIN}/setup#{token}\n(μίας χρήσης, ισχύει {args.minutes} λεπτά"
              f"{'' if uid else '· νέος χρήστης'})")
    elif args.what == "mcp-token":
        if not uid:
            raise SystemExit("κανένας χρήστης")
        print(auth.new_mcp_token(uid, "cli"))
    elif args.what == "sessions":
        for s in auth.sessions(uid):
            print(f"{s['id']}\t{s['via']}\t{s['ip']}\t{s['agent']}")
