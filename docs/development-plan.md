# WebTerm CF 开发计划

## 一、项目目标

用户在服务器执行一条安装命令：

```bash
curl -fsSL https://example.com/install.sh | sudo bash
```

安装完成后启动本地 Web Terminal，并输出浏览器访问地址和一次性访问令牌。浏览器认证后通过 HTTPS 和 WebSocket 操作服务器本地 PTY，整个过程不依赖 SSH 或 sshd。

第一版采用 Go 后端、xterm.js 前端和 cloudflared 管理器，优先支持 Debian 12/13 与 Ubuntu 22.04/24.04。

## 二、设计原则

1. 后端直接创建 PTY 并启动本地 Shell，不依赖 SSH。
2. 默认禁止匿名访问，不默认启动 root Shell。
3. Go 后端嵌入前端静态资源，实现单文件部署。
4. 服务默认仅监听 `127.0.0.1`。
5. 同时支持 Quick Tunnel 和 Token Tunnel。
6. 默认创建并使用 `webterm` 专用系统用户。
7. 临时模式到期后关闭终端、隧道和全部子进程。
8. 第一版支持 Debian/Ubuntu，后续扩展 Alpine、Rocky Linux 等。

## 三、总体架构

```text
Browser
  |
  | HTTPS + WebSocket
  v
Cloudflare Tunnel
  |
  | HTTP + WebSocket
  v
127.0.0.1:7681
  |
  v
Go Web Server
  |-- Authentication
  |-- Session Manager
  |-- WebSocket Handler
  |-- Audit Events
  |-- Tunnel Manager
  |
  v
PTY Process
  |
  v
/bin/bash、/bin/sh 或配置的命令
```

后端负责 HTTP 服务、前端资源、认证、WebSocket 生命周期、PTY 输入输出和缩放、会话限制、超时、cloudflared 启停与监控、配置、日志及运行状态。

前端负责令牌兑换、终端显示、键盘输入、剪贴板、窗口尺寸同步、连接状态、重连提示与退出操作。

## 四、技术栈

### 后端

- Go，最低版本根据受支持发行版和依赖兼容性确定。
- PTY：`github.com/creack/pty`。
- HTTP：标准库 `net/http`。
- WebSocket：选择维护活跃、API 简洁并支持消息大小限制的 Go 库。
- 配置：优先 JSON 或标准库可覆盖的格式，必要时引入轻量 YAML 依赖。
- 日志：`log/slog`。
- 随机令牌：`crypto/rand`。
- 密码散列：Argon2id 或 bcrypt。
- 前端嵌入：`go:embed`。

### 前端

- TypeScript。
- xterm.js。
- xterm-addon-fit。
- xterm-addon-web-links。
- Vite。
- 原生 CSS，不引入大型 UI 框架。

### 隧道

- Quick Tunnel：`cloudflared tunnel --url http://127.0.0.1:7681`。
- Fixed Tunnel：`cloudflared tunnel run --token ...`，实际 Token 从权限受控文件读取。
- cloudflared 由后端作为受控子进程运行。
- 从结构化日志或标准输出识别 Quick Tunnel URL。

## 五、项目目录

```text
ssh-cf/
├── cmd/webterm/main.go
├── internal/
│   ├── app/
│   ├── auth/
│   ├── config/
│   ├── server/
│   ├── session/
│   ├── terminal/
│   ├── tunnel/
│   ├── security/
│   └── logging/
├── web/
│   ├── src/
│   ├── public/
│   ├── package.json
│   ├── tsconfig.json
│   └── vite.config.ts
├── scripts/
│   ├── install.sh
│   ├── uninstall.sh
│   └── release.sh
├── packaging/systemd/webterm.service
├── configs/config.example.yaml
├── docs/
│   ├── architecture.md
│   ├── security.md
│   ├── quick-start.md
│   └── fixed-tunnel.md
├── go.mod
├── Makefile
├── README.md
└── LICENSE
```

项目工作名称为 WebTerm CF，后续可改名为 `webterm-cf`、`tunnelterm`、`cloudterm` 或 `termflare`。

## 六、后端模块

### 1. 配置模块

配置优先级：命令行参数、环境变量、配置文件、内置安全默认值。

```yaml
server:
  listen: 127.0.0.1:7681
  public_url: ""

terminal:
  shell: /bin/bash
  user: webterm
  working_directory: /home/webterm
  max_sessions: 1
  idle_timeout: 15m
  max_lifetime: 1h

security:
  auth_mode: one-time-token
  trusted_origins: []
  cookie_secure: true
  login_rate_limit: 5

cloudflare:
  mode: quick
  binary: /usr/local/bin/cloudflared
  token_file: /etc/webterm/cloudflare-token
```

敏感信息不写入普通配置。固定隧道 Token 保存在权限为 `0600` 的独立文件中。

### 2. PTY 模块

- 根据配置选择 `/bin/bash`、`/bin/sh` 或用户默认 Shell。
- 创建 PTY 和子进程。
- 设置 `TERM=xterm-256color`。
- 转发浏览器输入和 Shell 输出。
- 处理终端窗口大小变化。
- 连接关闭时终止整个进程树。
- 限制最大会话数量。
- 第一版每个已认证连接创建独立 PTY，断开后销毁。

### 3. WebSocket 协议

控制消息使用 JSON，终端输入输出使用二进制帧：

```json
{"type":"resize","cols":120,"rows":35}
```

```json
{"type":"ping"}
```

连接流程：认证并取得 Cookie、连接 `/api/terminal/ws`、校验 Cookie 与 Origin、检查并发及有效期、创建 PTY、同步尺寸、双向传输、关闭或超时后清理。

### 4. 隧道管理器

统一接口：

```text
Start(context) -> PublicURL
Stop(context)
Status() -> TunnelStatus
```

Quick Tunnel 无需 Cloudflare 账号，适合临时维护；服务退出或到期时同步关闭。

Fixed Tunnel 从 Token 文件读取凭据，支持受控重启和健康检测，并兼容 Cloudflare Access。固定 Token 不得出现在日志、状态接口或 Web 页面中。

## 七、认证设计

### 临时模式

1. 启动时生成 32 字节安全随机令牌。
2. 服务仅保存令牌摘要。
3. 明文令牌只输出一次。
4. 用户访问 `/auth?token=...`。
5. 验证后签发短期 HttpOnly Cookie。
6. 原令牌立即失效。
7. 重定向到不含 Token 的 `/terminal`。

Cookie 使用 `HttpOnly`、`Secure`、`SameSite=Strict` 和短有效期，服务重启时更换签名密钥。

### 固定模式

优先级为 Cloudflare Access、本地管理员账号密码、长期 API Token。固定模式不长期使用 URL 查询参数认证。

## 八、权限模型

安装脚本创建 `webterm` 专用用户。默认无 root 权限、禁止直接登录、具有独立 Home，服务和 Shell 均以该用户运行。

完整服务器管理必须由管理员显式配置受限 sudo、指定管理命令或 root 模式。root 模式只能通过类似 `webterm start --allow-root` 的明显参数开启，并输出安全警告。

## 九、前端页面

### 登录页

- 项目名称和主机标识。
- 密码输入或令牌验证状态。
- 安全连接提示。
- 模糊登录错误，不泄露内部状态。

### 终端页

- 全屏 xterm.js。
- 自动 Fit。
- 连接、断开及重连状态。
- 当前用户与主机名。
- 会话剩余时间。
- 主动退出按钮。
- 移动端 Ctrl、Alt、Tab、Esc 和方向键工具栏。

第一版不加入文件管理、命令模板或监控面板。

## 十、HTTP 接口

```text
GET  /healthz
GET  /readyz
GET  /api/status
POST /api/auth/token
POST /api/auth/login
POST /api/auth/logout
GET  /api/terminal/ws
POST /api/session/terminate
GET  /terminal
```

`/healthz` 只表示进程存活，不泄露系统、用户、路径、版本或隧道凭据。

## 十一、安全要求

- 默认禁止匿名访问。
- 默认仅监听 `127.0.0.1`。
- 使用安全随机一次性令牌，服务端只保存摘要。
- 使用 HttpOnly、Secure Cookie。
- 校验 WebSocket Origin。
- 实施登录限速和失败延迟。
- 限制最大连接数。
- 实施空闲超时和绝对超时。
- 限制请求体和 WebSocket 消息大小。
- 可靠清理 PTY 子进程和进程树。
- 隧道停止时终止已有会话。
- 敏感参数不写入日志。
- 检查配置文件和 Token 文件权限。
- 默认不记录终端输入输出。
- 默认运行低权限 Shell。
- 防范认证绕过、WebSocket 劫持、令牌泄露、孤儿 Shell、路径穿越和参数注入。

## 十二、安装流程

1. 检测 `amd64` 或 `arm64` 架构。
2. 检测 Debian/Ubuntu 版本。
3. 下载 WebTerm 二进制及校验和。
4. 检测或下载 cloudflared。
5. 校验 SHA-256。
6. 创建 `webterm` 系统用户和目录。
7. 写入 `/etc/webterm/config.yaml`。
8. 安装 systemd 服务。
9. 启动服务。
10. 通过本地管理命令取得访问地址和一次性 Token。

文件布局：

```text
/usr/local/bin/webterm
/usr/local/bin/cloudflared
/etc/webterm/config.yaml
/etc/webterm/cloudflare-token
/var/lib/webterm/
/var/log/webterm/
/etc/systemd/system/webterm.service
```

固定 Tunnel Token 不通过命令行参数传递。

## 十三、命令行设计

```text
webterm start
webterm stop
webterm status
webterm url
webterm token create
webterm token revoke
webterm config check
webterm tunnel quick
webterm tunnel fixed --token-file /path/to/token
webterm version
```

典型用法：

```bash
sudo webterm start --tunnel quick --expires 1h
sudo webterm start --tunnel fixed --token-file /etc/webterm/cloudflare-token
```

## 十四、systemd 设计

- 崩溃后自动重启。
- 使用私有临时目录。
- 启用文件系统保护。
- 限制 Linux capabilities。
- 限制可写路径。
- 明确运行用户。
- 优雅停止 cloudflared 和 PTY。
- 低权限主服务不动态切换到任意系统用户。
- 管理操作通过本地管理命令或受控 Unix Socket 完成。

## 十五、开发阶段

### 阶段 1：可运行 MVP

- 初始化 Go 项目。
- 创建 PTY。
- 建立 WebSocket 双向传输。
- 接入 xterm.js。
- 支持窗口缩放。
- 连接结束时清理进程。
- 将前端嵌入 Go 二进制。

验收：Bash、vim、top 可正常交互；Ctrl+C、Tab、方向键正常；浏览器尺寸变化可同步到 PTY。

### 阶段 2：安全认证

- 一次性令牌。
- 会话 Cookie。
- Origin 校验。
- 登录限速。
- 空闲和绝对超时。
- 最大并发数。
- 登出和会话终止。

验收：未经认证无法访问 WebSocket，一次性令牌兑换后立即失效。

### 阶段 3：Cloudflare Quick Tunnel

- 启动 cloudflared。
- 提取公网 URL。
- 展示一次性访问链接。
- 监控 cloudflared 退出。
- 主服务退出时清理隧道。

验收：全新 Debian/Ubuntu 安装后，一条命令输出可访问链接。

### 阶段 4：固定隧道

- Token 文件配置。
- 固定 Tunnel 进程管理。
- 重连和健康状态。
- systemd 集成。
- Cloudflare Access 兼容说明。

### 阶段 5：安装和发布

- 一键安装和卸载脚本。
- amd64、arm64 构建。
- SHA-256 校验文件。
- GitHub Release 或自建发布源。
- 版本升级和回滚机制。
- Debian/Ubuntu 干净系统验证。

### 阶段 6：增强功能

核心安全稳定后再开发 tmux 恢复、多标签、只读共享、管理员断开、文件传输、审计导出、OIDC、Alpine 和 Rocky Linux 支持。

## 十六、测试计划

### 单元测试

- 配置优先级。
- Token 生成和摘要验证。
- Cookie 签名与过期。
- 会话超时。
- 并发限制。
- cloudflared 日志解析。
- Shell 和参数校验。

### 集成测试

- WebSocket 输入输出。
- PTY 尺寸变化。
- 连接关闭后的进程清理。
- 一次性 Token 兑换。
- 隧道异常退出后的状态变化。
- 服务停止后的隧道清理。

### 安全与平台测试

- 未认证 WebSocket 请求。
- 非法 Origin。
- 一次性 Token 重放。
- 暴力登录。
- 超大 WebSocket 消息。
- Shell 参数注入。
- Ubuntu 22.04 amd64。
- Ubuntu 24.04 amd64。
- systemd 和主机重启。
- 无 SSH 环境。
- `/bin/bash` 不存在时回退 `/bin/sh`。

## 十七、发布标准

- 全新 Debian/Ubuntu 可一键安装。
- 不依赖 sshd。
- 默认非 root Shell。
- 未认证用户无法连接终端。
- Quick Tunnel 地址可以自动输出。
- Fixed Tunnel 可以通过 Token 文件运行。
- 服务退出后不残留 Shell 或 cloudflared 进程。
- amd64 和 arm64 均提供签名或校验和。
- README 明确说明安全边界。

## 十八、推荐实施顺序

1. 项目骨架与配置。
2. PTY 生命周期。
3. WebSocket 数据通道和尺寸同步。
4. 前端终端及静态资源嵌入。
5. 一次性认证、Cookie、Origin 和会话限制。
6. Quick Tunnel 生命周期。
7. Fixed Tunnel 和 systemd。
8. 安装、卸载及发布流水线。

优先解决 PTY 生命周期、WebSocket 安全和进程权限，不先开发复杂安装器或控制面板。

最终产品定位：一条命令启动、无需 SSH、默认低权限、带一次性认证，同时支持 Cloudflare 临时和固定隧道的浏览器终端。
