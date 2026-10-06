"""Every test runs on a demo archive of invented people, in a folder of its own: the environment
points Chronika's folders there before anything of it is imported, so the user's archive and
settings are never touched."""
import os
import shutil
import tempfile

import pytest

ROOT = tempfile.mkdtemp(prefix="chronika-test-")
for name in ("DATA", "CACHE", "CONFIG", "STATE"):
    os.environ[f"CHRONIKA_{name}"] = os.path.join(ROOT, name.lower())
    os.makedirs(os.environ[f"CHRONIKA_{name}"], exist_ok=True)
os.environ["CHRONIKA_KEYRING"] = "chronika-test"       # never the user's secrets
with open(os.path.join(os.environ["CHRONIKA_CONFIG"], "config.toml"), "w", encoding="utf-8") as f:
    f.write('[owner]\nnumbers = ["+15550000000"]\nregion = "US"\n')

from chronika import demo  # noqa: E402

DEMO = demo.build(7)
PRISTINE = DEMO + ".pristine"
shutil.copy(DEMO, PRISTINE)


@pytest.fixture
def store(tmp_path):
    """A fresh copy of the demo archive for each test."""
    from chronika.core import Store
    path = tmp_path / "archive.db"
    shutil.copy(PRISTINE, path)
    s = Store(str(path))
    yield s
    s.close()


def pytest_sessionfinish(session, exitstatus):
    shutil.rmtree(ROOT, ignore_errors=True)
