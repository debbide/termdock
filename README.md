# TermDock

TermDock 是一个安全、独立的 Linux 网页终端。它内置浏览器客户端，直接创建本地 PTY 会话，不依赖 SSH，并支持令牌认证以及 Cloudflare Quick Tunnel 和固定隧道。

## 构建

```sh
make check
make build
```

本地运行：

```sh
./bin/webterm --config configs/config.example.json
```

更多部署说明请参阅 `docs/quick-start.md`、`docs/fixed-tunnel.md` 和 `docs/security.md`。

## 一键安装

TermDock 提供 AMD64（`x86_64`）和 ARM64（`aarch64`）静态 Linux 二进制文件，支持 Debian 和 Ubuntu：

```sh
curl -fsSL https://raw.githubusercontent.com/debbide/termdock/main/scripts/install.sh | sudo sh
```

安装脚本会自动检测 CPU 架构、校验 SHA-256、创建 `webterm` 系统账号并启动服务。系统存在可用的 systemd 时使用 systemd，否则自动以后台进程运行，日志写入 `/var/log/webterm/webterm.log`。

默认监听 `127.0.0.1:7681`。未指定固定令牌时，程序会生成随机的一次性令牌并写入服务日志。

支持以下环境变量：

- `WEBTERM_PORT`：监听端口，默认值为 `7681`。
- `WEBTERM_TOKEN`：固定访问 Token；未提供时自动生成并在安装完成后明确显示。

直接运行且终端可交互时，脚本只询问端口和 Token。通过管道运行时默认使用端口 `7681` 并自动生成 Token。

使用固定令牌安装：

```sh
curl -fsSL https://raw.githubusercontent.com/debbide/termdock/main/scripts/install.sh | \
  sudo WEBTERM_TOKEN='请替换为安全令牌' sh
```

同时指定令牌和端口：

```sh
curl -fsSL https://raw.githubusercontent.com/debbide/termdock/main/scripts/install.sh | \
  sudo WEBTERM_TOKEN='请替换为安全令牌' WEBTERM_PORT=8080 sh
```

安装完成后脚本会明确输出安装版本、运行模式、端口和 Token。脚本自动探测 systemd；可用时注册系统服务，否则以独立后台进程运行。

常用管理命令：

```sh
sudo systemctl status webterm
sudo systemctl restart webterm
sudo journalctl -u webterm -f
```

## 一键卸载

通过管道执行卸载：

```sh
curl -fsSL https://raw.githubusercontent.com/debbide/termdock/main/scripts/install.sh | sudo sh -s -- uninstall
```

使用本地脚本卸载：

```sh
sudo sh scripts/install.sh uninstall
```

卸载操作会停止 systemd、后台进程及旧版 Supervisor 管理的 TermDock，随后删除：

- `/usr/local/bin/webterm` 和旧版 `/opt/webterm`
- `/etc/webterm`
- `/var/lib/webterm`
- `/var/log/webterm`
- `webterm.service` 和旧版 Supervisor 配置
- `webterm` 系统用户和用户组

卸载会删除配置、令牌、日志及运行目录，请先备份需要保留的数据。

## GitHub Release

推送 `v*` 标签会触发 GitHub Actions，运行检查和测试，构建 AMD64 与 ARM64 二进制文件，生成 `SHA256SUMS` 并发布 Release：

```sh
git tag v1.0.0
git push origin v1.0.0
```

也可以在 GitHub 的 **Actions → Release → Run workflow** 中输入版本标签手动发布。

## 开发

```sh
go run ./cmd/webterm
```

服务默认监听 `127.0.0.1:7681` 并输出一次性令牌。在本地 HTTP 环境开发时，可在权限受限的 JSON 配置文件中将 `security.cookie_secure` 设置为 `false`，然后通过 `--config` 加载。

## 安全边界

- 未认证用户不能访问终端和状态接口。
- 认证使用随机令牌及短期、签名的 HttpOnly Cookie；Cookie 在服务端可撤销，退出登录立即失效。
- 登录接口不按来源地址限速：隧道场景下所有请求都来自 `127.0.0.1`，按地址计数会让任何远程访客锁死真正的使用者。令牌为 256 位随机值，暴力猜测不可行；如需额外保护，建议在隧道前叠加 Cloudflare Access。
- WebSocket 请求必须携带有效 Cookie，且 Origin 必须匹配或已加入信任列表。
- `X-Forwarded-Host` / `X-Forwarded-Proto` 仅在来源为回环地址或 `security.trusted_proxies` 中列出的代理时才被信任，远程客户端无法伪造。默认部署（cloudflared 转发到 `127.0.0.1`）无需额外配置。
- PTY 进程使用服务账号权限运行，断开连接时终止对应进程组。
- 退出登录会同时撤销 Cookie 并结束当前 PTY；仅网络中断时保留 PTY 供重连，保留时长由 `terminal.session_retention` 控制（默认 1 小时）。
- 文件管理接口被限制在 `terminal.working_directory` 之内，越界路径（包括绝对路径和 `..`）一律拒绝；终端本身仍可访问整台主机。
- 默认仅监听回环地址，远程访问前应配置 HTTPS 或 Cloudflare Tunnel。
- 除非明确需要 root 终端权限，否则不要以 root 身份运行服务。

## 配置说明

配置文件为 JSON，字段缺省时使用内置安全默认值；省略 `security.cookie_secure` 不会关闭安全 Cookie，必须显式写 `false` 才会关闭。

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `server.listen` | `127.0.0.1:7681` | 监听地址 |
| `terminal.shell` | `/bin/bash` 或 `/bin/sh` | 必须为绝对路径的可执行文件 |
| `terminal.working_directory` | 当前用户主目录 | 终端工作目录，同时是文件管理的根目录 |
| `terminal.max_sessions` | `1` | 并发 PTY 数量 |
| `terminal.idle_timeout` | `0`（禁用） | 空闲断开时长 |
| `terminal.max_lifetime` | `0`（禁用） | 会话绝对时长，同时决定 Cookie 有效期 |
| `terminal.session_retention` | `1h` | 断开连接后保留 PTY 供重连的时长 |
| `security.trusted_origins` | `[]` | 额外的可信 Origin |
| `security.trusted_proxies` | `[]` | 可信反向代理的 IP 或 CIDR；仅这些来源的转发头被采信，回环地址始终可信 |
| `security.cookie_secure` | `true` | 仅通过 HTTPS 发送 Cookie |
| `security.max_message_size` | `65536` | WebSocket 单条消息上限 |
| `cloudflare.mode` | `disabled` | `disabled`、`quick` 或 `fixed` |

使用固定令牌运行时，令牌来自 `WEBTERM_ACCESS_TOKEN`，程序只提示"令牌来自环境变量"，不会把长期令牌写入日志。

## 第三方组件

本项目在 `LICENSE`（MIT）之外还打包或链接了以下组件，各自的许可证与完整声明见 [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md)：

| 组件 | 版本 | 许可证 |
| --- | --- | --- |
| `@xterm/xterm` | 6.0.0 | MIT |
| `@xterm/addon-fit` | 0.11.0 | MIT |
| `github.com/creack/pty` | v1.1.24 | MIT |
| `github.com/gorilla/websocket` | v1.5.3 | BSD-2-Clause |

`internal/app/static/` 下的 `xterm.js`、`xterm.css`、`xterm-addon-fit.js` 是上游 npm 产物的逐字节副本，版本通过 SHA-256 比对确认。其中 `xterm.js` 和 `xterm-addon-fit.js` 上游以压缩形式发布且不带版权声明，因此本项目在这两个文件头部补上了许可证声明；**更新这两个文件时请保留该声明**，并同步更新 `THIRD-PARTY-NOTICES.md` 中的版本与摘要。
