#!/bin/sh
# Write .env for this checkout once: a random database password, this user's
# IDs (the container runs as this user), where the credentials and backups
# folder lives, and the two loopback ports. .env is ignored by Git.
set -eu
cd "$(dirname "$0")/.."
[ -f .env ] && exit 0
umask 077
password="$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
cat > .env <<EOF
POSTGRES_PASSWORD=$password
GTM_UID=$(id -u)
GTM_GID=$(id -g)
GTM_HOME=$HOME/.config/gtm-connector
GTM_PORT=8443
GTM_DB_PORT=55432
EOF
echo "wrote .env"
