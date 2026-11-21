#!/bin/sh
# The release files, in build/release: the connector with the web app's build
# inside it, for macOS and Linux on arm64 and amd64, and SHA256SUMS.
# `make release` builds the web app, then runs both of these.
#
#   scripts/release.sh         build the four binaries and write SHA256SUMS
#   scripts/release.sh check   the folder holds those five files and no other, every sum matches,
#                              each binary is built for its platform without C, stamped with the
#                              commit and holds the web app's build, and the one for this machine runs
#
# GTM_REVISION is the commit to stamp, as in the image build. No C is
# compiled, so a Linux binary is static; a macOS binary always loads the
# system library. The checkout's path and Git state stay out of the binary,
# so one commit gives the same bytes wherever it is built.
set -eu
cd "$(dirname "$0")/.."
: "${GTM_REVISION:?set GTM_REVISION to the commit, as make release does}"
export GOTOOLCHAIN=local CGO_ENABLED=0
out=build/release
names="gtm-connector-darwin-amd64 gtm-connector-darwin-arm64 gtm-connector-linux-amd64 gtm-connector-linux-arm64"

# sha256sum where there is one, and shasum on a Mac without it. Both write and check the same lines.
sum() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

fail() {
  echo "release: $*" >&2
  exit 1
}

case "${1:-}" in
  '')
    test -f web/dist/index.html || fail "web/dist holds no build of the web app; make release builds it first"
    rm -rf "$out"
    mkdir -p "$out"
    for name in $names; do
      platform="${name#gtm-connector-}"
      GOOS="${platform%-*}" GOARCH="${platform#*-}" go build -trimpath -buildvcs=false \
        -ldflags="-s -w -X main.revision=$GTM_REVISION" -o "$out/$name" ./cmd/gtm-connector
    done
    # $names is unquoted on purpose: one argument for each file.
    (cd "$out" && sum $names > SHA256SUMS)
    ;;
  check)
    test "$(ls -A "$out" | LC_ALL=C sort | tr '\n' ' ')" = "SHA256SUMS $names " ||
      fail "$out does not hold exactly SHA256SUMS and $names"
    test "$(sed 's/^[0-9a-f]\{64\}  //' "$out/SHA256SUMS" | tr '\n' ' ')" = "$names " ||
      fail "SHA256SUMS does not list exactly $names, each with a SHA-256"
    (cd "$out" && sum -c SHA256SUMS >/dev/null) || fail "a file does not match its line in SHA256SUMS"
    for name in $names; do
      platform="${name#gtm-connector-}"
      built="$(go version -m "$out/$name")"
      for setting in "GOOS=${platform%-*}" "GOARCH=${platform#*-}" CGO_ENABLED=0 -trimpath=true; do
        echo "$built" | grep -q "^	build	$setting\$" || fail "$name was not built with $setting"
      done
      grep -qaF "$GTM_REVISION" "$out/$name" || fail "$name is not stamped with $GTM_REVISION"
      # Go keeps an embedded file's bytes as they are, so a binary that holds
      # this build of the web app holds the name of each of its assets.
      for asset in web/dist/assets/*; do
        grep -qaF "$(basename "$asset")" "$out/$name" || fail "$name does not hold the web app's $asset"
      done
    done
    host="$out/gtm-connector-$(go env GOHOSTOS)-$(go env GOHOSTARCH)"
    test -x "$host" || fail "no release binary runs on this machine"
    printed="$("$host" version)"
    test "$printed" = "gtm-connector $GTM_REVISION $(go env GOVERSION)" || fail "$host version printed: $printed"
    echo "release: $out holds four binaries of $GTM_REVISION and their SHA256SUMS"
    ;;
  *)
    echo "usage: $0 [check]" >&2
    exit 2
    ;;
esac
