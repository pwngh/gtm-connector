#!/bin/sh
# Forward ports published inside a Colima VM to 127.0.0.1 on this machine.
#
# Colima normally forwards published ports itself. When its watcher does not
# (seen with Lima 2.1 and an OpenSSH that rejects the cipher list in Lima's
# generated SSH config), containers are healthy but nothing answers here.
#
#   scripts/colima-forward.sh open  55432 8443     forward the ports
#   scripts/colima-forward.sh close 55432 8443     remove the forwards
#   scripts/colima-forward.sh ensure 55432 8443    forward only ports that do not answer
#
# The forwards ride on Lima's existing SSH connection, so there is no process
# to keep alive, and they go away when the VM stops. Both ends are loopback.
set -eu

action="${1:-}"
case "$action" in
  open | close | ensure) shift ;;
  *)
    echo "usage: $0 open|close|ensure PORT [PORT...]" >&2
    exit 2
    ;;
esac

answers() { nc -z -w 2 127.0.0.1 "$1" >/dev/null 2>&1; }

if [ "$action" = ensure ]; then
  pending=""
  for port in "$@"; do
    answers "$port" || pending="$pending $port"
  done
  [ -z "$pending" ] && exit 0
  case "$(docker info --format '{{.Name}}' 2>/dev/null || true)" in
    *colima*) ;;
    *)
      echo "ports$pending do not answer on 127.0.0.1" >&2
      exit 1
      ;;
  esac
  # shellcheck disable=SC2086
  set -- $pending
  action=open
fi

instance="${LIMA_INSTANCE:-colima}"
config="${LIMA_SSH_CONFIG:-$HOME/.colima/_lima/$instance/ssh.config}"
if [ ! -f "$config" ]; then
  echo "no Lima SSH config at $config: this helper is only for Colima (set LIMA_INSTANCE for a named profile)" >&2
  exit 1
fi
host="$(awk '$1 == "Host" { print $2; exit }' "$config")"

# Given on the command line so it wins over the config file's cipher line.
ciphers="aes128-gcm@openssh.com,aes256-gcm@openssh.com"

status=0
for port in "$@"; do
  case "$port" in
    '' | *[!0-9]*)
      echo "not a port: $port" >&2
      exit 2
      ;;
  esac
  spec="127.0.0.1:$port:127.0.0.1:$port"
  if [ "$action" = open ]; then
    if ssh -F "$config" -o "Ciphers=$ciphers" -O forward -L "$spec" "$host" 2>/dev/null; then
      echo "forwarding 127.0.0.1:$port from the Colima VM"
    else
      echo "could not forward port $port (already forwarded, or in use on this machine)" >&2
      status=1
    fi
  else
    ssh -F "$config" -o "Ciphers=$ciphers" -O cancel -L "$spec" "$host" 2>/dev/null || true
  fi
done
exit $status
