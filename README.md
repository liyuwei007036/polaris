# sb-control

`sb-control` 是一个单 Go 二进制，包含 `master` 与 `agent` 两种运行角色。

当前实现覆盖方案的 P-0 与 P-1：管理员密码与 TOTP、会话与 CSRF、角色授权、一次性节点注册凭据、节点 CSR 审核签发、节点证书轮换与吊销，以及 mTLS agent 心跳、持续控制流和节点能力/在线状态上报。

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
.\sb-control.exe master serve --data-dir .\data --listen :8443 --tls-cert .\master.crt --tls-key .\master.key
```

生产环境必须使用受信任的 HTTPS 证书。master 不会生成默认管理员账户或默认密码。

浏览器访问 `https://<master>:8443/` 后，使用管理员密码和 TOTP 登录。页面仅展示已实现的节点状态、一次性注册凭据与待审核注册。

agent 注册并获签后，以 mTLS 运行控制流和心跳：

```powershell
.\sb-control.exe agent run --data-dir .\agent-data --master https://master.example.com --sing-box-version 1.12.0
```

如 master 使用私有 HTTPS CA，额外传入 `--master-ca <ca.pem>`。agent 控制流目前只建立连接、保活和上报；尚未实现配置发布或任意命令执行。
