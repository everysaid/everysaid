"""Nothing the user reads skips translation.

The interface's words are in web/src/lib/i18n.ts (Greek and English; tsc already fails when a key is
in one and not the other); the server's (plugins, logs, notifications) are English in code with the
Greek in chronika/plugins/i18n.py. These tests check what tsc cannot: that every key the code uses
exists and none is left unused, that the server's error codes and the archive's vocabularies have
words, that no text in the interface bypasses t(), that the plugins' words have Greek, and that no
Greek is left in the Python code outside the dictionaries.
"""
import ast
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
WEB = ROOT / "web" / "src"
I18N = WEB / "lib" / "i18n.ts"
GREEK = re.compile(r"[Ͱ-Ͽἀ-῿]")


def flatten(src, start):
    """The keys of the object literal that opens at src[start] ('{'), as 'a.b.c'."""
    keys, path, i, key = [], [], start + 1, None
    while i < len(src):
        c = src[i]
        if c in "\"'`":
            j = i + 1
            while src[j] != c:
                j += 2 if src[j] == "\\" else 1
            if key is not None:
                keys.append(".".join(path + [key]))
                key = None
            i = j + 1
            continue
        if src.startswith("//", i):
            i = src.index("\n", i)
            continue
        if c == "{":
            path.append(key)
            key = None
        elif c == "}":
            if not path:
                return keys
            path.pop()
        else:
            m = re.match(r"([A-Za-z_][\w]*)\s*:", src[i:])
            if m and src[i - 1] in " \n{,":
                key = m.group(1)
                i += m.end()
                continue
        i += 1
    return keys


def ui_keys(lang="el"):
    src = I18N.read_text(encoding="utf-8")
    start = src.index("{", src.index(f"const {lang}"))
    return set(flatten(src, start))


def sources():
    return {p: p.read_text(encoding="utf-8") for p in WEB.rglob("*.tsx")} | \
           {p: p.read_text(encoding="utf-8") for p in WEB.rglob("*.ts") if p != I18N}


# keys the code builds from data: each whole namespace is used
DYNAMIC = ("errors.", "kind.", "call.", "nav.", "settings.via.", "sources.", "people.why", "chat.state")


def test_both_languages_have_the_same_keys():
    assert ui_keys("el") == ui_keys("en")


def test_every_key_used_exists_and_none_is_unused():
    keys = ui_keys()
    used = set()
    for path, src in sources().items():
        for k in re.findall(r"""\bt\(\s*["']([\w.]+)["']""", src):
            used.add(k)
            assert k in keys or any(x.startswith(k + "_") for x in keys), f"{path.name}: t('{k}') is not in i18n.ts"
        used |= set(re.findall(r"""["']((?:[a-z]+\.)+[a-zA-Z_]+)["']""", src)) & keys    # keys in tables (WHY, STATE_FIELDS)
    unused = {k for k in keys - used if not k.startswith(DYNAMIC)
              and not any(k == u + "_one" or k == u + "_other" for u in used)}
    assert not unused, f"unused keys in i18n.ts: {sorted(unused)}"


def test_server_error_codes_have_words():
    keys = ui_keys()
    codes = set()
    for p in (ROOT / "chronika").rglob("*.py"):
        s = p.read_text(encoding="utf-8")
        codes |= set(re.findall(r'UserError\(\s*"([\w.]+)"', s)) | set(re.findall(r'"code": "([\w.]+)"', s))
    missing = {c for c in codes if f"errors.{c}" not in keys}
    assert codes and not missing, f"error codes without words: {sorted(missing)}"


def test_the_archives_vocabularies_have_words():
    from chronika import archive
    keys = ui_keys()
    assert {f"kind.{k}" for k in archive.MESSAGE_KINDS} <= keys
    assert {f"call.{d}" for d in archive.VOCABULARY["call.detail"]} <= keys


def test_no_text_in_the_interface_bypasses_t():
    allowed = {"Chronika", "QR", "Ελληνικά", "English", "Passkey", "abcd-ef01-2345-6789"}
    found = []
    for path, src in sources().items():
        if path.suffix != ".tsx":
            continue
        for m in re.finditer(r">\s*([^<>{}\n]*[A-Za-zΑ-Ωα-ωά-ώ]{2,}[^<>{}\n]*?)\s*<", src):
            text = m.group(1).strip()
            # code, not text: generics (api.get<T>), conditions, types
            if text and text not in allowed and not re.search(r"[()=;?:|&]|^[\w.]+$|^,", text):
                found.append(f"{path.name}: >{text}<")
        for m in re.finditer(r'\b(aria-label|title|placeholder|alt)="([^"]*[A-Za-zΑ-Ωα-ω]{2,}[^"]*)"', src):
            if m.group(2) not in allowed:
                found.append(f"{path.name}: {m.group(1)}=\"{m.group(2)}\"")
    assert not found, "text outside t():\n" + "\n".join(found)


PROPER = {"Telegram", "WhatsApp", "Viber", "iMessage", "SMS", "MMS", "RCS", "FaceTime", "Messenger", "Signal",
          "UDID", "API key", "adb", "Android (adb)", "immich", "Immich", "CardDAV", "URL", "Passkey",
          "Viber Desktop", "libimobiledevice (idevicebackup2)"}


def test_the_plugins_words_have_greek():
    from chronika import plugins
    same = []
    for p in plugins.REGISTRY.values():
        el, en = p.manifest("el"), p.manifest("en")
        pairs = [(el["name"], en["name"]), (el["description"], en["description"]), *zip(el["needs"], en["needs"]),
                 *[(a["label"], b["label"]) for a, b in zip(el["settings"], en["settings"])],
                 *[(a["help"], b["help"]) for a, b in zip(el["settings"], en["settings"]) if b["help"]],
                 *[(a["label"], b["label"]) for a, b in zip(el["actions"], en["actions"])]]
        same += [f"{p.id}: {b!r}" for a, b in pairs if a == b and b not in PROPER and re.search(r"[a-z]{3}", b)]
    for x in plugins.name_sources("el"):
        if x["id"] != "contacts" and "(" not in x["label"]:
            same.append(f"name source {x['id']}: {x['label']!r}")
    assert not same, "no Greek for:\n" + "\n".join(same)


# Greek is said through plugins/i18n.py; these files keep Greek of their own on purpose: the
# dictionary itself, the folding of text, the carriers' notices (they are Greek SMS), the demo's
# invented people. The importers and the command line still speak Greek (their translation is in
# the README's plans); new code must not add to this list.
GREEK_ALLOWED = {
    "plugins/i18n.py", "text.py", "demo.py", "voip.py", "carriers",
    "cli.py", "config.py", "importers.py", "server/users.py", "server/__init__.py",
    "sms.py", "calls.py", "viber.py", "whatsapp.py", "telegram.py", "media.py", "extras.py", "archive.py",
    "core/store.py", "mcp_server.py",
}


def test_no_greek_in_the_python_code():
    found = []
    for p in (ROOT / "chronika").rglob("*.py"):
        rel = p.relative_to(ROOT / "chronika").as_posix()
        if rel in GREEK_ALLOWED or rel.split("/")[0] in GREEK_ALLOWED:
            continue
        text = p.read_text(encoding="utf-8")
        tree, lines = ast.parse(text), text.splitlines()
        docs = {id(n.body[0].value) for n in ast.walk(tree)
                if isinstance(n, (ast.Module, ast.FunctionDef, ast.ClassDef, ast.AsyncFunctionDef))
                and n.body and isinstance(n.body[0], ast.Expr) and isinstance(n.body[0].value, ast.Constant)}
        for n in ast.walk(tree):
            if isinstance(n, ast.Constant) and isinstance(n.value, str) and id(n) not in docs and GREEK.search(n.value) \
                    and not lines[n.lineno - 1].rstrip().endswith("# console"):     # the command line's own words
                found.append(f"{rel}:{n.lineno}: {n.value[:60]!r}")
    assert not found, "Greek outside the dictionaries:\n" + "\n".join(found)
