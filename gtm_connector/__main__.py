import sys

if sys.version_info < (3, 11):
    sys.exit("Python 3.11 or newer is required.")

from .cli import main

raise SystemExit(main())
