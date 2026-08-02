# sb-control

`sb-control` 是一个面向多台 Linux 代理服务器的集中管理控制面。项目使用一个 Go 二进制提供两种运行角色：

- `master`：保存期望配置、提供 Web 控制台、审批节点并下发任务。
- `agent`：运行在受控节点上，上报状态并执行经过校验的系统变更。

项目不直接接收任意 sing-box JSON 或远程 Shell 命令。控制台中的 Listener、Endpoint、出站和路由规则会先在 master 端编译为受控配置，再由 agent 校验、应用并在失败时尝试回滚。

## 当前能力

- Vue 3 + Element Plus PC 管理控制台，构建产物嵌入 Go 二进制。
- 管理员密码、TOTP 两步验证、会话、CSRF 和 `admin` / `operator` / `viewer` 三类角色。
- 一次性节点注册凭据、节点公钥审批、吊销、在线状态和自动重连。
- sing-box 入站、用户凭据、TLS、Reality、传输层和路由规则管理。
- 全局直连、SOCKS5、HTTP 出站管理。
- 客户端订阅分享链接，以及可下载的 Mihomo YAML 与分流策略。
- sing-box 配置自动应用，并从官方 GitHub Release 获取最新稳定版、校验摘要后签名安装或升级。
- Nginx Stream SNI 端口复用配置发布。
- nftables 防火墙和 Fail2Ban 配置发布。
- Cloudflare DNS 期望状态、发布确认、远端校验和漂移检测。
- 主机累计流量、实时连接、Fail2Ban 状态、任务结果和审计日志。

## 架构

```mermaid
flowchart LR
    Browser["管理员浏览器"] -->|"HTTPS，建议由反向代理终止 TLS"| Proxy["Nginx / Caddy"]
    Proxy -->|"HTTP"| Web["master 浏览器端口<br/>默认 :8080"]
    Agent1["节点 A：agent"] -->|"Noise_XK 加密 TCP"| Control["master agent 端口<br/>默认 :8443"]
    Agent2["节点 B：agent"] -->|"Noise_XK 加密 TCP"| Control
    Web --> Master["master 控制面"]
    Control --> Master
    Master --> DB["SQLite + 加密密钥"]
    Agent1 --> Services1["sing-box / Nginx / nftables / Fail2Ban"]
    Agent2 --> Services2["sing-box / Nginx / nftables / Fail2Ban"]
```

浏览器流量和 agent 流量必须使用不同端口：

| 端口 | 默认值 | 协议 | 用途 |
| --- | --- | --- | --- |
| 浏览器端口 | `:8080` | HTTP | Web 控制台和 JSON API |
| agent 端口 | `:8443` | Noise 加密的原始 TCP | 节点注册、心跳、任务和实时连接 |

agent 端口不是 HTTP、HTTPS 或 TLS 端口，不能放在会终止 TLS 的 HTTP 反向代理后面。如果需要经 Nginx 转发，只能使用 `stream` 模块进行不解密的纯 TCP 转发。

## 技术栈与运行要求

### 构建环境

- Go 1.24 或更高版本。
- master 可运行在 Go 支持的平台上。
- agent 的系统管理功能面向 Linux 和 systemd。

### 受控节点

agent 应以 `root` 身份运行。具体功能还依赖以下本机命令：

| 功能 | 所需命令或服务 |
| --- | --- |
| sing-box 配置发布 | `sing-box`、`systemctl` |
| sing-box 安装或升级 | `systemctl`，并允许访问 GitHub API 与官方 Release 下载地址 |
| Nginx SNI 分流 | `nginx`、`nginx.service` |
| 防火墙 | `nft` |
| Fail2Ban | `fail2ban-client`、`fail2ban.service` |
| 实时连接 | sing-box Clash API；由受管配置监听 `127.0.0.1:9090` |

sing-box 安装任务目前只接受 `amd64` 和 `arm64`。master 自动查询官方最新稳定版，并使用官方资源摘要校验下载内容。

## 快速开始

### 1. 构建

先构建前端（需要 Node.js 22 或更高版本）：

```powershell
Set-Location .\webui
npm ci
npm run build
Set-Location ..
```

Windows PowerShell：

```powershell
go test ./...
go build -o .\bin\sb-control.exe .\cmd\sb-control
```

Linux：

```bash
go test ./...
go build -o ./bin/sb-control ./cmd/sb-control
```

### 2. 初始化管理员

master 不会创建默认账户或默认密码。密码至少需要 12 个字符。

Windows PowerShell：

```powershell
Read-Host -MaskInput "请输入管理员密码" |
  .\bin\sb-control.exe master init-admin `
    --data-dir .\data `
    --email admin@example.com `
    --password-stdin
```

Linux：

```bash
read -rsp "请输入管理员密码: " SB_CONTROL_PASSWORD
printf '%s\n' "$SB_CONTROL_PASSWORD" |
  ./bin/sb-control master init-admin \
    --data-dir ./data \
    --email admin@example.com \
    --password-stdin
unset SB_CONTROL_PASSWORD
```

命令会输出一次 TOTP 密钥。立即将它保存到身份验证器中；之后无法再次查看原值，只能由管理员重置。

### 3. 启动 master

```powershell
.\bin\sb-control.exe master serve `
  --data-dir .\data `
  --agent-listen :8443 `
  --browser-listen :8080
```

生产环境建议只让反向代理访问浏览器端口，并由反向代理提供 HTTPS。默认会话 Cookie 带 `Secure` 属性。

如果仅在可信内网或本机开发环境中直接使用明文 HTTP，必须显式添加：

```powershell
.\bin\sb-control.exe master serve `
  --data-dir .\data `
  --agent-listen :8443 `
  --browser-listen :8080 `
  --insecure-dev-cookies
```

否则浏览器不会通过 HTTP 回传会话 Cookie，表现为登录后仍未认证。`--insecure-dev-cookies` 不应在公网环境使用。

启动后：

- 控制台：`http://<master>:8080/`
- 健康检查：`http://<master>:8080/api/v1/health`

### 4. 查看 master 公钥

所有 agent 都需要预先固定 master 的 Noise 公钥：

```powershell
.\bin\sb-control.exe master show-pubkey --data-dir .\data
```

该命令输出 Base64 编码的 32 字节 Curve25519 公钥。它不是 TLS 证书，也不是 Cloudflare 或 Reality 密钥。

### 5. 注册节点

在控制台的“节点”页面生成一次性注册凭据，然后在目标服务器执行：

```bash
sudo ./sb-control agent register \
  --data-dir /var/lib/sb-control-agent \
  --master master.example.com:8443 \
  --master-pubkey '<MASTER_NOISE_PUBKEY>' \
  --token '<ONE_TIME_TOKEN>' \
  --node-name my-node
```

注册完成后，回到控制台核对节点名称并批准。一次性凭据默认有效 15 分钟，最长可设置为 1 小时，且只能成功使用一次。

### 6. 长期运行 agent

```bash
sudo ./sb-control agent run \
  --data-dir /var/lib/sb-control-agent \
  --master master.example.com:8443 \
  --master-pubkey '<MASTER_NOISE_PUBKEY>' \
  --sing-box-version 1.12.0
```

可选参数：

| 参数 | 默认值 | 约束 |
| --- | --- | --- |
| `--heartbeat-interval` | `30s` | `5s` 到 `5m` |
| `--connections-interval` | `2s` | `1s` 到 `30s` |
| `--sing-box-version` | 空 | 节点当前 sing-box 版本标识 |

`agent run` 会自动等待审批并在连接断开后重连。待审批时无需运行额外轮询命令。

## systemd 部署

仓库提供了 [deploy/sb-control-agent.service](deploy/sb-control-agent.service) 模板。

```bash
sudo install -m 0755 ./sb-control /usr/local/bin/sb-control
sudo install -m 0644 ./deploy/sb-control-agent.service /etc/systemd/system/sb-control-agent.service
sudo editor /etc/systemd/system/sb-control-agent.service
sudo systemctl daemon-reload
sudo systemctl enable --now sb-control-agent.service
sudo systemctl status sb-control-agent.service
```

启用前必须替换模板中的：

- `MASTER_HOST:8443`
- `MASTER_NOISE_PUBKEY`
- `--sing-box-version` 的实际值

首次执行受管 sing-box 配置时，如果系统没有 `sing-box.service`，agent 会创建并启用一个基本 systemd 单元；已有单元不会被覆盖。

## Web 控制台使用流程

推荐按以下顺序配置：

1. 在“节点”中接入并批准服务器。
2. 按需在“系统设置”中导入 TLS 证书或生成 Reality 密钥；在“服务器节点”中可直接安装官方最新稳定版 sing-box。
3. 在“出口代理”中按需创建全局 SOCKS5 或 HTTP 出口；不配置时使用内置 `direct`。
4. 在“入站协议”中创建入站并同时创建首个访问账户；保存后自动应用到对应节点。
5. 在“流量路由”中按需配置直连、拒绝或指定出口规则；变更后自动应用。
6. 在“任务与审计”中确认自动配置任务的执行结果。
7. 按需配置防火墙、Fail2Ban、客户端订阅、Mihomo 分流策略和 Cloudflare。

### 入站协议

当前控制台允许创建以下 sing-box 入站类型：

`anytls`、`http`、`hysteria`、`hysteria2`、`naive`、`shadowsocks`、`shadowtls`、`snell`、`socks`、`trojan`、`tuic`、`vless`、`vmess`。

传输层支持 `HTTP`、`WebSocket`、`HTTPUpgrade`、`gRPC` 和 `QUIC`，但仅可用于后端允许的协议组合。Reality 仅支持 VLESS。

这里表示协议已经进入类型与校验清单，不代表所有 sing-box 版本和参数组合都经过真实节点互通测试。正式发布前仍应使用目标节点上的实际 sing-box 版本执行配置检查和客户端连通性验证。

### 订阅

客户端订阅把多个 Endpoint 生成的分享链接合并后整体 Base64 编码。目前能生成链接的协议为 VLESS、Trojan、Shadowsocks、Hysteria2、SOCKS 和 HTTP。

### Mihomo YAML

“Mihomo 分流策略”只生成供客户端下载的 YAML，不改变 sing-box 服务端配置。生成时选择入站访问账户、填写客户端实际可连接的服务器域名或 IP，并配置直连、代理、拦截域名及代理网段。支持手动选择、自动测速和故障切换三种代理组策略。

### Cloudflare

Cloudflare 集成支持 `A`、`AAAA`、`CNAME` 和 `TXT` 记录。master 保存期望状态，并将最近一次远端观测结果标记为 `synced`、`drift`、`missing` 等状态。

- “同步”只读取远端状态并检测漂移，不会写入 Cloudflare。
- 单条“发布”先返回差异，确认后才创建或更新远端记录。
- 已发布记录的删除需要二次确认。
- 橙云记录必须绑定 Listener，以便校验协议、传输层、端口和 TLS 兼容性。
- Reality 和 UDP Listener 只能使用灰云 DNS。

## 配置发布与文件边界

agent 只处理固定任务类型，不接受任意可执行文件路径或 Shell 文本。

| 功能 | 受管位置或对象 | 应用前检查 |
| --- | --- | --- |
| sing-box 配置 | `/etc/sing-box/config.json` | `sing-box check -c` |
| sing-box 二进制 | `/usr/local/bin/sing-box` | Ed25519 清单签名、HTTPS、SHA-256、版本和架构 |
| Nginx Stream | `/etc/nginx/stream-conf.d/sb-control.conf` | `nginx -t` |
| 防火墙 | `table inet sb_control` | `nft -c -f` |
| Fail2Ban jail | `/etc/fail2ban/jail.d/sb-control.local` | `fail2ban-client -t` |
| Fail2Ban filter | `/etc/fail2ban/filter.d/sb-control-*.conf` | 文件名和内容边界校验 |

sing-box、Nginx、nftables 和 Fail2Ban 发布都保留或读取上一个状态，并在应用失败时尝试恢复。任务结果会记录为 `succeeded`、`failed` 或 `rolled_back`。

Nginx SNI 分流要求系统的主 Nginx 配置在 `stream {}` 中包含：

```nginx
include /etc/nginx/stream-conf.d/*.conf;
```

防火墙发布只管理 `inet sb_control` 表，不会主动修改其他 nftables 表。Fail2Ban 发布只管理 `sb-control-` 命名空间。

## 安全模型

- agent 与 master 使用 `Noise_XK` 加密的 TCP 长连接和固定的 Curve25519 身份密钥。
- agent 预先固定 master 公钥；master 在管理员审批后固定 agent 公钥。
- 节点私钥需要更换时，应吊销旧节点并重新注册，不支持原地轮换。
- 管理员密码使用 Argon2id 保存，登录必须完成 TOTP。
- 写请求要求会话 Cookie 和 CSRF Token；Cookie 使用 `HttpOnly`、`SameSite=Strict`，生产默认启用 `Secure`。
- Endpoint 凭据、TLS 私钥、Reality 私钥和 Cloudflare Token 使用 master key 加密后写入 SQLite。
- API 列表不会回传 Endpoint 密码、TLS 私钥或完整 Cloudflare Token。
- 任务在 master 中先持久化；离线 agent 重连后会收到未完成任务。
- agent 按任务 ID、类型和内容哈希保存完成结果，重复下发不会重复执行副作用。
- sing-box 安装清单由 master 的 Ed25519 密钥签名，agent 还会校验 HTTPS URL、SHA-256 和 CPU 架构。

## 数据目录与备份

master 数据目录包含：

| 路径 | 内容 |
| --- | --- |
| `sb-control.db` | SQLite 控制面数据 |
| `master.key` | 加密数据库中敏感字段的主密钥 |
| `master-noise.key.enc` | 加密保存的 master Noise 私钥 |
| `release-manifest.key.pem` | sing-box 发布清单签名私钥 |

agent 数据目录包含：

| 路径 | 内容 |
| --- | --- |
| `agent-noise.key` | 节点 Noise 私钥 |
| `release-manifest.pub` | master 的发布签名公钥 |
| `completed-tasks/` | 已执行任务的幂等结果 |

备份或迁移 master 时必须把整个数据目录作为一个整体处理。只有 `sb-control.db` 而没有原 `master.key` 时，已加密凭据无法恢复；更换 Noise 私钥也会使现有 agent 固定的 master 公钥失效。

## 可观测性边界

- 节点累计收发字节来自 `/proc/net/dev` 的非回环网卡总和，是主机级累计值，不是单个代理协议的精确流量。
- 实时连接来自 sing-box Clash API，最多上报 1000 条当前连接。
- 浏览器通过经过认证的 Server-Sent Events 接收所有节点的连接快照。
- 不可观测的数据保持为空，不会根据其他指标推算。

## 测试

单元与进程内集成测试：

```bash
go test ./...
```

这些测试覆盖 Noise 握手与消息分帧、注册审批、一次性凭据、角色约束、TLS/Reality 配置编译、任务签名与防篡改、出站编译、Fail2Ban、Cloudflare、实时连接和配置发布流程，但不能代替端到端测试。

### 端到端测试

Windows：

```powershell
.\scripts_e2e.ps1
```

Linux/macOS：

```bash
bash ./scripts_e2e.sh
```

统一入口会依次运行两层测试：

1. `go test -tags=e2e -count=1 -v ./e2e`：构建真实 `sb-control` 可执行文件，分别启动 master 和 agent 进程，通过真实 HTTP 与 Noise TCP 连接完成管理员 MFA、节点注册与审批、心跳、实时连接上报、任务下发与回传、自动应用配置、按用户选择出口、两个 VLESS 共用 TCP 443、Hysteria2 使用 UDP 443、Nginx 端口共享、防火墙、Fail2Ban、Mihomo YAML 下载、分页和注销闭环。
2. `npm --prefix webui run test:e2e`：重新构建嵌入式前端，使用真实 Chrome 打开管理平台，完成登录、MFA、全部功能入口、全局服务器筛选、任务分页、接入服务多用户表单、Mihomo 配置入口和手机尺寸布局检查。

进程级 E2E 使用真实 agent 任务执行器和真实临时文件替换。为了不修改测试宿主机的 `/etc` 与 `/usr/local`，测试启动的 agent 会设置 `SB_CONTROL_E2E_ROOT`，并以可记录调用的确定性命令替代 `sing-box` 和 `systemctl`。生产进程未设置该变量时仍使用标准系统路径。目标 Linux 服务器上的真实 systemd、Nginx、nftables、Fail2Ban 和公网客户端连通性属于部署验收，不能由安全的本地 E2E 替代。

浏览器测试默认使用 Chrome。需要选择其他已安装的 Playwright 浏览器通道时，可以设置 `SB_CONTROL_E2E_BROWSER_CHANNEL`。

仓库中的脚本有明确环境边界：

- `scripts_smoke.sh` 使用硬编码的 Raspberry Pi 路径，只适合对应测试机。
- `scripts_verify.sh` 是兼容入口，现已转交给当前 `scripts_e2e.sh`，不再使用旧版 TLS/mTLS 参数。

## 项目结构

```text
.
├── cmd/sb-control/                 # CLI 入口，master / agent 两种角色
├── deploy/                         # systemd 服务模板
├── e2e/                            # 真实 master/agent 进程级黑盒测试
├── internal/agent/                 # 节点身份、指标采集和任务执行
├── internal/control/               # 数据库、Web API、配置编译与控制会话
│   └── web/dist/                   # 嵌入 Go 二进制的前端构建产物
├── internal/security/              # 密码、TOTP、Token 和对称加密
├── internal/wire/                  # Noise 连接、分帧和二进制消息
├── webui/                           # Vue 前端源码和 Playwright 浏览器 E2E
├── go.mod
└── sb-control-详细方案设计.md       # 设计背景；与现状冲突时以源码和本 README 为准
```

## 命令索引

```text
sb-control master init-admin ...
sb-control master show-pubkey ...
sb-control master serve ...
sb-control agent register ...
sb-control agent run ...
```

直接运行参数不足的命令时，程序会输出可用的顶级命令。当前 CLI 不包含证书签发、取回证书或 agent HTTP 轮询命令。
