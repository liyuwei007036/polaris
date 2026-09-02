# Polaris

Polaris 是自建代理服务的集中管理平台。一台控制机管理任意数量的受控服务器，在 Web 控制台里完成 sing-box 接入服务、客户端订阅、出口与路由、主机防火墙与 Fail2Ban 的配置下发与状态查看，不需要登录每台服务器改配置文件。

- 安装、初始化、验证与卸载：见 [INSTALL.md](INSTALL.md)
- 本文覆盖：整体架构、术语、控制台使用流程、本地开发

---

## 1. 架构

### 1.1 三种运行模式

| 模式 | 进程内容 | 运行用户 |
| --- | --- | --- |
| `master` | 控制面 + Web 控制台 | `polaris` |
| `agent` | 受控节点代理 | `root` |
| `combined` | 同一进程内同时运行 master 与本机 agent | `root` |

同一台机器只能启用其中一个 systemd 服务。Combined 模式下本机 agent 同样要走一次性令牌注册与管理员批准，不会自动信任。

### 1.2 两个端口

| 端口 | 默认值 | 协议 | 用途 |
| --- | --- | --- | --- |
| Web 端口 | `19670/TCP` | 明文 HTTP | 控制台与 JSON API，由前置反向代理终止 HTTPS |
| Agent 端口 | `19994/TCP` | Noise 加密的原始 TCP | 节点注册、心跳、任务下发、实时连接推送 |

`19994` 不是 HTTPS，不能放在会解包 TLS 的反向代理后面。

### 1.3 控制面与节点之间

- agent 主动出站连接 master 的 agent 端口，建立一条长连接。传输层是 Noise_XK + 原始 Curve25519 公钥（无 X.509 证书、无 HTTP），消息为二进制帧 + gob 编码，实现在 `internal/wire`。
- 身份靠公钥固定：agent 用配置里的 master 公钥校验控制面，master 在管理员批准后固定该节点公钥。
- master 只下发配置与事实，**落地方式由节点自己决定**：接入服务是直接绑定公网端口，还是移到受管 Nginx stream 的 SNI 分流后面，取决于节点上该端口是否已被占用（`internal/agent/placement.go`）。
- agent 以 root 长期运行，代表 master 写入 `/etc/sing-box`、Nginx stream 配置、Fail2Ban 与主机防火墙，并调用 `systemctl`。

### 1.4 下发的任务类型

| 任务 | 作用 |
| --- | --- |
| `singbox.install` | 在节点上安装或升级 sing-box |
| `singbox.apply_config` | 写入编译好的 sing-box 配置并重载 |
| `nginx.apply_config` | 写入受管 Nginx stream 的 SNI 路由 |
| `firewall.mutate` | 增删主机防火墙端口规则 |
| `fail2ban.query` / `fail2ban.mutate` / `fail2ban.unban` | 读取 jail 状态、启停 jail、解封地址 |
| `outbound.test` | 从节点侧探测上网出口是否可用 |
| `agent.upgrade` | 升级节点上的 polaris 二进制 |

节点配置由控制面从**结构化对象**编译得到（`internal/control/compiler.go`），不接受调用方直接塞入任意 sing-box JSON。

---

## 2. 术语对照

控制台里的名词与代码里的类型不完全同名，先对齐：

| 控制台名称 | 代码 | 含义 |
| --- | --- | --- |
| 服务器 | node | 一台装有 agent 的受控主机 |
| 接入服务 | listener | 某台服务器上的一个 sing-box 入站 |
| 节点 / 用户 | endpoint | 接入服务下的一份客户端凭据，对应一条分享链接与二维码 |
| 上网出口 | outbound | 全局出口定义（`direct` 之外的 SOCKS5 / HTTP 代理），任意接入服务可选 |
| 服务器访问规则 | route rule | 按域名、CIDR、端口、协议把流量导向某个出口 |
| 客户端配置 | mihomo client config | 生成给客户端的 Mihomo 订阅（代理分组 + 规则供应商 + DNS） |

支持的接入服务形态：VLESS + Reality、VLESS + WebSocket、VLESS + gRPC、Hysteria2。

---

## 3. 控制台功能

| 菜单 | 页面 | 作用 |
| --- | --- | --- |
| 工作台 | 运行概览 | 服务器总数 / 在线数、节点数量、活动连接 |
| 工作台 | 服务器 | 生成接入命令、批准与移除节点、安装升级 sing-box、升级 agent、填写客户端连接地址 |
| 连接配置 | 接入服务 | 新建接入服务与用户，查看分享链接与二维码 |
| 连接配置 | 代理分组 | 客户端订阅里的 select / url-test / fallback 分组 |
| 连接配置 | 规则供应商 | 订阅引用的外部规则集 |
| 连接配置 | 客户端配置 | 组装并分发 Mihomo 订阅，含订阅令牌轮换与访问记录 |
| 连接配置 | 服务器访问规则 | 每台服务器的分流规则，支持优先级与下发前预览 |
| 连接配置 | 上网出口 | 管理出口并从节点侧发起连通性检测 |
| 状态与记录 | 当前连接 | agent 从本机 sing-box Clash API 读取后实时推送 |
| 状态与记录 | 操作记录 | 审计事件与任务执行结果 |
| 系统管理 | 网络防护 | 主机防火墙端口规则、Fail2Ban jail 与封禁列表 |
| 系统管理 | 域名解析 | Cloudflare DNS 记录与源证书 |
| 系统管理 | 系统设置 | 管理账户、两步验证、控制面自更新 |

网络防护页列出的是主机当前真实生效的规则，每次打开或刷新都实时读取，平台不维护影子规则表。节点上只保留一套防火墙：agent 发现 ufw 或 firewalld 正在运行，会先把它们放行的端口翻译成 iptables 规则再停用该工具，之后所有增删都写 iptables 的 `INPUT` 链并持久化。

管理账户分三种权限：管理员、运维人员、只读用户。

---

## 4. 日常使用流程

### 4.1 接入一台服务器

1. 控制台「服务器 → 接入」生成一次性注册令牌与安装命令（只显示一次，默认 15 分钟有效）。
2. 在目标机器上执行该命令，或按 [INSTALL.md](INSTALL.md) 手动安装 agent 并执行 `polaris agent register`。
3. 回到「服务器」页批准注册。批准后该节点的公钥被固定，后续连接只认这把公钥。
4. 填写「客户端连接地址」——这是分享链接与订阅里写给客户端的地址，没填则该服务器不会出现在分享链接中。

### 4.2 准备 sing-box

在「服务器」页对节点执行安装或升级。控制面从 GitHub 查询 sing-box 官方稳定版，节点侧校验签名清单后再落地。

### 4.3 建立接入服务与用户

1. 「接入服务 → 新建」，选服务器、协议形态与公网端口。
2. 若该端口上已有启用的 TCP 接入服务，两者会在同一次事务里一起迁到受管 SNI 路由后面，不需要手工挪端口。
3. 在同一表单里添加用户，保存后即生成配置并下发。
4. 用「二维码」或「复制」取得分享链接分发给客户端。

### 4.4 分发客户端配置

面向 Mihomo / Clash 客户端时改用「客户端配置」：挑选要暴露的节点、组织代理分组、挂规则供应商与 DNS，保存后得到订阅链接。令牌可随时轮换，旧链接立即失效。

### 4.5 出口与分流

- 「上网出口」定义全局出口并做连通性检测；出口是全局的，任意服务器上的接入服务都能选。
- 「服务器访问规则」按服务器配置分流，调整优先级，下发前可预览编译结果。

### 4.6 防护与观察

- 「网络防护」增删端口放行、启停 Fail2Ban jail、解封地址；改动立即写入服务器。
- 「当前连接」看实时连接，「操作记录」回溯谁在什么时候下发了什么。

### 4.7 维护

- 控制面自身在「系统设置」里检查并应用更新。
- 节点 agent 在「服务器」页逐台升级。
- 首次登录使用 `polaris_admin` / `123456`，改密前其他管理接口一律拒绝，新密码至少 12 位；两步验证在「系统设置 → 登录安全」中启用。

---

## 5. 目录结构

| 路径 | 内容 |
| --- | --- |
| `cmd/polaris` | 命令行入口：`master` / `agent` / `combined` / `version` |
| `internal/control` | 控制面：存储、编译器、HTTP API、订阅、Cloudflare、自更新 |
| `internal/control/web` | 前端构建产物，由 `go:embed` 打进二进制 |
| `internal/agent` | 节点侧：任务执行、端口落位、Nginx 接管、防火墙、Fail2Ban |
| `internal/wire` | master 与 agent 之间的 Noise 传输与消息定义 |
| `internal/nginxroute` | Nginx stream SNI 路由的编译 |
| `internal/security` | 口令、TOTP 等安全原语 |
| `internal/selfupdate` | 校验并替换运行中的二进制 |
| `webui` | Vue 3 + Element Plus 控制台，含桌面与移动端两套视图 |
| `e2e` | 以真实进程启动 master 与 agent 的黑盒测试 |
| `deploy` | systemd unit 与配置样例 |
| `install.sh` | 一键安装脚本 |

---

## 6. 本地开发

需要 Go（版本以 `go.mod` 为准）与 Node.js 22 及以上。目标服务器不需要这些，编译只在本地或 GitHub Actions 中进行。

### 6.1 构建

```bash
npm --prefix webui ci
npm --prefix webui run build
go build ./cmd/polaris
```

前端产物写入 `internal/control/web/dist`，该目录在 `.gitignore` 中。新克隆的仓库**必须先构建前端**，否则 `go:embed` 找不到目录，Go 侧直接编译失败。

调试前端可用 `npm --prefix webui run dev`；`webui/vite.config.js` 里的 `/api` 代理目标是固定的内网地址，按自己的 master 地址改。

### 6.2 测试

```bash
go test ./...
```

```bash
go test -tags=e2e -count=1 ./e2e
```

```bash
npm --prefix webui run test:e2e
```

`npm run test:e2e` 会先 build 再跑 Playwright；改了前端源码直接跑 `playwright test` 测的是旧产物。移动端用例是 `npm --prefix webui run test:e2e:mobile`。

`scripts_e2e.sh`（Linux/macOS）与 `scripts_e2e.ps1`（Windows）把 Go e2e 与前端 e2e 串起来一次跑完。

### 6.3 发布

推送 `v*` 标签触发 `.github/workflows/release.yml`：校验安装脚本、构建前端、跑 `go test ./...`，然后交叉编译 `linux/amd64` 与 `linux/arm64` 并打包上传 Release。

---

## 7. 命令速查

```bash
polaris version

polaris master serve       --config /etc/polaris/master.yaml
polaris master show-pubkey --config /etc/polaris/master.yaml

polaris agent register --config /etc/polaris/agent.yaml --token TOKEN
polaris agent serve    --config /etc/polaris/agent.yaml

polaris combined serve --master-config /etc/polaris/master.yaml \
                       --agent-config  /etc/polaris/agent.yaml
```

故障排查见 [INSTALL.md 第 8 节](INSTALL.md)。

---

## 8. 许可证

本项目以 Apache License 2.0 发布，全文见 [LICENSE](LICENSE)。

```
Copyright 2026 Polaris

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
```
