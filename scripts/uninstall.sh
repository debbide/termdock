#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "uninstall.sh must run as root" >&2
  exit 1
fi

systemctl disable --now webterm.service >/dev/null 2>&1 || true
rm -f /etc/systemd/system/webterm.service /usr/local/bin/webterm
if [ "${REMOVE_CLOUDFLARED:-0}" = "1" ]; then
  rm -f /usr/local/bin/cloudflared
fi
systemctl daemon-reload

if [ "${PURGE:-0}" = "1" ]; then
  rm -rf /etc/webterm /var/lib/webterm /var/log/webterm
  userdel webterm >/dev/null 2>&1 || true
  groupdel webterm >/dev/null 2>&1 || true
fi

echo "TermDock uninstalled. Set PURGE=1 to remove configuration and data, or REMOVE_CLOUDFLARED=1 to remove the managed cloudflared binary."
