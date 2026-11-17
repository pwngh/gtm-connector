#!/bin/sh
# Optional documentation capture. The normal demo needs none of these tools.
set -eu
cd "$(dirname "$0")/.."
umask 077
for tool in python3 node npx asciinema agg; do
    command -v "$tool" >/dev/null 2>&1 || {
        printf 'Missing recording tool: %s. See docs/recording.md.\n' "$tool" >&2
        exit 1
    }
done
mkdir -p var docs/media
capture_dir=$(mktemp -d var/capture.XXXXXX)
mkdir "$capture_dir/config" "$capture_dir/state"
cat > "$capture_dir/config/config.toml" <<'CONFIG'
[session]
capture_input = false
capture_env = "TERM"
[notifications]
enabled = false
CONFIG
export ASCIINEMA_CONFIG_HOME="$PWD/$capture_dir/config"
export ASCIINEMA_STATE_HOME="$PWD/$capture_dir/state"
export TERM=xterm-256color
printf 'Preparing the optional recording tools and Python environment.\n'
python3 -m venv .venv
.venv/bin/python -m pip install --quiet --disable-pip-version-check -r requirements.lock \
    > "$capture_dir/setup.log" 2>&1
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --version \
    > "$capture_dir/inspector-version.log" 2>&1
printf 'Recording real commands. Long idle periods will be shortened in playback.\n'
asciinema rec --headless --quiet --return --output-format asciicast-v2 \
    --window-size 112x28 --capture-env TERM --idle-time-limit 12 \
    --title 'GTM connector: fixture walkthrough and Inspector CLI' \
    --command '.venv/bin/python -I scripts/recording-session.py' "$capture_dir/session.cast"
.venv/bin/python -I scripts/render-recording.py "$capture_dir/session.cast"
printf 'Recording and Inspector frame saved in docs/media/.\n'
