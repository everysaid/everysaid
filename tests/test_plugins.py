"""How a plugin runs a script: its output as a terminal shows it."""
import pytest

BAR = r'''
import sys, time
print("start", flush=True)
for p in (10, 50, 100):
    sys.stdout.write(f"\r[{p:3}%] copying")
    sys.stdout.flush()
    time.sleep(0.35)
print(flush=True)
print("done", flush=True)
sys.stdout.write("\r[ 99%] moving")      # drawn, then a long wait: its last state is still shown
sys.stdout.flush()
time.sleep(0.6)
print("\r[100%] moving", flush=True)    # a bar ended by \n stays the bar, not a line of the panel
'''

FAIL = r'''
import sys
print("trying")
sys.exit("the phone is locked")
'''


class Ctx:
    def __init__(self):
        self.lines, self.drawn, self.bars = [], [], []

    def log(self, text, redrawn=False, **kw):
        (self.bars if redrawn else self.lines).append(text)

    def progress(self, line):
        self.drawn.append(line)


def test_a_progress_bar_is_one_line_and_a_failure_says_why(tmp_path, monkeypatch):
    from chronika.plugins import sources
    monkeypatch.setattr(sources, "SCRIPTS", str(tmp_path))
    (tmp_path / "bar.py").write_text(BAR)
    (tmp_path / "fail.py").write_text(FAIL)
    ctx = Ctx()
    sources.run_script(ctx, "bar.py")
    assert ctx.lines == ["start", "done"]
    assert ctx.bars == ["[100%] copying", "[100%] moving"]           # each bar once, as it ended
    assert "[ 99%] moving" in ctx.drawn                              # and drawn as it went
    with pytest.raises(RuntimeError, match="the phone is locked"):
        sources.run_script(Ctx(), "fail.py")
