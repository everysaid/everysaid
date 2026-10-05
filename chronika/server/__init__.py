"""`chronika serve`: the app (API, UI, plugins, live connections) on one port.

    chronika serve [--host H] [--port P] [--archive PATH]

Listens on 127.0.0.1:8520 unless config `[server] host/port` say otherwise. To reach it from other
devices, put it behind a reverse proxy with HTTPS (Caddy, nginx) or a private network (WireGuard,
Tailscale), and set `[server] origin` to the address the devices use: passkeys are tied to it.
"""
import argparse
import os


def main(argv=None):
    from .. import config
    ap = argparse.ArgumentParser(prog="chronika serve", description="The app: API, UI, plugins, live connections.")
    ap.add_argument("--host", default=config.SERVER_HOST)
    ap.add_argument("--port", type=int, default=config.SERVER_PORT)
    ap.add_argument("--archive", help="archive database (default: the user's)")
    args = ap.parse_args(argv)
    os.umask(0o077)
    import uvicorn
    from .app import create_app
    app = create_app(args.archive)
    print(f"Chronika: {config.SERVER_ORIGIN} (ακούει στο {args.host}:{args.port})", flush=True)
    uvicorn.run(app, host=args.host, port=args.port, log_level="warning", proxy_headers=True,
                forwarded_allow_ips="127.0.0.1")
