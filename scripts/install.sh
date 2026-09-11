#!/bin/sh
set -eu

REPO=debbide/termdock
INSTALL_BIN=/usr/local/bin/webterm
CONFIG_DIR=/etc/webterm
STATE_DIR=/var/lib/webterm
LOG_DIR=/var/log/webterm
SERVICE_FILE=/etc/systemd/system/webterm.service
ENV_FILE=/etc/webterm/environment
PID_FILE=/var/run/webterm.pid
WEBTERM_PORT=${WEBTERM_PORT:-7681}
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

case "$WEBTERM_PORT" in
  ""|*[!0-9]*) echo "WEBTERM_PORT must be a number" >&2; exit 1 ;;
esac
if [ "$WEBTERM_PORT" -lt 1 ] || [ "$WEBTERM_PORT" -gt 65535 ]; then
  echo "WEBTERM_PORT must be between 1 and 65535" >&2
  exit 1
fi

RELEASE_BASE_URL="https://github.com/$REPO/releases/latest/download"

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

cat >"$CONFIG_DIR/config.json" <<EOF
{"server":{"listen":"127.0.0.1:$WEBTERM_PORT"},"terminal":{"shell":"/bin/bash","working_directory":"$STATE_DIR","max_sessions":1,"idle_timeout":"15m","max_lifetime":"1h"},"security":{"trusted_origins":[],"cookie_secure":true,"login_rate_limit":5,"max_message_size":65536},"cloudflare":{"mode":"disabled","binary":"/usr/local/bin/cloudflared","token_file":"$CONFIG_DIR/cloudflare-token"}}
EOF
chown root:webterm "$CONFIG_DIR/config.json"
chmod 0640 "$CONFIG_DIR/config.json"

if [ -n "${WEBTERM_TOKEN:-}" ]; then
  case "$WEBTERM_TOKEN" in
    *"\n"*) echo "WEBTERM_TOKEN must be a single line" >&2; exit 1 ;;
  esac
  printf "WEBTERM_ACCESS_TOKEN=%s\n" "$WEBTERM_TOKEN" >"$ENV_FILE"
else
  : >"$ENV_FILE"
fi
chown root:webterm "$ENV_FILE"
chmod 0640 "$ENV_FILE"

start_standalone() {
  if [ -f "$PID_FILE" ]; then
    old_pid=$(cat "$PID_FILE" 2>/dev/null || true)
    if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
      kill "$old_pid"
    fi
  fi
  if [ -n "${WEBTERM_TOKEN:-}" ]; then
    WEBTERM_ACCESS_TOKEN=$WEBTERM_TOKEN nohup "$INSTALL_BIN" --config "$CONFIG_DIR/config.json" >>"$LOG_DIR/webterm.log" 2>&1 &
  else
    nohup "$INSTALL_BIN" --config "$CONFIG_DIR/config.json" >>"$LOG_DIR/webterm.log" 2>&1 &
  fi
  echo $! >"$PID_FILE"
  chmod 0644 "$PID_FILE"
}

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] && systemctl show-environment >/dev/null 2>&1; then
  cat >"$SERVICE_FILE" <<EOF
[Unit]
Description=TermDock secure web terminal
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=webterm
Group=webterm
EnvironmentFile=-$ENV_FILE
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
  systemctl restart webterm.service
  START_MODE=systemd
else
  start_standalone
  START_MODE=standalone
fi
echo "TermDock installation completed."
echo "Config: $CONFIG_DIR/config.json"
echo "Listen: 127.0.0.1:$WEBTERM_PORT"
if [ -n "${WEBTERM_TOKEN:-}" ]; then
  echo "Access token: configured"
else
  if [ "$START_MODE" = systemd ]; then
    echo "Access token: randomly generated; view it with: journalctl -u webterm -n 30 --no-pager"
  else
    echo "Access token: randomly generated; view it with: tail -n 30 $LOG_DIR/webterm.log"
  fi
fi
if [ "$START_MODE" = systemd ]; then
  echo "Status: systemctl status webterm"
else
  echo "Started without systemd. PID: $(cat "$PID_FILE")"
  echo "Log: $LOG_DIR/webterm.log"
  echo "Stop: kill $(cat "$PID_FILE")"
fi
