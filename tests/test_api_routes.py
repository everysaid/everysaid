"""Every call the interface makes to the server (`api.get/post/patch/put/del` with an "/api/..."
address in web/src) has a route of that method on the server: an address or a method that does not
match is a button that does nothing (as the chat info's state once was, "Method Not Allowed")."""
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
CALL = re.compile(r"api\.(get|post|patch|put|del)\b[^(`\"]{0,200}?\(\s*[`\"](/api/[^`\"]*)[`\"]", re.S)


def calls():
    out = set()
    for p in (ROOT / "web" / "src").rglob("*.ts*"):
        for method, path in CALL.findall(p.read_text(encoding="utf-8")):
            path = re.split(r"\?|\$\{qs\(", path)[0]             # the query is not the route
            path = re.sub(r"\$\{[^}]*\}", "{x}", path)            # a value in the address
            out.add((("DELETE" if method == "del" else method.upper()), path, p.name))
    return out


def test_every_call_of_the_interface_has_a_route(tmp_path):
    import shutil
    from everysaid.server.app import create_app
    from tests.conftest import PRISTINE
    db = tmp_path / "archive.db"
    shutil.copy(PRISTINE, db)
    app = create_app(str(db), str(tmp_path / "server.db"))
    routes = []
    for r in app.routes:
        methods = getattr(r, "methods", None) or set()
        if r.path.startswith("/api/"):
            routes.append((methods, re.compile("^" + re.sub(r"\{[^}]+\}", "[^/]+", r.path) + "$")))
    found = calls()
    assert len(found) > 50                                      # the pattern still finds them
    missing = sorted(f"{m} {p} ({f})" for m, p, f in found
                     if not any(m in methods and rx.match(p.replace("{x}", "1")) for methods, rx in routes))
    assert not missing, "calls of the interface with no route:\n" + "\n".join(missing)
