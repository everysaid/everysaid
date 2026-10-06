"""`python -m everysaid`: the `everysaid` command (see `everysaid/cli.py`)."""
import os

from .cli import main

os.umask(0o077)
main()
