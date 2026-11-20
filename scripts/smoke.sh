#!/bin/sh
# After `make up`: the server answers over TLS with this install's
# certificate, refuses a foreign Host, and signs in the demo principal.
set -eu
cd "$(dirname "$0")/.."
. ./.env
compose="${COMPOSE:-docker compose}"
creds="$GTM_HOME/credentials"
base="https://127.0.0.1:$GTM_PORT"
jar="$(mktemp)"
trap 'rm -f "$jar"' EXIT
fetch() { curl -sS --cacert "$creds/tls.pem" -b "$jar" -c "$jar" "$@"; }

$compose exec -T app /app/gtm-connector health
test "$(fetch -o /dev/null -w '%{http_code}' -H "Host: evil.example:$GTM_PORT" "$base/healthz")" = 421
csrf="$(fetch "$base/api/v1/session" | python3 -c 'import json,sys; print(json.load(sys.stdin)["csrf_token"])')"
login="$(python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))))' "$creds/demo-credentials.json")"
status="$(fetch -o /dev/null -w '%{http_code}' -X POST -H "Origin: $base" -H "X-CSRF-Token: $csrf" \
  -H 'Content-Type: application/json' --data "$login" "$base/api/v1/session")"
test "$status" = 200
fetch "$base/api/v1/session" | python3 -c '
import json, sys
session = json.load(sys.stdin)
assert session["signed_in"] and session["principal"]["demo"], session
print("signed in as", session["principal"]["display_name"])'
test "$($compose exec -T app id -u)" != 0
test "$(docker image inspect -f '{{.Config.User}}' gtm-connector:local)" = 65532:65532
echo "smoke check passed"
