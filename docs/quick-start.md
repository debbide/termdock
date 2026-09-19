# Quick Start

Build and run locally without a tunnel:

```sh
make build
./bin/webterm --config configs/config.example.json
```

To use a Cloudflare Quick Tunnel, set `cloudflare.mode` to `quick` and ensure the configured `cloudflared` binary exists. The process prints the public URL and one-time token to standard output.

## 会话有效期

`terminal` 下的三个时长选项控制会话生命周期：

- `idle_timeout`：PTY 空闲多久后关闭，`"0"` 表示不限制。
- `max_lifetime`：PTY 会话和登录 cookie 的最长有效期。设为 `"24h"` 时，会话最多存活 24 小时，cookie 同样 24 小时后失效；设为 `"0"` 时，PTY 会话永不过期，登录 cookie 则回退为 30 天有效期。
- `session_retention`：浏览器断开后，PTY 会话在服务端保留多久以便重连。

终端输入在前端按 32KB 分片发送，因此大段粘贴不会触发服务端的单条消息大小限制（`security.max_message_size`，默认 64KB）。

The repository-hosted installer downloads the matching amd64 or arm64 binary from a published release and installs `cloudflared` automatically:

```sh
curl -fsSL https://raw.githubusercontent.com/debbide/termdock/main/scripts/install.sh | sudo sh
```
