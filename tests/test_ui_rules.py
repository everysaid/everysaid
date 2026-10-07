"""Rules of the interface's code that a whole kind of mistake breaks.

A button of a list that does something to its row (`x.mutate(row)`) spins only while that row is
being done (`loading={x.isPending && x.variables ... row}`): with `loading={x.isPending}` every row's
button spins at once, as the merge dialog's once did."""
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def buttons(text):
    """Each <Button ...> opening tag whole (its props may hold arrow functions and braces)."""
    for m in re.finditer(r"<Button\b", text):
        depth, i = 0, m.end()
        while i < len(text):
            c = text[i]
            if c == "{":
                depth += 1
            elif c == "}":
                depth -= 1
            elif c == ">" and depth == 0 and text[i - 1] != "=":
                yield text[m.start():i + 1], text.count("\n", 0, m.start()) + 1
                break
            i += 1


def test_a_rows_button_spins_only_for_its_row():
    found = []
    for p in (ROOT / "web" / "src").rglob("*.tsx"):
        text = p.read_text(encoding="utf-8")
        for tag, line in buttons(text):
            for name, arg in re.findall(r"(\w+)\.mutate\(([^)]*)", tag):
                if arg.strip() and re.search(r"loading=\{\s*" + name + r"\.isPending\s*\}", tag):
                    found.append(f"{p.relative_to(ROOT)}:{line}: {name}.mutate({arg.strip()[:30]}) spins for every row")
    assert not found, "\n".join(found)
