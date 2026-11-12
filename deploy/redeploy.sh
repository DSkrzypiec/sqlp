#!/usr/bin/env bash
# Rebuilds sqlp_serv from this checkout, installs it (and the systemd unit, when
# deploy/sqlp_serv.service changed) and restarts the service.
#
#   deploy/redeploy.sh               # run the tests first
#   deploy/redeploy.sh --skip-tests
#
# It builds whatever is checked out, including uncommitted changes; the page
# footer and /healthz then show the commit with a "+dirty" suffix.
set -euo pipefail

repo="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo"

if [[ "${1:-}" != "--skip-tests" ]]; then
  echo "== tests"
  go test -short ./...
  # The server is a module of its own (cmd/sqlp_serv/go.mod), which ./... skips.
  go test -C cmd/sqlp_serv -short ./...
fi

echo "== build"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
go build -C cmd/sqlp_serv -trimpath -o "$tmp/sqlp_serv" .

echo "== install and restart"
sudo install -D -m 0755 "$tmp/sqlp_serv" /opt/sqlp_serv/sqlp_serv
# The unit and the binary go together: the unit passes flags only a new
# binary knows. So a changed unit is installed with the binary.
unit=/etc/systemd/system/sqlp_serv.service
if ! cmp -s deploy/sqlp_serv.service "$unit"; then
  echo "installing the changed unit"
  sudo install -m 0644 deploy/sqlp_serv.service "$unit"
  sudo systemctl daemon-reload
fi
sudo systemctl restart sqlp_serv

if health="$(curl -fs --retry 20 --retry-delay 1 --retry-connrefused http://127.0.0.1:9393/healthz)"; then
  echo "sqlp_serv is up: $health (checkout $(git rev-parse --short=7 HEAD))"
else
  echo "sqlp_serv did not come up. Recent logs:" >&2
  sudo journalctl -u sqlp_serv -n 30 --no-pager >&2
  exit 1
fi
