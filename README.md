# sb-control

`sb-control` 是一个面向多台 Linux 代理服务器的集中管理控制面。项目使用一个 Go 二进制提供两种运行角色：

- `master`：保存期望配置、提供 Web 控制台、审批节点并下发任务。
- `agent`：运行在受控节点上，上报状态并执行经过校验的系统变更。

项目不直接接收任意 sing-box JSON 或远程 Shell 命令。控制台中的 Listener、Endpoint、出站和路由规则会先在 master 端编译为受控配置，再由 agent 校验、应用并在失败时尝试回滚。

## 当前能力

- Vue 3 + Element Plus PC 管理控制台，构建产物嵌入 Go 二进制。
- 管理员密码、可选的 TOTP 两步验证、首次登录强制改密、会话、CSRF 和 `admin` / `operator` / `viewer` 三类角色。
- 一次性节点注册凭据、节点公钥审批、吊销、在线状态和自动重连。
- sing-box 入站、用户凭据、TLS、Reality、传输层和路由规则管理。
- 全局直连、SOCKS5、HTTP 出站管理。
- 客户端订阅分享链接，以及可下载的 Mihomo YAML 与分流策略。
- sing-box 配置自动应用，并从官方 GitHub Release 获取最新稳定版、校验摘要后签名安装或升级。
- 同一服务器出现兼容的 TCP 端口重复时，自动生成并发布基于连接域名的端口分配配置。
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

### 发布与运行环境

- Go、Node.js 和 npm 只在 GitHub Actions 的发布任务中使用。
- master 和 agent 安装服务器不需要 Go、Node.js、npm 或源码。
- GitHub Release 当前提供 Linux `amd64` 和 `arm64` 成品包。
- agent 的系统管理功能面向 Linux 和 systemd。

### 受控节点

agent 应以 `root` 身份运行。具体功能还依赖以下本机命令：

| 功能 | 所需命令或服务 |
| --- | --- |
| sing-box 配置发布 | `sing-box`、`systemctl` |
| sing-box 安装或升级 | `systemctl`，并允许访问 GitHub API 与官方 Release 下载地址 |
| 自动 TCP 端口分配 | 首次需要时由 agent 通过 `apt-get`、`dnf`、`yum` 或 `apk` 安装带 stream 功能的 Nginx |
| 防火墙 | `nft` |
| Fail2Ban | `fail2ban-client`、`fail2ban.service` |
| 实时连接 | sing-box Clash API；由受管配置监听 `127.0.0.1:9090` |

sing-box 安装任务目前只接受 `amd64` 和 `arm64`。master 自动查询官方最新稳定版，并使用官方资源摘要校验下载内容。

## 安装概览

### 三种运行模式

程序严格提供三种模式。每种模式都支持 YAML 文件启动和纯命令行参数启动。

#### 1. 只运行 Master

使用文件：

```bash
sb-control master serve --config /etc/sb-control/master.yaml
```

使用命令行：

```bash
sb-control master serve \
  --data-dir /var/lib/sb-control-master \
  --database-path /var/lib/sb-control-master/sb-control.db \
  --agent-port 8443 \
  --web-port 8080
```

#### 2. 只运行 Agent

使用文件：

```bash
sb-control agent serve --config /etc/sb-control/agent.yaml
```

使用命令行：

```bash
sb-control agent serve \
  --data-dir /var/lib/sb-control-agent \
  --master control.example.com:8443 \
  --master-pubkey '<MASTER_NOISE_PUBKEY>'
```

#### 3. 一个进程同时运行 Master 和 Agent

文件模式必须同时提供两份相互独立的配置：

```bash
sb-control combined serve \
  --master-config /etc/sb-control/master.yaml \
  --agent-config /etc/sb-control/agent.yaml
```

纯命令行模式：

```bash
sb-control combined serve \
  --master-data-dir /var/lib/sb-control-master \
  --database-path /var/lib/sb-control-master/sb-control.db \
  --agent-port 8443 \
  --web-port 8080 \
  --agent-data-dir /var/lib/sb-control-agent \
  --master 127.0.0.1:8443 \
  --master-pubkey '<MASTER_NOISE_PUBKEY>'
```

Combined 模式只有一个长期运行进程，但不会自动信任本机 Agent。Agent 仍须使用一次性令牌注册、由管理员批准，并在后续连接中使用固定 Noise 公钥验证身份。

Master 配置只包含端口，不配置监听 IP。Agent 名称自动读取操作系统主机名，不需要 `node_name`。配置文件只接受 `.yaml` 或 `.yml`，未知字段会导致启动失败。

配置模板位于 `deploy/sb-control-master.yaml` 和 `deploy/sb-control-agent.yaml`。

`master.yaml`：

```yaml
data_dir: /var/lib/sb-control-master
database_path: /var/lib/sb-control-master/sb-control.db
agent_port: 8443
web_port: 8080
allow_insecure_http: false
```

`agent.yaml`：

先在 Master 服务器执行：

```bash
sudo -u sb-control /usr/local/bin/sb-control master show-pubkey --config /etc/sb-control/master.yaml
```

把输出的单行 Base64 字符串写入 `master_public_key`。这是 Master 的 Noise 公钥，不是 Reality 公钥、TLS 证书或服务器注册令牌。

```yaml
data_dir: /var/lib/sb-control-agent
master_address: 127.0.0.1:8443
master_public_key: <MASTER_NOISE_PUBKEY>
heartbeat_interval: 30s
connections_interval: 2s
```

Combined 首次安装时，先用 `master show-pubkey` 获取公钥并写入 `agent.yaml`，再启动 Combined。此时未注册 Agent 会被拒绝但 Master 控制台保持可用。在控制台生成一次性令牌后执行一次 `agent register --config /etc/sb-control/agent.yaml --token TOKEN`，然后在控制台批准。注册命令执行完成即退出，不是第二个长期服务；批准后 Combined 进程内的 Agent 会按正常认证流程自动重连。

正式安装只使用 GitHub Release 已经编译完成的 Linux 二进制。安装 master 或 agent 时不在目标服务器上执行 `go build`、`npm ci` 或 `npm run build`。

一个发布包中的 `sb-control` 二进制同时包含 master、agent 和已经嵌入的 Web 控制台，不需要下载不同角色的程序。

```text
开发者推送 v* 标签
        ↓
GitHub Actions 构建 Web UI 和 Go 二进制
        ↓
GitHub Release 发布 AMD64 / ARM64 压缩包和 SHA-256
        ↓
master / agent 服务器下载对应压缩包并直接安装
```

完整部署顺序如下：

1. 开发者推送版本标签，由 GitHub Actions 自动编译并创建 Release。
2. master 服务器从 Release 下载与 CPU 架构匹配的成品包。
3. 在控制服务器上直接安装并初始化 master。
4. 为 master 配置 systemd 和 HTTPS 反向代理。
5. 从 master 获取 Noise 公钥，并在控制台生成一次性注册令牌。
6. 每台 agent 服务器从同一个 Release 下载对应架构的成品包。
7. 安装、注册并启动 agent。
8. 在控制台批准节点，确认 agent 在线及任务执行正常。

master 与 agent 可以安装在同一台服务器上，但生产环境通常把 master 独立部署。下文命令以 Ubuntu/Debian 风格的 Linux 为例；其他 systemd 发行版只需要替换软件包管理命令。

## 安装前规划

部署前准备以下信息：

| 名称 | 示例 | 说明 |
| --- | --- | --- |
| master 控制台域名 | `control.example.com` | 管理员通过 HTTPS 访问 |
| master agent 地址 | `control.example.com:8443` | agent 建立 Noise TCP 连接时使用，不带 `http://` 或 `https://` |
| master 数据目录 | `/var/lib/sb-control-master` | 数据库、主密钥和 Noise 私钥必须整体备份 |
| agent 数据目录 | `/var/lib/sb-control-agent` | 保存节点私钥、发布签名公钥和任务幂等结果 |
| 默认管理员用户名 | `sb_admin` | 首次启动时自动创建，首次登录后必须修改初始密码 |

master 服务器的网络要求：

- 对 agent 开放 `8443/TCP`。
- 生产控制台通过反向代理开放 `443/TCP`。
- `8080/TCP` 不应直接向公网放行；使用主机防火墙仅允许本机反向代理或可信管理网访问。
- master 需要访问 GitHub API，以查询官方 sing-box 最新稳定版和校验信息。

agent 服务器的网络要求：

- 能主动连接 master 的 `8443/TCP`。
- 能通过 HTTPS 下载 master 批准的 sing-box 官方发布资源。
- agent 控制连接不需要对公网开放新的入站端口；业务代理端口按后续 Listener 配置开放。

如果使用 UFW，可按实际部署开放 master 端口：

```bash
sudo ufw allow 8443/tcp
sudo ufw allow 443/tcp
```

不要把 `8443` 配置成 HTTP/HTTPS 反向代理。它承载的是 Noise 加密原始 TCP；如需转发，只能使用不终止连接的四层 TCP 转发。

## GitHub 自动构建并发布

仓库中的 `.github/workflows/release.yml` 负责全部编译工作。正式发布应推送 `v*` 标签：

```bash
git tag v0.1.0
git push origin v0.1.0
```

GitHub Actions 会自动：

1. 验证一键安装脚本。
2. 安装前端依赖并构建 Web UI。
3. 运行 Go 测试。
4. 把 Web UI 嵌入 `sb-control` 二进制。
5. 分别编译 Linux AMD64 和 ARM64。
6. 把安装脚本、systemd 服务和配置模板写入成品包。
7. 生成 `.tar.gz` 与 SHA-256 校验文件。
8. 创建或更新对应标签的 GitHub Release。

发布完成后，Releases 页面应出现：

```text
sb-control_0.1.0_linux_amd64.tar.gz
sb-control_0.1.0_linux_amd64.tar.gz.sha256
sb-control_0.1.0_linux_arm64.tar.gz
sb-control_0.1.0_linux_arm64.tar.gz.sha256
```

在 GitHub 的 `Actions` 页面手动运行 `Build release packages` 只生成 Actions Artifacts，不创建正式 Release。用于服务器安装时，推荐推送版本标签并从 Releases 页面下载。

## 一键安装

仓库公开后，可以直接运行根目录的 `install.sh`。脚本只下载 GitHub Release 中已经编译完成的二进制，不会在目标服务器安装 Go、Node.js、npm，也不会编译源码。

Master：

```bash
curl -fsSLo install.sh https://raw.githubusercontent.com/liyuwei007036/sb-control/main/install.sh \
  && sudo bash install.sh master
```

Agent：

```bash
curl -fsSLo install.sh https://raw.githubusercontent.com/liyuwei007036/sb-control/main/install.sh \
  && sudo bash install.sh agent
```

脚本会交互询问 Master 的 `主机:端口`、Noise 公钥和一次性注册令牌。令牌输入不会显示在终端中。也可以先设置 `SB_CONTROL_MASTER_ADDRESS`、`SB_CONTROL_MASTER_PUBKEY` 和 `SB_CONTROL_REGISTRATION_TOKEN`，用于受控的非交互部署。

Combined：

```bash
curl -fsSLo install.sh https://raw.githubusercontent.com/liyuwei007036/sb-control/main/install.sh \
  && sudo bash install.sh combined
```

Combined 会先启动本机 Master。在终端等待令牌输入时，登录控制台创建一次性注册令牌并粘贴；注册请求仍须在控制台确认后才会获得信任。

生产环境默认使用带 `Secure` 属性的登录 Cookie，应先配置 HTTPS 反向代理。仅在可信内网临时测试并且需要直接通过 HTTP 登录时，才显式传入：

```text
--allow-insecure-http
```

不建议把远程脚本直接交给 root 执行时，可以先下载并检查：

```bash
curl -fsSL https://raw.githubusercontent.com/liyuwei007036/sb-control/main/install.sh -o install.sh
less install.sh
sudo bash install.sh master
```

安装指定版本时使用 `--version 0.1.3`。重复运行脚本可更新二进制和 systemd 服务；已有 `/etc/sb-control/*.yaml` 配置与 Agent 身份不会被覆盖。执行 `install.sh --help` 可查看全部参数。

## 从 GitHub Release 下载安装包

以下命令在 master 或 agent 的目标 Linux 服务器执行。它们只下载、校验和解压 GitHub 已经编译好的成品，不会在服务器上编译代码。

### 自动识别 AMD64 或 ARM64

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl

case "$(uname -m)" in
  x86_64|amd64)
    ARCH=amd64
    ;;
  aarch64|arm64)
    ARCH=arm64
    ;;
  *)
    echo "不支持的 CPU 架构：$(uname -m)" >&2
    exit 1
    ;;
esac

VERSION=0.1.0
REPOSITORY=liyuwei007036/sb-control
PACKAGE="sb-control_${VERSION}_linux_${ARCH}"

curl -fLO "https://github.com/${REPOSITORY}/releases/download/v${VERSION}/${PACKAGE}.tar.gz"
curl -fLO "https://github.com/${REPOSITORY}/releases/download/v${VERSION}/${PACKAGE}.tar.gz.sha256"
sha256sum -c "${PACKAGE}.tar.gz.sha256"
tar -xzf "${PACKAGE}.tar.gz"
```

把 `VERSION=0.1.0` 改为 Releases 页面中的实际版本。校验成功后，目录结构应类似：

```text
sb-control_0.1.0_linux_amd64/
├── sb-control
├── install.sh
├── README.md
└── deploy/
    ├── sb-control-master.yaml
    ├── sb-control-agent.yaml
    ├── sb-control-master.service
    ├── sb-control-agent.service
    └── sb-control-combined.service
```

`x86_64` 对应发布包中的 `amd64`，64 位 ARM 对应 `arm64`。当前发布流程不生成 32 位 ARMv7 包。

解压后也可以完全离线地安装包内二进制：

```bash
cd "${PACKAGE}"
sudo ./install.sh master
```

## 安装 master

以下步骤只在 master 控制服务器执行。

### 1. 安装二进制和创建运行用户

进入解压后的发布包目录，然后执行：

```bash
cd "${PACKAGE}"

sudo install -m 0755 ./sb-control /usr/local/bin/sb-control
if ! id sb-control >/dev/null 2>&1; then
  sudo useradd \
    --system \
    --user-group \
    --home-dir /var/lib/sb-control-master \
    --shell /usr/sbin/nologin \
    sb-control
fi
sudo install -d \
  -o sb-control \
  -g sb-control \
  -m 0700 \
  /var/lib/sb-control-master
sudo install -d -o root -g sb-control -m 0750 /etc/sb-control
sudo install -o root -g sb-control -m 0640 deploy/sb-control-master.yaml /etc/sb-control/master.yaml
```

如果发行版的 `nologin` 位于 `/sbin/nologin`，请相应修改 `useradd` 命令。master 不需要 root 权限；默认端口 `8080` 和 `8443` 都高于 1024。

### 2. 首次登录

Master 或 Combined 模式首次启动时，如果数据库中没有管理账户，会自动创建：

- 用户名：`sb_admin`
- 初始密码：`123456`

首次登录后只能进入修改密码页面。新密码至少 12 位；修改完成前，其他管理接口会由服务端拒绝。两步验证默认关闭，可以在“系统设置 → 登录安全”中使用验证器应用扫描二维码启用；启用后，后续登录必须输入动态验证码。

`master init-admin` 和 `master reset-mfa` 仅保留给已有自动化脚本兼容使用，新部署不需要执行。

### 3. 获取并保存 master Noise 公钥

所有 agent 必须预先固定同一个 master 公钥：

```bash
sudo -u sb-control /usr/local/bin/sb-control master show-pubkey \
  --config /etc/sb-control/master.yaml
```

保存输出的 Base64 字符串，后续把它作为 `MASTER_NOISE_PUBKEY` 使用。它是 32 字节 Curve25519 公钥，不是 HTTPS 证书、Reality 公钥或 Cloudflare Token。

不要删除或单独替换 `/var/lib/sb-control-master/master-noise.key.enc`。如果 master Noise 私钥改变，现有 agent 会因公钥固定校验失败而无法连接。

### 4. 创建 master systemd 服务

```bash
sudo tee /etc/systemd/system/sb-control-master.service >/dev/null <<'EOF'
[Unit]
Description=sb-control master
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=sb-control
Group=sb-control
UMask=0077
ExecStart=/usr/local/bin/sb-control master serve --config /etc/sb-control/master.yaml
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now sb-control-master.service
sudo systemctl status sb-control-master.service
```

查看实时日志：

```bash
sudo journalctl -u sb-control-master.service -f
```

### 5. 验证 master

```bash
curl -fsS http://127.0.0.1:8080/api/v1/health
sudo ss -lntp | grep -E ':(8080|8443)\b'
```

健康检查应返回：

```json
{"status":"ok"}
```

如果 `8443` 没有监听，查看 `journalctl` 中是否存在端口占用或数据目录权限错误。

### 6. 为控制台配置 HTTPS

master 的浏览器端口本身是普通 HTTP。Master 配置只填写端口，程序监听所有本机地址；生产环境必须使用防火墙限制 `8080/TCP` 的来源，并由 Nginx、Caddy 或其他反向代理终止 HTTPS。

Nginx 示例：

```nginx
server {
    listen 443 ssl;
    server_name control.example.com;

    ssl_certificate /etc/letsencrypt/live/control.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/control.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }
}
```

默认登录 Cookie 带 `Secure` 属性，因此生产控制台必须通过 HTTPS 访问。只有可信内网临时测试需要直接使用 HTTP 时，才显式添加：

```text
--allow-insecure-http
```

`--allow-insecure-http` 不应在公网使用；YAML 中对应 `allow_insecure_http: true`。

## 安装 agent

以下步骤需要在每台受控 Linux 服务器上分别执行。agent 必须以 root 身份长期运行，因为它需要管理 sing-box、Nginx、nftables、Fail2Ban 和 systemd。

### 1. 检查到 master 的连接

将地址替换成实际 master 地址：

```bash
MASTER_ADDRESS=control.example.com:8443
MASTER_HOST=${MASTER_ADDRESS%:*}
MASTER_PORT=${MASTER_ADDRESS##*:}

getent hosts "$MASTER_HOST"
timeout 5 bash -c "</dev/tcp/${MASTER_HOST}/${MASTER_PORT}"
```

这里只检查 TCP 是否可达。`8443` 不是 HTTPS 服务，因此不要使用 `curl https://...:8443` 进行检测。

### 2. 安装 agent 二进制

按照“获取发布包”一节在 agent 服务器下载对应架构的包，然后执行：

```bash
cd "${PACKAGE}"
sudo install -m 0755 ./sb-control /usr/local/bin/sb-control
sudo install -d -o root -g root -m 0700 /var/lib/sb-control-agent
sudo install -d -o root -g root -m 0700 /etc/sb-control
sudo install -o root -g root -m 0600 deploy/sb-control-agent.yaml /etc/sb-control/agent.yaml
```

master 与 agent 使用同一个二进制；运行角色由后面的 `master` 或 `agent` 子命令决定。

### 3. 在控制台生成一次性注册令牌

1. 使用管理员用户名和密码登录控制台；仅在账户已启用两步验证时输入动态验证码。
2. 打开“服务器”页面。
3. 点击“添加服务器”。
4. 立即复制只显示一次的注册令牌。

令牌默认有效 15 分钟，最长 1 小时，并且只能成功使用一次。令牌过期后重新生成即可，不要把令牌写进 systemd 服务文件。

### 4. 注册 agent

把以下占位值替换为真实值：

```bash
REGISTRATION_TOKEN='<ONE_TIME_TOKEN>'

sudo /usr/local/bin/sb-control agent register \
  --config /etc/sb-control/agent.yaml \
  --token "$REGISTRATION_TOKEN"

unset REGISTRATION_TOKEN
```

正常情况下会显示注册请求处于 `pending`。如果 master 已经批准了该节点公钥，则会直接显示 `approved`。

注册命令会在 `/var/lib/sb-control-agent` 创建节点 Noise 私钥。不要在多台服务器之间复制同一个 agent 数据目录，否则这些服务器会共享身份。

### 5. 在控制台批准节点

回到“服务器”页面，在“等待确认的服务器”区域核对节点名称，然后点击“允许接入”。master 会把该 agent 的公钥固定到批准后的节点记录中。

如果节点名称或来源不符合预期，不要批准；应让令牌过期，删除目标服务器上的 agent 数据目录后重新注册。删除数据目录会永久更换节点身份，只应在确定尚未投入使用时执行。

### 6. 创建 agent systemd 服务

先在 `/etc/sb-control/agent.yaml` 中填写 Master 地址和公钥，然后创建服务：

```bash
sudo tee /etc/systemd/system/sb-control-agent.service >/dev/null <<EOF
[Unit]
Description=sb-control agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
UMask=0077
ExecStart=/usr/local/bin/sb-control agent serve --config /etc/sb-control/agent.yaml
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now sb-control-agent.service
sudo systemctl status sb-control-agent.service
```

Agent 会执行本机 `sing-box version` 自动检测；未检测到版本时，Master 为受支持的 Linux AMD64/ARM64 节点自动创建首次安装任务，并获取官方最新稳定版。

可选参数：

| 参数 | 默认值 | 约束或用途 |
| --- | --- | --- |
| `--heartbeat-interval` | `30s` | 允许 `5s` 到 `5m` |
| `--connections-interval` | `2s` | 允许 `1s` 到 `30s` |

### 7. 验证 agent 和 sing-box

```bash
sudo systemctl status sb-control-agent.service
sudo journalctl -u sb-control-agent.service -n 100 --no-pager
sudo ls -la /var/lib/sb-control-agent
```

随后在控制台确认：

1. 节点显示在线。
2. 操作系统和架构分别显示为 `linux`、`amd64` 或 `arm64`。
3. “任务与审计”中没有持续失败的安装任务。
4. `sing_box_version` 在安装完成后出现实际版本。

节点首次上线且未检测到 sing-box 时，master 会查询官方最新稳定版、核对官方摘要、签署安装清单并下发任务。agent 会再次验证 HTTPS、SHA-256、签名和 CPU 架构，然后安装到 `/usr/local/bin/sing-box`。同一节点不会因重复心跳不断创建首次安装任务；失败后可在“服务器”页面手动点击“安装或升级”。

如果系统没有 `sing-box.service`，首次安装会创建并启用基础 systemd 单元；已有单元不会被覆盖。

### 8. 安装可选系统组件

只使用 sing-box 时不必预先安装全部组件。启用相应管理功能前，再安装对应服务：

| 控制台功能 | agent 服务器要求 |
| --- | --- |
| 自动 TCP 端口分配 | agent 会安装带 stream 功能的 Nginx，并自动加载 `/etc/nginx/stream-conf.d/*.conf`；已有自定义 `stream {}` 时需先在其中加入该目录 |
| nftables 防火墙 | `nft` 命令可用 |
| Fail2Ban | `fail2ban-client` 和 `fail2ban.service` 可用 |
| 实时连接 | 受管 sing-box 配置中的 Clash API，默认仅监听 `127.0.0.1:9090` |

## 升级 master 和 agent

升级前先下载新版本对应架构的发布包并验证 SHA-256。

升级 master：

```bash
sudo systemctl stop sb-control-master.service
sudo cp -a /var/lib/sb-control-master "/var/lib/sb-control-master.backup-$(date +%Y%m%d-%H%M%S)"
sudo install -m 0755 ./sb-control /usr/local/bin/sb-control
sudo systemctl start sb-control-master.service
curl -fsS http://127.0.0.1:8080/api/v1/health
```

升级 agent：

```bash
sudo install -m 0755 ./sb-control /usr/local/bin/sb-control
sudo systemctl restart sb-control-agent.service
sudo systemctl status sb-control-agent.service
```

这里升级的是 `sb-control` 自身。sing-box 的安装和升级由控制台中的“安装或升级”任务处理。

## 安装故障排查

### 控制台登录后仍显示未登录

- 生产环境确认通过 HTTPS 访问。
- 确认反向代理把请求转发到 `127.0.0.1:8080`。
- 只有明文 HTTP 测试环境才使用 `--allow-insecure-http`。
- 如果已启用两步验证，确认浏览器、验证器设备和 master 系统时间准确，否则动态验证码可能失败。

### agent 无法连接 master

- `--master` 必须是 `主机:端口`，不能包含 URL scheme。
- 确认 master 的 `8443/TCP` 已监听并通过防火墙。
- 确认 agent 使用的公钥来自同一个 master 数据目录。
- 不要把 agent 连接发送到 HTTP、HTTPS 或 TLS 终止代理。
- 查看 `journalctl -u sb-control-agent.service` 中的 Noise 握手或连接错误。

### agent 在线但任务提示权限不足

- 确认 systemd unit 使用 `User=root`。
- 确认 `/var/lib/sb-control-agent` 归 root 所有并且权限为 `0700`。
- 根据任务类型确认 `systemctl`、`nft` 或 `fail2ban-client` 已安装；Nginx 会在首次需要自动 TCP 端口分配时安装，安装失败原因会记录在任务结果中。

### sing-box 没有自动安装

- 确认 Agent 能执行本机 `sing-box version`；未安装时检查自动安装任务。
- 确认节点报告的系统为 Linux，架构为 `amd64` 或 `arm64`。
- 确认 master 能访问 GitHub API，agent 能访问官方 Release 下载地址。
- 查看控制台“任务与审计”；首次自动安装失败后不会因每次心跳重复创建，需要手动点击“安装或升级”重试。

## Web 控制台使用流程

推荐按以下顺序配置：

1. 在“节点”中接入并批准服务器。
2. 节点首次上线后会自动安装官方最新稳定版 sing-box；普通 TLS 按需在“系统设置”中导入证书，Reality 密钥与 Short ID 在创建接入服务时自动生成，控制台不提供手动配置入口。
3. 在“出口代理”中按需创建全局 SOCKS5 或 HTTP 出口；不配置时使用内置 `direct`。
4. 在“接入服务”中创建服务并添加用户；每个用户属于该服务所在的服务器，并填写唯一的“客户端节点别名”，保存后自动生成凭据并应用到对应服务器。
5. 在“流量路由”中按需配置直连、拒绝或指定出口规则；变更后自动应用。
6. 在“任务与审计”中确认自动配置任务的执行结果。
7. 在“代理分组”中创建节点分组；分组成员可以是接入用户对应的节点，也可以是其他已保存分组。
8. 在“客户端配置”中引用一个或多个代理分组并手动配置访问规则，生成 Mihomo 更新地址；按需配置防火墙、Fail2Ban 和 Cloudflare。

### 入站协议

当前控制台允许创建以下 sing-box 入站类型：

`anytls`、`http`、`hysteria`、`hysteria2`、`naive`、`shadowsocks`、`shadowtls`、`snell`、`socks`、`trojan`、`tuic`、`vless`、`vmess`。

传输层支持 `HTTP`、`WebSocket`、`HTTPUpgrade`、`gRPC` 和 `QUIC`，但仅可用于后端允许的协议组合。Reality 仅支持 VLESS。

这里表示协议已经进入类型与校验清单，不代表所有 sing-box 版本和参数组合都经过真实节点互通测试。正式发布前仍应使用目标节点上的实际 sing-box 版本执行配置检查和客户端连通性验证。

### 订阅

客户端订阅把多个 Endpoint 生成的分享链接合并后整体 Base64 编码。目前能生成链接的协议为 VLESS、Trojan、Shadowsocks、Hysteria2、SOCKS 和 HTTP。连接主机始终取服务器的“客户端连接地址”，显示名称始终取用户的“客户端节点别名”；缺少连接地址时拒绝生成无效订阅。

### Mihomo YAML

“客户端配置”只生成供客户端下载的 YAML，不改变 sing-box 服务端配置。“代理分组”是独立菜单和独立资源：分组成员是有序列表，可以混合接入用户对应的节点与其他已保存分组，因此多个分组可以组合成新的分组；分组关系必须是无环图。客户端配置本身不创建或复制分组，只保存对已有分组的引用，生成订阅时递归解析引用闭包。被其他分组或客户端配置引用的分组不能删除。

节点名称取接入服务用户的“客户端节点别名”。创建或修改分组时会检查整个递归闭包中的用户状态、服务器连接地址、协议兼容性、节点别名唯一性以及节点别名与分组名称冲突；修改分组不能使已有客户端规则动作失效。分组改名会同步更新客户端规则中的对应动作。

访问规则只负责分流，不再提供任何预定义规则。规则可以使用“表格配置”或“高级纯文本”模式，按 Mihomo 官方规则从上到下匹配；必须由操作者手动配置最后一条 `MATCH`，系统不会补写默认动作或终结规则。客户端配置可以维护远程 HTTP 规则供应商（`rule-providers`），配置行为、格式、规则地址、保存路径、更新间隔和下载代理，并通过 `RULE-SET` 规则引用。创建客户端配置只保存配置，不自动下载；保存后仍可以在列表中手动下载 YAML、复制更新地址或轮换更新地址。

客户端配置使用 Fake-IP 承载代理域名，业务 DNS 返回 `rcode://success`，使代理请求携带原始域名并由最终命中的节点远端解析。规则动作可直接指向节点或代理组：选择组中的 A/B 节点时，代理流量与域名解析随实际节点切换；单独指定 YouTube 等规则走节点 C 时，对应域名也交给 C 远端解析。直连域名及代理服务器地址使用客户端直连 DoH。配置同时启用 TUN、DNS 劫持和严格路由，避免系统普通 DNS 绕过 Mihomo。

用于解析 DoH 服务器域名和代理节点域名的 `default-nameserver`、`proxy-server-nameserver` 仍使用 `https://223.5.5.5/dns-query` 直连引导。代理节点建立前无法通过自身解析自身域名；如果要求引导查询也不暴露客户端公网出口 IP，服务器的“客户端连接地址”必须填写 IP，TLS/Reality 域名只放在 SNI。配置不会写入明文 UDP/TCP DNS 服务器。

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

自动安装 Nginx 时，Agent 会先阻止新安装的服务自动启动；在 Debian/Ubuntu 上移除软件包自带的默认站点，并确认新安装配置没有 HTTP 80 监听后，才会启用实际需要的 TCP 入口。如果无法确认不会暴露额外 HTTP 端口，任务会失败并保持 Nginx 停止。已存在的用户自建 Nginx 不会被自动删除或改写。

- agent 与 master 使用 `Noise_XK` 加密的 TCP 长连接和固定的 Curve25519 身份密钥。
- agent 预先固定 master 公钥；master 在管理员审批后固定 agent 公钥。
- 节点私钥需要更换时，应吊销旧节点并重新注册，不支持原地轮换。
- 管理员密码使用 Argon2id 保存；初始密码必须在首次登录时更换。TOTP 两步验证由每个用户自行扫码启用，启用后登录必须完成动态验证码校验。
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

## 开发者测试

本节只用于修改项目源码后的开发验证，不属于 master 或 agent 安装步骤。安装服务器不需要执行这些命令，也不需要安装 Go、Node.js 或 npm。

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

1. `go test -tags=e2e -count=1 -v ./e2e`：构建真实 `sb-control` 可执行文件，分别启动 master 和 agent 进程，通过真实 HTTP 与 Noise TCP 连接完成管理员认证、节点注册与审批、心跳、实时连接上报、任务下发与回传、自动应用配置、按用户选择出口、两个 VLESS 自动使用 TCP 443、Hysteria2 使用 UDP 443、自动端口分配、防火墙、Fail2Ban、Mihomo YAML 下载、分页和注销闭环。
2. `npm --prefix webui run test:e2e`：重新构建嵌入式前端，使用真实 Chrome 打开管理平台，验证默认账户、首次强制改密、扫码启用两步验证、启用后的动态验证码登录、全部功能入口、全局服务器筛选、任务分页、接入服务多用户表单、Mihomo 配置入口和手机尺寸布局。

进程级 E2E 使用真实 agent 任务执行器和真实临时文件替换。为了不修改测试宿主机的 `/etc` 与 `/usr/local`，测试启动的 agent 会设置 `SB_CONTROL_E2E_ROOT`，并以可记录调用的确定性命令替代 `sing-box` 和 `systemctl`。生产进程未设置该变量时仍使用标准系统路径。目标 Linux 服务器上的真实 systemd、Nginx、nftables、Fail2Ban 和公网客户端连通性属于部署验收，不能由安全的本地 E2E 替代。

浏览器测试默认使用 Chrome。需要选择其他已安装的 Playwright 浏览器通道时，可以设置 `SB_CONTROL_E2E_BROWSER_CHANNEL`。

仓库中的脚本有明确环境边界：

- `scripts_smoke.sh` 使用硬编码的 Raspberry Pi 路径，只适合对应测试机。
- `scripts_verify.sh` 是兼容入口，现已转交给当前 `scripts_e2e.sh`，不再使用旧版 TLS/mTLS 参数。

## 项目结构

```text
.
├── .github/workflows/              # AMD64/ARM64 自动构建与 GitHub Release
├── cmd/sb-control/                 # CLI 入口，master / agent / combined 三种模式
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

- `sb-control combined serve --master-config MASTER.yaml --agent-config AGENT.yaml`
- `sb-control combined serve --master-data-dir DIR --database-path FILE --agent-port PORT --web-port PORT --agent-data-dir DIR --master HOST:PORT --master-pubkey KEY`

```text
sb-control master init-admin ...   # 兼容旧自动化，新部署不需要
sb-control master reset-mfa ...    # 兼容旧自动化，新部署从系统设置管理
sb-control master show-pubkey ...
sb-control master serve ...
sb-control agent register ...
sb-control agent serve ...
sb-control combined serve ...
```

直接运行参数不足的命令时，程序会输出可用的顶级命令。当前 CLI 不包含证书签发、取回证书或 agent HTTP 轮询命令。
