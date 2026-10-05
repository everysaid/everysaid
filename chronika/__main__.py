"""`python -m chronika`: the `chronika` command (see `chronika/cli.py`)."""
import os

from .cli import main

os.umask(0o077)
main()
