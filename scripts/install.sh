#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "install.sh must run as root" >&2
  exit 1
fi

SOURCE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
INSTALL_BIN=${INSTALL_BIN:-/usr/local/bin/webterm}
CLOUDFLARED_BIN=${CLOUDFLARED_BIN:-/usr/local/bin/cloudflared}
CONFIG_DIR=${CONFIG_DIR:-/etc/webterm}
STATE_DIR=${STATE_DIR:-/var/lib/webterm}
LOG_DIR=${LOG_DIR:-/var/log/webterm}
SERVICE_FILE=${SERVICE_FILE:-/etc/systemd/system/webterm.service}
RELEASE_BASE_URL=${RELEASE_BASE_URL:-}
DOWNLOAD_DIR=$(mktemp -d)
trap 'rm -rf "$DOWNLOAD_DIR"' EXIT HUP INT TERM

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [ -r /etc/os-release ]; then
  . /etc/os-release
  case "${ID:-}" in
    debian|ubuntu) ;;
    *) echo "unsupported distribution: ${ID:-unknown}; Debian or Ubuntu is required" >&2; exit 1 ;;
  esac
else
  echo "cannot detect Linux distribution" >&2
  exit 1
fi

download() {
  url=$1
  destination=$2
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --connect-timeout 15 "$url" -o "$destination"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$destination" "$url"
  else
    echo "curl or wget is required" >&2
    exit 1
  fi
}

if ! getent group webterm >/dev/null 2>&1; then
  groupadd --system webterm
fi
if ! id webterm >/dev/null 2>&1; then
  useradd --system --gid webterm --home-dir "$STATE_DIR" --create-home --shell /usr/sbin/nologin webterm
fi

install -d -m 0750 -o root -g webterm "$CONFIG_DIR"
install -d -m 0750 -o webterm -g webterm "$STATE_DIR" "$LOG_DIR"

if [ -n "${WEBTERM_BINARY:-}" ]; then
  install -m 0755 "$WEBTERM_BINARY" "$INSTALL_BIN"
elif [ -x "$SOURCE_DIR/bin/webterm" ]; then
  install -m 0755 "$SOURCE_DIR/bin/webterm" "$INSTALL_BIN"
elif [ -n "$RELEASE_BASE_URL" ]; then
  download "$RELEASE_BASE_URL/webterm-linux-$ARCH" "$DOWNLOAD_DIR/webterm"
  download "$RELEASE_BASE_URL/SHA256SUMS" "$DOWNLOAD_DIR/SHA256SUMS"
  expected=$(awk -v file="webterm-linux-$ARCH" '$2 == file { print $1 }' "$DOWNLOAD_DIR/SHA256SUMS")
  if [ -z "$expected" ]; then
    echo "release checksum for webterm-linux-$ARCH is missing" >&2
    exit 1
  fi
  actual=$(sha256sum "$DOWNLOAD_DIR/webterm" | awk '{ print $1 }')
  if [ "$actual" != "$expected" ]; then
    echo "WebTerm checksum verification failed" >&2
    exit 1
  fi
  install -m 0755 "$DOWNLOAD_DIR/webterm" "$INSTALL_BIN"
else
  echo "set WEBTERM_BINARY, build bin/webterm, or set RELEASE_BASE_URL" >&2
  exit 1
fi

if [ -n "${CLOUDFLARED_BINARY:-}" ]; then
  install -m 0755 "$CLOUDFLARED_BINARY" "$CLOUDFLARED_BIN"
elif ! command -v cloudflared >/dev/null 2>&1 && [ ! -x "$CLOUDFLARED_BIN" ]; then
  download "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-$ARCH" "$DOWNLOAD_DIR/cloudflared"
  install -m 0755 "$DOWNLOAD_DIR/cloudflared" "$CLOUDFLARED_BIN"
fi

if [ ! -f "$CONFIG_DIR/config.json" ]; then
  install -m 0640 -o root -g webterm "$SOURCE_DIR/configs/config.example.json" "$CONFIG_DIR/config.json"
fi
install -m 0644 "$SOURCE_DIR/packaging/systemd/webterm.service" "$SERVICE_FILE"

systemctl daemon-reload
systemctl enable webterm.service
systemctl restart webterm.service
echo "TermDock installed and started. Check status with: systemctl status webterm"
