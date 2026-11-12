#!/usr/bin/env bash
# One-time server setup for sqlp_serv: the systemd unit, the nginx site and
# the TLS certificate. Safe to run again: it skips what is already in place.
# Afterwards, deploy/redeploy.sh builds and starts the service.
set -euo pipefail

domain=sqlp.dskrzypiec.dev
here="$(cd "$(dirname "$0")" && pwd)"

echo "== systemd unit"
sudo install -m 0644 "$here/sqlp_serv.service" /etc/systemd/system/sqlp_serv.service
sudo systemctl daemon-reload
sudo systemctl enable sqlp_serv

echo "== nginx site"
# certbot edits the installed copy, so it is only installed once.
if [[ ! -e /etc/nginx/sites-available/$domain ]]; then
  sudo install -m 0644 "$here/nginx/$domain" "/etc/nginx/sites-available/$domain"
fi
if [[ ! -e /etc/nginx/sites-enabled/$domain ]]; then
  sudo ln -s "/etc/nginx/sites-available/$domain" "/etc/nginx/sites-enabled/$domain"
fi
sudo nginx -t
sudo systemctl reload nginx

echo "== TLS certificate"
if [[ ! -d /etc/letsencrypt/live/$domain ]]; then
  sudo certbot --nginx -d "$domain" --redirect --non-interactive
else
  echo "already issued"
fi
