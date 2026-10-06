"""`everysaid serve`: the app (API, UI, plugins, live connections) on one port.

    everysaid serve [--host H] [--port P] [--archive PATH]

Listens on 127.0.0.1:8520 unless config `[server] host/port` say otherwise. To reach it from other
devices, put it behind a reverse proxy with HTTPS (Caddy, nginx) or a private network (WireGuard,
Tailscale), and set `[server] origin` to the address the devices use: passkeys are tied to it.

What goes wrong (a request that fails, a task that breaks) is written to the console and to
`<state>/logs/server.log` (kept to five files of 5 MB).
"""
import argparse
import copy
import os


def log_config(folder):
    """uvicorn's logging, with warnings and errors (its own and Everysaid's) also in a file."""
    from uvicorn.config import LOGGING_CONFIG
    os.makedirs(folder, exist_ok=True)
    cfg = copy.deepcopy(LOGGING_CONFIG)
    cfg["formatters"]["file"] = {"format": "%(asctime)s %(levelname)s %(name)s: %(message)s"}
    cfg["handlers"]["file"] = {"class": "logging.handlers.RotatingFileHandler", "level": "WARNING", "formatter": "file",
                               "filename": os.path.join(folder, "server.log"), "maxBytes": 5_000_000,
                               "backupCount": 4, "encoding": "utf-8"}
    cfg["loggers"]["uvicorn"]["handlers"].append("file")
    cfg["loggers"]["everysaid"] = {"handlers": ["default", "file"], "level": "WARNING", "propagate": False}
    return cfg


def main(argv=None):
    from .. import config
    ap = argparse.ArgumentParser(prog="everysaid serve", description="The app: API, UI, plugins, live connections.")
    ap.add_argument("--host", default=config.SERVER_HOST)
    ap.add_argument("--port", type=int, default=config.SERVER_PORT)
    ap.add_argument("--archive", help="archive database (default: the user's)")
    args = ap.parse_args(argv)
    os.umask(0o077)
    import uvicorn
    from .app import create_app
    app = create_app(args.archive)
    print(f"Everysaid: {config.SERVER_ORIGIN} (ακούει στο {args.host}:{args.port})", flush=True)
    uvicorn.run(app, host=args.host, port=args.port, log_level="warning", proxy_headers=True,
                forwarded_allow_ips="127.0.0.1", log_config=log_config(config.LOGS))
