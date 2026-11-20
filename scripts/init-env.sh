#!/bin/sh
# Write .env for this checkout, adding any setting it lacks: random passwords
# for the database's owner role and app role, this user's IDs (the containers
# run as this user), where the credentials and backups folder lives, and the
# two loopback ports. .env is ignored by Git and kept private.
set -eu
cd "$(dirname "$0")/.."
umask 077
touch .env
chmod 600 .env
random() { od -An -N24 -tx1 /dev/urandom | tr -d ' \n'; }
add() {
  grep -q "^$1=" .env || { echo "$1=$2" >> .env && echo "added $1 to .env"; }
}
add POSTGRES_PASSWORD "$(random)"
add GTM_APP_PASSWORD "$(random)"
add GTM_UID "$(id -u)"
add GTM_GID "$(id -g)"
add GTM_HOME "$HOME/.config/gtm-connector"
add GTM_PORT 8443
add GTM_DB_PORT 55432
