#!/bin/sh
# After `make up`.
#
#   scripts/smoke.sh        the server answers over TLS with this install's certificate, refuses a
#                           foreign Host, serves the web app from the binary alone, signs in the demo
#                           principal, and runs the commit it was built from
#   scripts/smoke.sh demo   publish the demo cards, as the app role inside the app container
#   scripts/smoke.sh card   print acc-003's newest card, ID and SHA-256, after checking the bytes the API
#                           gives against both digests it states
set -eu
cd "$(dirname "$0")/.."
. ./.env
# The same choice the Makefile makes: the Compose plugin, or the standalone docker-compose.
if [ -n "${COMPOSE:-}" ]; then
  compose="$COMPOSE"
elif docker compose version >/dev/null 2>&1; then
  compose="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
  compose="docker-compose"
else
  echo "Docker Compose was not found (docker compose or docker-compose)." >&2
  exit 1
fi
creds="$GTM_HOME/credentials"
base="https://127.0.0.1:$GTM_PORT"
jar="$(mktemp)"
body="$(mktemp)"
trap 'rm -f "$jar" "$body"' EXIT
fetch() { curl -sS --fail-with-body --cacert "$creds/tls.pem" -b "$jar" -c "$jar" "$@"; }
# sha256sum where there is one, and shasum on a Mac without it.
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
# The API writes compact JSON with its members in a fixed order, so sed reads the few values checked here.

sign_in() {
  csrf="$(fetch "$base/api/v1/session" | sed -n 's/.*"csrf_token":"\([^"]*\)".*/\1/p')"
  test -n "$csrf"
  fetch -o /dev/null -X POST -H "Origin: $base" -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' \
    --data-binary "@$creds/demo-credentials.json" "$base/api/v1/session"
}

case "${1:-}" in
  demo)
    $compose exec -T app /app/gtm-connector demo
    exit
    ;;
  card)
    sign_in
    card="$(fetch "$base/api/v1/accounts/acc-003/cards" |
      sed -n 's/^{"account":"acc-003","cards":\[{"id":"\([^"]*\)",[^}]*"sha256":"\([0-9a-f]\{64\}\)".*/\1 \2/p')"
    test -n "$card"
    id="${card% *}"
    sha="${card#* }"
    stated="$(fetch -D - -o "$body" "$base/api/v1/cards/$id/bytes" | tr -d '\r' | sed -n 's/^[Xx]-[Cc]ontent-[Ss][Hh][Aa]256: //p')"
    actual="$(sha256 "$body" | cut -d ' ' -f 1)"
    test "$actual" = "$sha" && test "$actual" = "$stated"
    echo "acc-003 $id $sha"
    exit
    ;;
  '') ;;
  *)
    echo "usage: $0 [demo|card]" >&2
    exit 2
    ;;
esac

$compose exec -T app /app/gtm-connector health
test "$(curl -sS --cacert "$creds/tls.pem" -o /dev/null -w '%{http_code}' -H "Host: evil.example:$GTM_PORT" "$base/healthz")" = 421
# The image holds the binary and no web folder; a deep link gets the app's page, whose script is served.
test "$($compose exec -T app ls /app)" = gtm-connector
asset="$(fetch "$base/ops/evidence" | grep -o '/assets/[^"]*\.js' | head -n 1)"
test "$(fetch -o /dev/null -w '%{content_type}' "$base$asset")" = 'text/javascript; charset=utf-8'
sign_in
session="$(fetch "$base/api/v1/session")"
echo "$session" | grep -q '^{"signed_in":true,"principal":{[^}]*"demo":true}'
echo "signed in as $(echo "$session" | sed -n 's/.*"display_name":"\([^"]*\)".*/\1/p')"
test "$($compose exec -T app id -u)" != 0
test "$(docker image inspect -f '{{.Config.User}}' gtm-connector:local)" = 65532:65532
# The image and its binary name the commit they were built from.
revision="$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' gtm-connector:local)"
expected="$(git rev-parse HEAD)$(git status --porcelain | grep -q . && echo -modified || true)"
test "$revision" = "$expected"
$compose exec -T app /app/gtm-connector version | grep -qF "gtm-connector $revision "
echo "smoke check passed at $revision"
