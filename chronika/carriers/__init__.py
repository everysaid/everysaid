"""Parsers of the notices carriers send as SMS about calls the phone did not get, one module per
carrier or country, enabled by config `[import] carrier_notices` (e.g. ["gr"]).

Each module has SOURCE, the archive's source name for the calls it finds, and
alerts(text, sent) -> [(number, when, attempts, busy)], when and sent aware datetimes.
"""
import importlib


def enabled(names):
    """The modules of the names given; an unknown name stops with the ones there are."""
    out = []
    for name in names:
        try:
            out.append(importlib.import_module(f"{__name__}.{name}"))
        except ModuleNotFoundError:
            raise SystemExit(f"Άγνωστος πάροχος στο [import] carrier_notices: {name}")
    return out
