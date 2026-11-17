#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
python3 -m venv .venv
.venv/bin/python -m pip install --quiet --disable-pip-version-check -r requirements.lock
.venv/bin/python -m unittest discover -s tests -v
.venv/bin/python -m gtm_connector demo "$@"
