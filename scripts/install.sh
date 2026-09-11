#!/bin/sh
set -eu

REPO=${WEBTERM_REPO:-debbide/termdock}
INSTALL_BIN=/usr/local/bin/webterm
CONFIG_DIR=/etc/webterm
STATE_DIR=/var/lib/webterm
LOG_DIR=/var/log/webterm
SERVICE_FILE=/etc/systemd/system/webterm.service
ENV_FILE=$CONFIG_DIR/environment
PID_FILE=/var/run/webterm.pid
WEBTERM_PORT=${WEBTERM_PORT:-}
WEBTERM_TOKEN=${WEBTERM_TOKEN:-}
ACTION=${1:-}

die() {
  echo "错误: $*" >&2
  exit 1
}

is_interactive() {
  [ -r /dev/tty ] && [ -w /dev/tty ]
}

prompt() {
  prompt_text=$1
  default_value=${2:-}
  if [ -n "$default_value" ]; then
    printf "%s [%s]: " "$prompt_text" "$default_value" >/dev/tty
  else
    printf "%s: " "$prompt_text" >/dev/tty
  fi
  IFS= read -r answer </dev/tty || true
  if [ -n "$answer" ]; then
    printf '%s' "$answer"
  else
    printf '%s' "$default_value"
  fi
}

download() {
  url=$1
  output=$2
  separator='?'
  case "$url" in *\?*) separator='&' ;; esac
  url="${url}${separator}nocache=$(date +%s)"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --connect-timeout 15 \
      -H 'Cache-Control: no-cache' -H 'Pragma: no-cache' \
      "$url" -o "$output"
  elif command -v wget >/dev/null 2>&1; then
    wget -q --no-cache -O "$output" "$url"
  else
    die "需要 curl 或 wget"
  fi
}

stop_existing() {
  if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now webterm.service >/dev/null 2>&1 || true
  fi
  if [ -f "$PID_FILE" ]; then
    old_pid=$(cat "$PID_FILE" 2>/dev/null || true)
    if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
      kill "$old_pid" 2>/dev/null || true
    fi
  fi
  pkill -TERM -f '^/usr/local/bin/webterm( |$)' 2>/dev/null || true
}

uninstall() {
  echo "正在卸载 TermDock..."
  stop_existing
  rm -f "$SERVICE_FILE" "$PID_FILE" "$INSTALL_BIN"
  rm -rf "$CONFIG_DIR" "$STATE_DIR" "$LOG_DIR" /opt/webterm
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    systemctl reset-failed webterm.service >/dev/null 2>&1 || true
  fi
  userdel webterm >/dev/null 2>&1 || true
  groupdel webterm >/dev/null 2>&1 || true
  echo "TermDock 已卸载完成。"
}

if [ "$(id -u)" -ne 0 ]; then
  die "安装和卸载必须以 root 身份运行"
fi

if [ -z "$ACTION" ]; then
  if is_interactive; then
    ACTION=$(prompt "请选择操作: install 或 uninstall" "install")
  else
    ACTION=install
  fi
fi

case "$ACTION" in
  install) ;;
  uninstall|remove)
    uninstall
    exit 0
    ;;
  *) die "用法: $0 [install|uninstall]" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "不支持的架构: $(uname -m)" ;;
esac

if [ -z "$WEBTERM_PORT" ]; then
  if is_interactive; then
    WEBTERM_PORT=$(prompt "监听端口" "7681")
  else
    WEBTERM_PORT=7681
  fi
fi
case "$WEBTERM_PORT" in
  ''|*[!0-9]*) die "WEBTERM_PORT 必须是数字" ;;
esac
if [ "$WEBTERM_PORT" -lt 1 ] || [ "$WEBTERM_PORT" -gt 65535 ]; then
  die "WEBTERM_PORT 必须在 1 到 65535 之间"
fi

if [ -z "$WEBTERM_TOKEN" ] && is_interactive; then
  WEBTERM_TOKEN=$(prompt "访问 Token，留空则自动生成" "")
fi
if [ -z "$WEBTERM_TOKEN" ]; then
  if command -v openssl >/dev/null 2>&1; then
    WEBTERM_TOKEN=$(openssl rand -hex 24)
  else
    WEBTERM_TOKEN=$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')
  fi
fi
case "$WEBTERM_TOKEN" in
  *'
'*) die "WEBTERM_TOKEN 必须是单行内容" ;;
esac

DOWNLOAD_DIR=$(mktemp -d)
trap 'rm -rf "$DOWNLOAD_DIR"' EXIT HUP INT TERM
RELEASE_BASE_URL="https://github.com/$REPO/releases/latest/download"

echo "正在下载 TermDock ($ARCH)..."
download "$RELEASE_BASE_URL/webterm-linux-$ARCH" "$DOWNLOAD_DIR/webterm"
download "$RELEASE_BASE_URL/SHA256SUMS" "$DOWNLOAD_DIR/SHA256SUMS"
expected=$(awk -v file="webterm-linux-$ARCH" '$2 == file { print $1 }' "$DOWNLOAD_DIR/SHA256SUMS")
[ -n "$expected" ] || die "校验文件缺少 webterm-linux-$ARCH"
actual=$(sha256sum "$DOWNLOAD_DIR/webterm" | awk '{ print $1 }')
[ "$actual" = "$expected" ] || die "安装包校验失败"
chmod 0755 "$DOWNLOAD_DIR/webterm"
VERSION=$("$DOWNLOAD_DIR/webterm" version 2>/dev/null || true)
[ -n "$VERSION" ] || die "无法读取安装包版本号"

stop_existing
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
printf 'WEBTERM_ACCESS_TOKEN=%s\n' "$WEBTERM_TOKEN" >"$ENV_FILE"
chown root:webterm "$CONFIG_DIR/config.json" "$ENV_FILE"
chmod 0640 "$CONFIG_DIR/config.json" "$ENV_FILE"

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
EnvironmentFile=$ENV_FILE
ExecStart=$INSTALL_BIN --config $CONFIG_DIR/config.json
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$STATE_DIR $LOG_DIR

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now webterm.service
  systemctl restart webterm.service
  START_MODE=systemd
  STATUS_HINT="systemctl status webterm"
else
  rm -f "$SERVICE_FILE"
  WEBTERM_ACCESS_TOKEN=$WEBTERM_TOKEN nohup "$INSTALL_BIN" --config "$CONFIG_DIR/config.json" >>"$LOG_DIR/webterm.log" 2>&1 &
  echo $! >"$PID_FILE"
  chmod 0644 "$PID_FILE"
  START_MODE=standalone
  STATUS_HINT="tail -n 30 $LOG_DIR/webterm.log"
fi

echo "TermDock 安装完成"
echo "版本: $VERSION"
echo "模式: $START_MODE"
echo "地址: http://127.0.0.1:$WEBTERM_PORT"
echo "Token: $WEBTERM_TOKEN"
echo "状态: $STATUS_HINT"
