# sb-control

`sb-control` 是一个单 Go 二进制，包含 `master` 与 `agent` 两种运行角色。

当前实现覆盖方案的 P-0 与 P-1：管理员密码与 TOTP、会话与 CSRF、角色授权、一次性节点注册凭据、节点公钥审核批准与吊销，以及 agent 状态上报、持续会话与节点能力/在线状态、实时连接推送。

agent ↔ master 之间**不使用 HTTP**：走的是一条 Noise 协议（`Noise_XK`，与 WireGuard 同款的握手模式）加密的原始 TCP 长连接，节点身份是一对 Curve25519 公私钥（不是 X.509 证书，没有 CA，没有证书签发/轮换），消息体是二进制编码（`encoding/gob`），不是 JSON。浏览器 ↔ master 仍是普通 HTTP/JSON。

## 本地开发

```powershell
go test ./...
go build ./cmd/sb-control
```

初始化管理员不会使用固定密码：

```powershell
'选择一个高强度密码' | .\sb-control.exe master init-admin --data-dir .\data --email admin@example.com --password-stdin
```

命令仅在终端显示一次 TOTP 密钥；先导入验证器后再启动 Web 服务。

```powershell
.\sb-control.exe master serve --data-dir .\data --agent-listen :8443 --browser-listen :8080
```

master 监听两个**独立端口**，用途不同，都不需要你提供任何证书：

- `--agent-listen`（默认 `:8443`）：agent 走的端口，Noise 协议加密的原始 TCP，不是 TLS/HTTPS。master 首次启动会自动生成一对 Curve25519 密钥并加密保存在 `--data-dir` 里；用 `master show-pubkey` 查看公钥（agent 那边需要把这个值配成 `--master-pubkey`，是 WireGuard 那种"提前互相知道对方公钥"的信任模型，不是证书链）。这个端口不能放在做 TLS 终止的反向代理后面——反代会把这条连接的字节流"消费掉"，Noise 握手就做不成了；如果确实想让这条线也过 nginx，nginx 只能做不解密的纯 TCP 转发（`stream` 模块）。
- `--browser-listen`（默认 `:8080`）：管理员用浏览器访问的端口，本身就是明文 HTTP，不涉及证书。如果需要公网 HTTPS，按标准反向代理模式在前面套一层 nginx（证书由 nginx 管理，nginx 转发明文 HTTP 给这个端口）；纯内网访问可以不加任何反向代理，直接访问明文 HTTP。

  **注意**：默认情况下会话 Cookie 带 `Secure` 属性（假设你会在前面套 HTTPS 反向代理）。如果你**不**打算加反向代理、就直接裸访问这个明文 HTTP 端口，必须加 `--insecure-dev-cookies`，否则浏览器不会回传 Cookie，登录会一直失败且没有明显报错。

master 不会生成默认管理员账户或默认密码。

浏览器访问 `http://<master>:8080/`（或你在前面配置的 HTTPS 反代地址）后，使用管理员密码和 TOTP 登录。

## agent 接入一个节点

1. 查看 master 的公钥（只需要做一次，之后所有 agent 共用）：

   ```powershell
   .\sb-control.exe master show-pubkey --data-dir .\data
   ```

2. 在 master 网页上生成一次性注册凭据（管理员操作），拿到 `--token`。

3. 在被控服务器上注册（会在 `--data-dir` 下生成本机的 Curve25519 密钥对，仅需一次）：

   ```powershell
   .\sb-control.exe agent register --data-dir .\agent-data --master master.example.com:8443 --master-pubkey <上一步的公钥> --token <一次性凭据> --node-name my-node
   ```

   注册后状态是"待审批"，管理员在网页上批准这个节点即可。

4. 长期运行（systemd 建议，见 `deploy/sb-control-agent.service`）：

   ```powershell
   .\sb-control.exe agent run --data-dir .\agent-data --master master.example.com:8443 --master-pubkey <公钥> --sing-box-version 1.12.0
   ```

   `agent run` 会自动重连、自动等待审批通过（未批准时每隔几秒重试一次，不需要额外的"轮询"命令）。批准后的同一条连接上，agent 按 `--heartbeat-interval`（默认 30 秒）上报状态、按 `--connections-interval`（默认 2 秒）主动推送实时连接列表，并执行 master 下发的配置发布任务——全部复用同一条 Noise 长连接，不是分开的多个请求。

agent 需要以 root 身份运行才能写入 `/etc/sing-box`、`/etc/nginx`、`/etc/fail2ban` 并管理 `sing-box.service`；参见 `deploy/sb-control-agent.service`。首次下发 sing-box 配置时，若系统尚无 `sing-box.service` 单元，agent 会自动创建并启用一个。

**已知取舍**：去掉了旧版的证书轮换功能——现在如果一个节点的密钥需要更换，走"吊销旧节点 + 重新走一遍注册审批"，而不是原地换发证书。
