# Polaris 安装手册

本手册只覆盖安装、初始化、验证与卸载。控制台的日常使用流程见 [README.md](README.md)。

适用范围：Linux + systemd，CPU 架构 `amd64` 或 `arm64`。发布包只提供这两种架构的 Linux 二进制。

---

## 1. 安装前必读

### 1.1 三种运行模式

| 模式 | 进程内容 | 运行用户 | 典型场景 |
| --- | --- | --- | --- |
| `master` | 控制面 + Web 控制台 | `polaris` | 独立控制服务器 |
| `agent` | 受控节点代理 | `root` | 每台被管理的代理服务器 |
| `combined` | 同一进程内同时运行 master 与本机 agent | `root` | 单机自用，控制面和代理在同一台机器 |

同一台机器只能启用其中一个 systemd 服务。`install.sh` 会在启动前禁用另外两个。

Combined 模式不会自动信任本机 agent：本机 agent 同样需要一次性令牌注册、管理员批准，并按固定 Noise 公钥校验身份。

### 1.2 两个端口的性质完全不同

| 端口 | 默认值 | 协议 | 用途 |
| --- | --- | --- | --- |
| Web 端口 | `19670/TCP` | 明文 HTTP | 控制台与 JSON API，前置反向代理终止 HTTPS |
| Agent 端口 | `19994/TCP` | Noise 加密的原始 TCP | 节点注册、心跳、任务下发、实时连接 |

`19994` **不是** HTTPS。不能放在会终止 TLS 的 HTTP 反向代理后面，也不能用 `curl https://host:19994` 测试。如需转发，只能用不解包的四层 TCP 转发。

两个端口不允许相同，否则 master 启动时直接报错。

### 1.3 部署前需要确定的信息

| 名称 | 示例 | 说明 |
| --- | --- | --- |
| 控制台域名 | `control.example.com` | 管理员通过 HTTPS 访问 |
| Master agent 地址 | `control.example.com:19994` | agent 连接用，`主机:端口`，不带 `http://` |
| Master Noise 公钥 | 44 字符 Base64 | 安装 master 后由 `master show-pubkey` 输出 |
| 一次性注册令牌 | 控制台生成 | 默认 15 分钟有效，最长 1 小时，只能成功使用一次 |

### 1.4 网络与防火墙

Master 服务器：

- 对所有 agent 开放 `19994/TCP`。
- 控制台经反向代理开放 `443/TCP`。
- `19670/TCP` 不要直接暴露公网，只允许本机反向代理或可信管理网访问。
- 需要访问 GitHub API（查询 sing-box 官方最新稳定版及校验信息、检查 Polaris 自身更新）。

Agent 服务器：

- 能主动出站连接 master 的 `19994/TCP`。
- 能通过 HTTPS 下载 sing-box 官方 Release 资源。
- agent 控制连接本身不需要新开入站端口；业务代理端口按控制台中配置的接入服务另行开放。

UFW 示例（在 master 上执行）：

```bash
sudo ufw allow 19994/tcp
sudo ufw allow 443/tcp
```

### 1.5 受控节点的系统依赖

agent 必须以 `root` 长期运行：它代表 master 写入 `/etc/sing-box`、`/etc/nginx/stream-conf.d`、`/etc/fail2ban`，并通过 `systemctl` 重启服务。

按需安装，不必一次装齐：

| 功能 | 依赖命令或服务 |
| --- | --- |
| sing-box 配置发布 | `sing-box`、`systemctl` |
| sing-box 安装或升级 | `systemctl` + 可访问 GitHub |
| 自动 TCP 端口分配 | 带 stream 模块的 Nginx（首次需要时由 agent 通过 `apt-get` / `dnf` / `yum` / `apk` 自动安装） |
| nftables 防火墙 | `nft` |
| Fail2Ban | `fail2ban-client`、`fail2ban.service` |
| 实时连接 | 受管 sing-box 配置的 Clash API，监听 `127.0.0.1:9090` |

目标服务器**不需要** Go、Node.js、npm 或源码。编译只在 GitHub Actions 中进行。

---

## 2. 方式一：一键安装脚本（推荐）

`install.sh` 只下载 GitHub Release 中已编译好的二进制，不在服务器上编译。

### 2.1 Master

```bash
curl -fsSLo install.sh https://raw.githubusercontent.com/liyuwei007036/polaris/main/install.sh && sudo bash install.sh master
```

### 2.2 Agent

```bash
curl -fsSLo install.sh https://raw.githubusercontent.com/liyuwei007036/polaris/main/install.sh && sudo bash install.sh agent
```

脚本会依次询问 Master 地址（`主机:端口`）、Master Noise 公钥、一次性注册令牌。令牌输入不回显；留空则跳过注册，安装完成后再手动执行注册命令。

### 2.3 Combined

```bash
curl -fsSLo install.sh https://raw.githubusercontent.com/liyuwei007036/polaris/main/install.sh && sudo bash install.sh combined
```

Combined 会先生成 Master 公钥并写好本机 agent 配置，再启动服务。终端停在等待令牌输入时，登录控制台创建一次性注册令牌并粘贴回终端。

### 2.4 脚本参数与环境变量

| 参数 | 说明 |
| --- | --- |
| `--version VERSION` | 安装指定版本，默认最新 Release |
| `--repository OWNER/REPO` | 指定 GitHub 仓库，默认 `liyuwei007036/polaris` |
| `--allow-insecure-http` | 允许明文 HTTP 登录，仅限可信内网或测试 |
| `--no-start` | 只安装文件和配置，不启动 systemd 服务 |
| `-h`, `--help` | 查看帮助 |

非交互部署可预设环境变量：`POLARIS_MASTER_ADDRESS`、`POLARIS_MASTER_PUBKEY`、`POLARIS_REGISTRATION_TOKEN`，以及 `POLARIS_VERSION`、`POLARIS_REPOSITORY`。

### 2.5 非 root 用户与无终端环境

以 `ubuntu` 等普通用户执行时无需手动加 `sudo`，脚本会自动用 `sudo` 重新执行自身，并透传上述 `POLARIS_*` 环境变量。前提是脚本已保存为文件；用 `curl ... | bash` 管道执行时无法自我提权，需改成先下载再 `sudo bash install.sh`。

脚本要下载到当前用户可写的目录，例如 `/tmp` 或家目录；写入 `/run`、`/usr/local` 等 root 目录会得到 `curl: (23) Failure writing output to destination`。

面板控制台、`ssh host '命令'`、CI、cron 等没有控制终端的环境无法交互输入，必须预设环境变量：

```bash
cd /tmp && curl -fsSLo install.sh https://raw.githubusercontent.com/liyuwei007036/polaris/main/install.sh
sudo env POLARIS_MASTER_ADDRESS='master主机:19994' POLARIS_MASTER_PUBKEY='Master的Noise公钥' POLARIS_REGISTRATION_TOKEN='一次性注册令牌' bash install.sh agent
```

缺少必需参数时脚本会直接报「非交互安装缺少 …」并退出，不会挂在等待输入上。

不放心直接把远程脚本交给 root 执行时，先下载检查再运行：

```bash
curl -fsSL https://raw.githubusercontent.com/liyuwei007036/polaris/main/install.sh -o install.sh
less install.sh
sudo bash install.sh master
```

重复执行脚本可用于更新二进制和 systemd 单元；已存在的 `/etc/polaris/*.yaml` 和 agent 身份不会被覆盖。

脚本会做的事：安装 `/usr/local/bin/polaris`、写入三个 systemd 单元、按模式创建数据目录与配置、创建 `polaris` 系统用户（master/combined）、启用并启动对应服务。

---

## 3. 方式二：从 Release 手动安装

### 3.1 下载并校验发布包

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "不支持的 CPU 架构：$(uname -m)" >&2; exit 1 ;;
esac

VERSION=0.1.0
REPOSITORY=liyuwei007036/polaris
PACKAGE="polaris_${VERSION}_linux_${ARCH}"

curl -fLO "https://github.com/${REPOSITORY}/releases/download/v${VERSION}/${PACKAGE}.tar.gz"
curl -fLO "https://github.com/${REPOSITORY}/releases/download/v${VERSION}/${PACKAGE}.tar.gz.sha256"
sha256sum -c "${PACKAGE}.tar.gz.sha256"
tar -xzf "${PACKAGE}.tar.gz"
```

`VERSION` 改成 Releases 页面上的实际版本。校验通过后目录结构：

```text
polaris_0.1.0_linux_amd64/
├── polaris
├── install.sh
├── README.md
└── deploy/
    ├── polaris-master.yaml
    ├── polaris-agent.yaml
    ├── polaris-master.service
    ├── polaris-agent.service
    └── polaris-combined.service
```

解压后也可以完全离线地使用包内脚本安装：

```bash
cd "${PACKAGE}"
sudo ./install.sh master
```

master 与 agent 使用同一个二进制，Web 控制台已嵌入其中；角色由子命令决定。

### 3.2 安装 master

在控制服务器执行：

```bash
cd "${PACKAGE}"

sudo install -m 0755 ./polaris /usr/local/bin/polaris

if ! id polaris >/dev/null 2>&1; then
  sudo useradd --system --user-group \
    --home-dir /var/lib/polaris-master \
    --shell /usr/sbin/nologin \
    polaris
fi

sudo install -d -o polaris -g polaris -m 0700 /var/lib/polaris-master
sudo install -d -o root -g polaris -m 0750 /etc/polaris
sudo install -o root -g polaris -m 0640 deploy/polaris-master.yaml /etc/polaris/master.yaml
sudo install -m 0644 deploy/polaris-master.service /etc/systemd/system/polaris-master.service

sudo systemctl daemon-reload
sudo systemctl enable --now polaris-master.service
sudo systemctl status polaris-master.service
```

发行版的 `nologin` 若位于 `/sbin/nologin`，相应修改 `useradd`。master 不需要 root：默认端口 `19670` 和 `19994` 均高于 1024。

获取 Master Noise 公钥（所有 agent 都要固定这一个值）：

```bash
sudo -u polaris /usr/local/bin/polaris master show-pubkey --config /etc/polaris/master.yaml
```

输出是 32 字节 Curve25519 公钥的 Base64 形式（44 字符，以 `=` 结尾）。它不是 HTTPS 证书、Reality 公钥或注册令牌。

不要删除或单独替换 `/var/lib/polaris-master/master-noise.key.enc`：私钥一旦改变，已注册 agent 会因公钥固定校验失败而全部断连。

### 3.3 安装 agent

在每台受控服务器上执行：

```bash
cd "${PACKAGE}"

sudo install -m 0755 ./polaris /usr/local/bin/polaris
sudo install -d -o root -g root -m 0700 /var/lib/polaris-agent
sudo install -d -o root -g root -m 0700 /etc/polaris
sudo install -o root -g root -m 0600 deploy/polaris-agent.yaml /etc/polaris/agent.yaml
sudo install -m 0644 deploy/polaris-agent.service /etc/systemd/system/polaris-agent.service
```

编辑 `/etc/polaris/agent.yaml`，填入 `master_address` 和上一步得到的 `master_public_key`。

先确认到 master 的 TCP 可达（不要用 `curl https://`）：

```bash
MASTER_ADDRESS=control.example.com:19994
MASTER_HOST=${MASTER_ADDRESS%:*}
MASTER_PORT=${MASTER_ADDRESS##*:}

getent hosts "$MASTER_HOST"
timeout 5 bash -c "</dev/tcp/${MASTER_HOST}/${MASTER_PORT}"
```

在控制台生成一次性注册令牌：登录 → “服务器”页面 → “添加服务器” → 立即复制只显示一次的令牌。

注册并启动：

```bash
REGISTRATION_TOKEN='<ONE_TIME_TOKEN>'

sudo /usr/local/bin/polaris agent register \
  --config /etc/polaris/agent.yaml \
  --token "$REGISTRATION_TOKEN"

unset REGISTRATION_TOKEN

sudo systemctl daemon-reload
sudo systemctl enable --now polaris-agent.service
sudo systemctl status polaris-agent.service
```

注册命令执行一次即退出，正常输出 `pending`（master 已批准过该公钥则为 `approved`）。节点名称自动取自操作系统主机名，无需配置。

注册会在 `/var/lib/polaris-agent` 生成节点私钥 `agent-noise.key`。**不要在多台服务器间复制 agent 数据目录**，否则它们会共享同一身份。

最后回到控制台“服务器”页面，在“等待确认的服务器”中核对节点名称后点击“允许接入”。名称或来源不符合预期时不要批准。

### 3.4 安装 combined

先按 3.2 完成 master 部分（二进制、用户、目录、`master.yaml`），然后：

```bash
PUBKEY=$(sudo -u polaris /usr/local/bin/polaris master show-pubkey --config /etc/polaris/master.yaml)

sudo install -d -o root -g root -m 0700 /var/lib/polaris-agent
sudo tee /etc/polaris/agent.yaml >/dev/null <<EOF
data_dir: /var/lib/polaris-agent
master_address: '127.0.0.1:19994'
master_public_key: '${PUBKEY}'
heartbeat_interval: 30s
connections_interval: 2s
EOF
sudo chmod 0600 /etc/polaris/agent.yaml

sudo install -m 0644 "${PACKAGE}/deploy/polaris-combined.service" /etc/systemd/system/polaris-combined.service
sudo systemctl daemon-reload
sudo systemctl enable --now polaris-combined.service
```

启动后本机 agent 尚未注册，会被拒绝，但控制台保持可用。登录控制台生成令牌，执行一次注册，再在控制台批准，进程内的 agent 会自动重连：

```bash
sudo /usr/local/bin/polaris agent register --config /etc/polaris/agent.yaml --token '<ONE_TIME_TOKEN>'
```

---

## 4. 配置文件参考

配置文件必须是 `.yaml` 或 `.yml`，单个 YAML 文档，不超过 1 MiB，不接受 JSON，**未知字段会导致启动失败**。Master 配置只填端口，不配置监听 IP（监听所有本机地址）。

### 4.1 master.yaml

```yaml
data_dir: /var/lib/polaris-master
database_path: /var/lib/polaris-master/polaris.db
agent_port: 19994
web_port: 19670
allow_insecure_http: false
```

| 字段 | 默认值 | 约束 |
| --- | --- | --- |
| `data_dir` | `data` | 相对路径基于配置文件所在目录解析 |
| `database_path` | `<data_dir>/polaris.db` | 留空时自动取默认值 |
| `agent_port` | `19994` | 1–65535，且不能与 `web_port` 相同 |
| `web_port` | `19670` | 1–65535 |
| `allow_insecure_http` | `false` | 置 `true` 时登录 Cookie 去掉 `Secure`，仅限内网测试 |

### 4.2 agent.yaml

```yaml
data_dir: /var/lib/polaris-agent
master_address: control.example.com:19994
master_public_key: <MASTER_NOISE_PUBKEY>
heartbeat_interval: 30s
connections_interval: 2s
```

| 字段 | 默认值 | 约束 |
| --- | --- | --- |
| `data_dir` | `agent-data` | 建议使用 `/var/lib/polaris-agent` |
| `master_address` | 无 | 必填，`主机:端口`，不带 URL scheme |
| `master_public_key` | 无 | 必填，Base64，解码后必须是 32 字节 |
| `heartbeat_interval` | `30s` | 允许 `5s` – `5m` |
| `connections_interval` | `5s`（发布模板写的是 `2s`） | 允许 `1s` – `30s` |

可选的 `nginx_passthrough_routes` 用于已有非 Polaris 服务与受管 Nginx stream 共用同一端口：

```yaml
nginx_passthrough_routes:
  - listen_address: 0.0.0.0
    port: 443
    sni: existing.example.com
    backend_address: 127.0.0.1
    backend_port: 10444
```

条目的监听地址和端口必须已由 master 的接入服务管理，SNI 不能与受管接入服务重复，并且不要再保留另一个监听相同地址端口的 Nginx `stream server`。

### 4.3 纯命令行启动（不使用配置文件）

```bash
polaris master serve \
  --data-dir /var/lib/polaris-master \
  --database-path /var/lib/polaris-master/polaris.db \
  --agent-port 19994 --web-port 19670

polaris agent serve \
  --data-dir /var/lib/polaris-agent \
  --master control.example.com:19994 \
  --master-pubkey '<MASTER_NOISE_PUBKEY>'

polaris combined serve \
  --master-data-dir /var/lib/polaris-master \
  --database-path /var/lib/polaris-master/polaris.db \
  --agent-port 19994 --web-port 19670 \
  --agent-data-dir /var/lib/polaris-agent \
  --master 127.0.0.1:19994 \
  --master-pubkey '<MASTER_NOISE_PUBKEY>'
```

同时使用 `--config` 与命令行参数时，显式传入的参数覆盖配置文件中的同名项。Combined 的 `--master-config` 与 `--agent-config` 必须成对提供。

---

## 5. 首次登录与 HTTPS

### 5.1 默认账户

master 或 combined 首次启动且数据库中没有管理账户时自动创建：

- 用户名：`polaris_admin`
- 初始密码：`123456`

首次登录后只能进入改密页面，新密码至少 12 位；改密完成前其他管理接口会被服务端拒绝。两步验证默认关闭，可在“系统设置 → 登录安全”中用验证器扫码启用。

`master init-admin` 和 `master reset-mfa` 仅为兼容既有自动化脚本保留，新部署不需要执行。

### 5.2 反向代理终止 HTTPS

master 的 Web 端口是明文 HTTP，生产环境必须由 Nginx/Caddy 等终止 HTTPS，并用防火墙限制 `19670/TCP` 的来源。

```nginx
server {
    listen 443 ssl;
    server_name control.example.com;

    ssl_certificate /etc/letsencrypt/live/control.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/control.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:19670;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }
}
```

登录 Cookie 默认带 `Secure`，因此必须通过 HTTPS 访问。只有可信内网临时测试才使用 `--allow-insecure-http` / `allow_insecure_http: true`，不要在公网启用。

---

## 6. 安装验证

Master：

```bash
sudo systemctl status polaris-master.service
curl -fsS http://127.0.0.1:19670/api/v1/health
sudo ss -lntp | grep -E ':(19670|19994)\b'
sudo journalctl -u polaris-master.service -n 100 --no-pager
```

健康检查应返回：

```json
{"status":"ok"}
```

Agent：

```bash
sudo systemctl status polaris-agent.service
sudo journalctl -u polaris-agent.service -n 100 --no-pager
sudo ls -la /var/lib/polaris-agent
```

随后在控制台确认：

1. 节点显示在线。
2. 操作系统显示 `linux`，架构显示 `amd64` 或 `arm64`。
3. “任务与审计”中没有持续失败的任务。
4. 安装完成后 `sing_box_version` 出现实际版本。

节点首次上线且检测不到 sing-box 时，master 会查询官方最新稳定版、核对官方摘要、签署安装清单并下发任务；agent 再次校验 HTTPS、SHA-256、签名与 CPU 架构后安装到 `/usr/local/bin/sing-box`。首次自动安装失败不会随心跳重复创建任务，需要在“服务器”页面手动点击“安装或升级”。系统若没有 `sing-box.service`，首次安装会创建并启用基础单元，已有单元不会被覆盖。

---

## 7. 数据目录与备份

| 路径 | 内容 | 说明 |
| --- | --- | --- |
| `/var/lib/polaris-master/polaris.db` | SQLite 数据库 | 节点、配置、审计 |
| `/var/lib/polaris-master/master.key` | 数据加密主密钥 | 丢失后数据库中的密文不可恢复 |
| `/var/lib/polaris-master/master-noise.key.enc` | Master Noise 私钥 | 变更会导致所有 agent 断连 |
| `/var/lib/polaris-agent/agent-noise.key` | 节点身份私钥 | 每台服务器唯一，禁止复制 |
| `/var/lib/polaris-agent/release-manifest.pub` | 发布签名公钥 | 校验 master 下发的安装清单 |
| `/etc/polaris/master.yaml` | Master 配置 | `root:polaris` `0640` |
| `/etc/polaris/agent.yaml` | Agent 配置 | `root:root` `0600` |

备份 master 时必须**整体**备份 `/var/lib/polaris-master`；只备份数据库而丢失 `master.key` 无法恢复。备份文件应与数据库同等保护。

---

## 8. 故障排查

### 控制台登录后仍提示未登录

- 生产环境确认通过 HTTPS 访问（Cookie 带 `Secure`）。
- 确认反向代理转发到 `127.0.0.1:19670` 并设置了 `X-Forwarded-Proto https`。
- 仅明文 HTTP 测试环境才使用 `--allow-insecure-http`。
- 已启用两步验证时，确认浏览器、验证器设备和 master 的系统时间准确。

### agent 无法连接 master

- `master_address` / `--master` 必须是 `主机:端口`，不能带 URL scheme。
- 确认 master 的 `19994/TCP` 已监听并放行。
- 确认公钥取自同一个 master 数据目录，且是 32 字节 Base64。
- 不要把 agent 连接送进 HTTP/HTTPS/TLS 终止代理。
- 查看 `journalctl -u polaris-agent.service` 中的 Noise 握手错误。

### agent 在线但任务提示权限不足

- 确认 systemd 单元使用 `User=root`。
- 确认 `/var/lib/polaris-agent` 属 root 且权限 `0700`。
- 按任务类型确认 `systemctl`、`nft`、`fail2ban-client` 可用；Nginx 由 agent 首次需要时自动安装，失败原因记录在任务结果中。

### sing-box 没有自动安装

- 确认节点能执行 `sing-box version`。
- 确认节点系统为 Linux，架构为 `amd64` 或 `arm64`。
- 确认 master 能访问 GitHub API，agent 能访问官方 Release 下载地址。
- 在控制台“任务与审计”中查看失败原因，然后手动点击“安装或升级”重试。

### 启动即报配置错误

- 配置文件扩展名必须是 `.yaml` 或 `.yml`。
- 不能有未知字段、多个 YAML 文档或空文件。
- `agent_port` 与 `web_port` 不能相同。
- `heartbeat_interval` 需在 `5s`–`5m`，`connections_interval` 需在 `1s`–`30s`。

### 端口被占用 / 19994 未监听

```bash
sudo ss -lntp | grep -E ':(19670|19994)\b'
sudo journalctl -u polaris-master.service -n 200 --no-pager
```

常见原因是端口占用或数据目录权限不正确。

---

## 9. 卸载

```bash
# 按实际模式选择服务名
sudo systemctl disable --now polaris-master.service
sudo systemctl disable --now polaris-agent.service
sudo systemctl disable --now polaris-combined.service

sudo rm -f /etc/systemd/system/polaris-{master,agent,combined}.service
sudo systemctl daemon-reload

sudo rm -f /usr/local/bin/polaris
```

数据与配置需要单独确认后再删除，删除后不可恢复：

```bash
sudo rm -rf /etc/polaris
sudo rm -rf /var/lib/polaris-master   # 删除后控制面数据不可恢复
sudo rm -rf /var/lib/polaris-agent    # 删除后节点身份永久更换
sudo userdel polaris
```

删除 agent 数据目录会永久更换节点身份，需要重新注册并在控制台重新批准。sing-box、Nginx、nftables、Fail2Ban 及其配置不在本卸载步骤范围内。

---

## 10. 命令速查

```bash
polaris version                                             # 查看版本

polaris master serve       --config /etc/polaris/master.yaml
polaris master show-pubkey --config /etc/polaris/master.yaml

polaris agent register --config /etc/polaris/agent.yaml --token TOKEN
polaris agent serve    --config /etc/polaris/agent.yaml

polaris combined serve --master-config /etc/polaris/master.yaml \
                          --agent-config  /etc/polaris/agent.yaml
```
