#!/bin/sh
# Run the connector on this machine, against PostgreSQL in Compose, with the
# settings in .env. The passwords are read here, at run time, so `make -n`
# never prints them.
#
#   scripts/host.sh setup | serve | backup --out FILE | restore --in FILE
#   scripts/host.sh test [go test flags]
set -eu
cd "$(dirname "$0")/.."
set -a
. ./.env
set +a
db="127.0.0.1:${GTM_DB_PORT}/gtm?sslmode=disable"
GTM_OWNER_DATABASE_URL="postgres://gtm:${POSTGRES_PASSWORD}@${db}"
GTM_DATABASE_URL="postgres://gtm_app:${GTM_APP_PASSWORD}@${db}"
unset POSTGRES_PASSWORD GTM_APP_PASSWORD
export GTM_OWNER_DATABASE_URL GTM_DATABASE_URL
export GTM_CREDENTIALS_DIR="${GTM_HOME}/credentials"
export GTM_LISTEN="127.0.0.1:${GTM_PORT}"
export GTM_PUBLIC_ORIGIN="https://127.0.0.1:${GTM_PORT}"
export GTM_ALLOWED_HOSTS="127.0.0.1:${GTM_PORT},localhost:${GTM_PORT}"
export GTM_WEB_DIR=web/dist
case "${1:-}" in
  test)
    shift
    # The tests create a database each, so they connect as the owner.
    GTM_TEST_DATABASE_URL="$GTM_OWNER_DATABASE_URL" GTM_REQUIRE_DATABASE=1 exec go test -race -count=1 "$@" ./...
    ;;
  serve)
    # serve needs only the app role.
    unset GTM_OWNER_DATABASE_URL
    exec build/gtm-connector "$@"
    ;;
  *)
    exec build/gtm-connector "$@"
    ;;
esac
