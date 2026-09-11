#!/bin/sh
set -eu

REPO=${WEBTERM_REPO:-debbide/termdock}
INSTALL_BIN=${INSTALL_BIN:-/usr/local/bin/webterm}
CLOUDFLARED_BIN=${CLOUDFLARED_BIN:-/usr/local/bin/cloudflared}
CONFIG_DIR=${CONFIG_DIR:-/etc/webterm}
STATE_DIR=${STATE_DIR:-/var/lib/webterm}
LOG_DIR=${LOG_DIR:-/var/log/webterm}
SERVICE_FILE=${SERVICE_FILE:-/etc/systemd/system/webterm.service}
WEBTERM_VERSION=${WEBTERM_VERSION:-latest}
WEBTERM_INSTALL_CLOUDFLARED=${WEBTERM_INSTALL_CLOUDFLARED:-ask}
WEBTERM_START_SERVICE=${WEBTERM_START_SERVICE:-yes}
DOWNLOAD_DIR=$(mktemp -d)
trap 'rm -rf "$DOWNLOAD_DIR"' EXIT HUP INT TERM

if [ "$(id -u)" -ne 0 ]; then
  echo "This installer must run as root. Try: curl ... | sudo sh" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [ ! -r /etc/os-release ]; then
  echo "Cannot detect Linux distribution" >&2
  exit 1
fi
. /etc/os-release
case "${ID:-}" in
  debian|ubuntu) ;;
  *) echo "Unsupported distribution: ${ID:-unknown}. Debian or Ubuntu is required." >&2; exit 1 ;;
esac

download() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --connect-timeout 15 "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    echo "curl or wget is required" >&2
    exit 1
  fi
}

ask_yes_no() {
  prompt=$1
  default=$2
  if [ ! -t 0 ]; then
    [ "$default" = yes ]
    return
  fi
  if [ "$default" = yes ]; then suffix="[Y/n]"; else suffix="[y/N]"; fi
  printf "%s %s " "$prompt" "$suffix" >/dev/tty
  read answer </dev/tty || answer=
  case "$answer" in
    y|Y|yes|YES) return 0 ;;
    n|N|no|NO) return 1 ;;
    "") [ "$default" = yes ]; return ;;
    *) return 1 ;;
  esac
}

case "$WEBTERM_VERSION" in
  latest) RELEASE_BASE_URL="https://github.com/$REPO/releases/latest/download" ;;
  v*) RELEASE_BASE_URL="https://github.com/$REPO/releases/download/$WEBTERM_VERSION" ;;
  *) RELEASE_BASE_URL="https://github.com/$REPO/releases/download/v$WEBTERM_VERSION" ;;
esac
RELEASE_BASE_URL=${RELEASE_BASE_URL_OVERRIDE:-$RELEASE_BASE_URL}

echo "Installing TermDock ($ARCH) from $RELEASE_BASE_URL"
download "$RELEASE_BASE_URL/webterm-linux-$ARCH" "$DOWNLOAD_DIR/webterm"
download "$RELEASE_BASE_URL/SHA256SUMS" "$DOWNLOAD_DIR/SHA256SUMS"
expected=$(awk -v file="webterm-linux-$ARCH" '$2 == file { print $1 }' "$DOWNLOAD_DIR/SHA256SUMS")
[ -n "$expected" ] || { echo "Checksum entry is missing for webterm-linux-$ARCH" >&2; exit 1; }
actual=$(sha256sum "$DOWNLOAD_DIR/webterm" | awk '{ print $1 }')
[ "$actual" = "$expected" ] || { echo "TermDock checksum verification failed" >&2; exit 1; }

if ! getent group webterm >/dev/null 2>&1; then groupadd --system webterm; fi
if ! id webterm >/dev/null 2>&1; then
  useradd --system --gid webterm --home-dir "$STATE_DIR" --create-home --shell /usr/sbin/nologin webterm
fi
install -d -m 0750 -o root -g webterm "$CONFIG_DIR"
install -d -m 0750 -o webterm -g webterm "$STATE_DIR" "$LOG_DIR"
install -m 0755 "$DOWNLOAD_DIR/webterm" "$INSTALL_BIN"

install_cloudflared=no
case "$WEBTERM_INSTALL_CLOUDFLARED" in
  yes|true|1) install_cloudflared=yes ;;
  no|false|0) ;;
  ask) if ! command -v cloudflared >/dev/null 2>&1 && [ ! -x "$CLOUDFLARED_BIN" ] && ask_yes_no "Install cloudflared?" yes; then install_cloudflared=yes; fi ;;
  *) echo "WEBTERM_INSTALL_CLOUDFLARED must be ask, yes, or no" >&2; exit 1 ;;
esac
if [ "$install_cloudflared" = yes ]; then
  download "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-$ARCH" "$DOWNLOAD_DIR/cloudflared"
  install -m 0755 "$DOWNLOAD_DIR/cloudflared" "$CLOUDFLARED_BIN"
fi

if [ ! -f "$CONFIG_DIR/config.json" ]; then
  cat >"$CONFIG_DIR/config.json" <<EOF
{"server":{"listen":"127.0.0.1:7681"},"terminal":{"shell":"/bin/bash","working_directory":"$STATE_DIR","max_sessions":1,"idle_timeout":"15m","max_lifetime":"1h"},"security":{"trusted_origins":[],"cookie_secure":true,"login_rate_limit":5,"max_message_size":65536},"cloudflare":{"mode":"disabled","binary":"$CLOUDFLARED_BIN","token_file":"$CONFIG_DIR/cloudflare-token"}}
EOF
  chown root:webterm "$CONFIG_DIR/config.json"
  chmod 0640 "$CONFIG_DIR/config.json"
fi

cat >"$SERVICE_FILE" <<EOF
[Unit]
Description=TermDock secure web terminal
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=webterm
Group=webterm
ExecStart=$INSTALL_BIN --config $CONFIG_DIR/config.json
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
ReadWritePaths=$STATE_DIR $LOG_DIR
CapabilityBoundingSet=
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable webterm.service
case "$WEBTERM_START_SERVICE" in
  yes|true|1) systemctl restart webterm.service ;;
  no|false|0) ;;
  *) echo "WEBTERM_START_SERVICE must be yes or no" >&2; exit 1 ;;
esac
echo "TermDock installation completed."
echo "Config: $CONFIG_DIR/config.json"
echo "Status: systemctl status webterm"
