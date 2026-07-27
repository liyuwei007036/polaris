#!/usr/bin/env bash
# End-to-end verification for sb-control on the Pi (P-0..P-7 subset that is
# safe to run on this shared host). Master runs as the pi user; the agent runs
# as root only so nftables is usable. All writes are namespaced (inet sb_control
# table) and cleaned up on exit.
set -u
cd "$(dirname "$0")"
BASE=https://127.0.0.1:8443
CA="$PWD/cert.pem"
PASS=0; FAIL=0
ok(){ echo "  PASS: $1"; PASS=$((PASS+1)); }
no(){ echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
sect(){ echo; echo "== $1 =="; }

MASTER_PID=""; AGENT_PID=""
cleanup(){
  echo; echo "== teardown =="
  [ -n "$AGENT_PID" ] && sudo kill "$AGENT_PID" 2>/dev/null
  [ -n "$MASTER_PID" ] && kill "$MASTER_PID" 2>/dev/null
  sudo nft delete table inet sb_control 2>/dev/null && echo "  removed inet sb_control table"
  echo "  RESULT: PASS=$PASS FAIL=$FAIL"
}
trap cleanup EXIT

jf(){ python3 -c 'import sys,json
try: d=json.load(sys.stdin)
except Exception: sys.exit(0)
k=sys.argv[1]
v=d
for part in k.split("."):
    if isinstance(v,list):
        try: v=v[int(part)]
        except Exception: v=""; break
    else: v=v.get(part,"") if isinstance(v,dict) else ""
print(v if v is not None else "")' "$1"; }

# curl helper: METHOD PATH CSRF BODY -> body on stdout, HTTP code to /tmp/sb_code.
# The status is written to a file (not a shell var) so it survives the
# command-substitution subshell that every `body=$(req ...)` call site uses.
req(){
  local m=$1 p=$2 c=${3:-} b=${4:-}
  local a=(-s -o /tmp/sb_resp -w '%{http_code}' --cacert "$CA" -X "$m" "$BASE$p" -b /tmp/sb_cookies -c /tmp/sb_cookies)
  [ -n "$c" ] && a+=(-H "X-CSRF-Token: $c")
  [ -n "$b" ] && a+=(-H 'Content-Type: application/json' -d "$b")
  curl "${a[@]}" >/tmp/sb_code; cat /tmp/sb_resp
}
# code prints the HTTP status of the most recent req call.
code(){ cat /tmp/sb_code 2>/dev/null; }

totp(){ python3 - "$1" <<'PY'
import sys,hmac,hashlib,base64,struct,time
s=sys.argv[1].upper(); s+='='*((8-len(s)%8)%8)
k=base64.b32decode(s); c=int(time.time())//30
mac=hmac.new(k,struct.pack('>Q',c),hashlib.sha1).digest()
o=mac[-1]&0x0f
print('%06d'%((struct.unpack('>I',mac[o:o+4])[0]&0x7fffffff)%1000000))
PY
}

rm -f /tmp/sb_cookies
# agent-data/completed-tasks is written by the root-run agent, so clean as root.
sudo rm -rf data agent-data
BIN="$PWD/sb-control"

sect "P-0  身份与 MFA（无默认口令）"
# self-signed serving cert with 127.0.0.1 SAN
openssl req -x509 -newkey ed25519 -nodes -keyout key.pem -out cert.pem -days 3 \
  -subj "/CN=sb-control-master" -addext "subjectAltName=IP:127.0.0.1,DNS:localhost" >/dev/null 2>&1
[ -s cert.pem ] && ok "生成含 127.0.0.1 SAN 的自签 TLS 证书" || no "生成 TLS 证书"

# init-admin: no default password path exists — a password is mandatory
ADMPW='Adm!n-verify-9271'
SECRET=$("$BIN" master init-admin --data-dir data --email admin@example.com --password-stdin <<<"$ADMPW" 2>/tmp/sb_err | tail -1)
if [ -n "$SECRET" ] && [ ${#SECRET} -ge 16 ]; then ok "init-admin 创建管理员并一次性返回 TOTP 密钥"; else no "init-admin: $(cat /tmp/sb_err)"; fi
# proof of "no default": serve refuses to start without an explicit TLS material / admin exists only via init-admin
if "$BIN" master serve --data-dir data --listen 127.0.0.1:9 2>&1 | grep -q 'tls-cert'; then ok "master 不提供无 TLS 的明文入口（强制 --tls-cert/--tls-key）"; else no "master serve TLS 强制"; fi

# start master
nohup "$BIN" master serve --data-dir data --listen 127.0.0.1:8443 --tls-cert cert.pem --tls-key key.pem >master.log 2>&1 &
MASTER_PID=$!
for i in $(seq 1 40); do req POST /api/v1/auth/login "" '{"email":"x","password":"y"}' >/dev/null 2>&1; C=$(code); [ -n "$C" ] && [ "$C" != 000 ] && break; sleep 0.3; done
C=$(code); { [ -n "$C" ] && [ "$C" != 000 ]; } && ok "master 已在 127.0.0.1:8443 提供 HTTPS 服务" || { no "master 启动失败: $(tail -3 master.log)"; exit 1; }

# wrong password rejected
body=$(req POST /api/v1/auth/login "" '{"email":"admin@example.com","password":"wrong"}')
C=$(code); [ "$C" != 200 ] && ok "错误口令被拒绝（$C）" || no "错误口令未被拒绝"

# correct login -> challenge
body=$(req POST /api/v1/auth/login "" "{\"email\":\"admin@example.com\",\"password\":\"$ADMPW\"}")
CH=$(echo "$body" | jf challenge_id)
[ -n "$CH" ] && ok "口令校验通过，返回 MFA challenge" || no "登录未返回 challenge: $body"

# access protected endpoint before MFA -> denied
body=$(req GET /api/v1/operators)
C=$(code); [ "$C" != 200 ] && ok "未完成 MFA 时管理接口被拒绝（$C）" || no "未完成 MFA 仍可访问管理接口"

# MFA -> csrf + session cookie
CODEV=$(totp "$SECRET")
body=$(req POST /api/v1/auth/mfa "" "{\"challenge_id\":\"$CH\",\"code\":\"$CODEV\"}")
CSRF=$(echo "$body" | jf csrf_token); ROLE=$(echo "$body" | jf role)
[ -n "$CSRF" ] && [ "$ROLE" = admin ] && ok "MFA 通过，返回 admin 会话与 CSRF" || no "MFA 失败: $body"

# write without CSRF -> denied (CSRF enforcement)
body=$(req POST /api/v1/nodes/registration-tokens "" '{"lifetime_seconds":600}')
C=$(code); [ "$C" != 201 ] && ok "缺少 CSRF 头的写操作被拒绝（$C）" || no "缺少 CSRF 仍可写"

sect "P-1  节点注册 / 审核 / 自报 / 重连"
body=$(req POST /api/v1/nodes/registration-tokens "$CSRF" '{"lifetime_seconds":900}')
TOKEN=$(echo "$body" | jf token)
[ -n "$TOKEN" ] && ok "签发一次性注册凭据" || no "注册凭据签发失败: $body"

REGOUT=$(SSL_CERT_FILE="$CA" "$BIN" agent register --data-dir agent-data --master "$BASE" --token "$TOKEN" --node-name pi-local 2>&1)
REGID=$(echo "$REGOUT" | sed -n 's/^registration_id=//p')
POLL=$(echo "$REGOUT" | sed -n 's/^poll_token=//p')
[ -n "$REGID" ] && ok "agent 提交 CSR 并进入待审核" || no "agent register 失败: $REGOUT"

# fetch before approval must not yield a cert
FB=$(SSL_CERT_FILE="$CA" "$BIN" agent fetch-certificate --data-dir agent-data --master "$BASE" --registration-id "$REGID" --poll-token "$POLL" 2>&1)
echo "$FB" | grep -qi 'pending' && ok "审核前无法取回证书（状态 pending）" || no "审核前取证行为异常: $FB"

# admin sees the pending registration
body=$(req GET /api/v1/registrations "$CSRF")
echo "$body" | grep -q "$REGID" && ok "管理端可见待审核注册" || no "待审核列表缺失"

# approve
body=$(req POST "/api/v1/nodes/$REGID/approve" "$CSRF" '')
NODEID=$(echo "$body" | jf node_id)
[ -n "$NODEID" ] && ok "审核通过并签发节点证书（node_id=$NODEID）" || no "审核失败: $body"

# fetch cert now succeeds
FB=$(SSL_CERT_FILE="$CA" "$BIN" agent fetch-certificate --data-dir agent-data --master "$BASE" --registration-id "$REGID" --poll-token "$POLL" 2>&1)
echo "$FB" | grep -qi 'saved' && ok "agent 取回并保存 mTLS 证书" || no "取证失败: $FB"

# run agent as root (nft on secure PATH); heartbeat + control channel
sudo nohup "$BIN" agent run --data-dir agent-data --master "$BASE" --master-ca "$CA" --heartbeat-interval 5s >agent.log 2>&1 &
AGENT_PID=$!
online=""
for i in $(seq 1 20); do
  body=$(req GET /api/v1/nodes "$CSRF")
  online=$(echo "$body" | jf nodes.0.online)
  [ "$online" = True ] && break; sleep 1
done
[ "$online" = True ] && ok "agent 心跳上线，master 显示 online" || no "节点未上线: $body / $(tail -3 agent.log)"
CAPS=$(echo "$body" | jf nodes.0.capabilities)
[ -n "$CAPS" ] && ok "节点自报能力矩阵（capabilities 非空）" || no "capabilities 为空"

sect "P-3  同 Listener 多 Endpoint + 端口冲突拦截"
body=$(req POST /api/v1/listeners "$CSRF" "{\"node_id\":\"$NODEID\",\"name\":\"ss-a\",\"listen_address\":\"127.0.0.1\",\"port\":10800,\"enabled\":true,\"spec\":{\"protocol\":\"shadowsocks\",\"network\":\"tcp\"}}")
LID=$(echo "$body" | jf id)
[ -n "$LID" ] && ok "创建 shadowsocks Listener（端口 10800）" || no "创建 Listener 失败: $body"
body=$(req POST "/api/v1/listeners/$LID/endpoints" "$CSRF" '{"name":"user-1","enabled":true,"credentials":{"method":"aes-128-gcm","password":"pw-user-1"}}')
E1=$(echo "$body" | jf id)
body=$(req POST "/api/v1/listeners/$LID/endpoints" "$CSRF" '{"name":"user-2","enabled":true,"credentials":{"method":"aes-128-gcm","password":"pw-user-2"}}')
E2=$(echo "$body" | jf id)
[ -n "$E1" ] && [ -n "$E2" ] && ok "同一 Listener 挂载多个用户 Endpoint" || no "多 Endpoint 创建失败"
# confirm endpoint listing masks credentials (no password echoed)
body=$(req GET "/api/v1/listeners/$LID/endpoints" "$CSRF")
echo "$body" | grep -q 'pw-user-1' && no "Endpoint 列表回显了明文凭据" || ok "Endpoint 列表不回显凭据（脱敏）"
# port conflict
body=$(req POST /api/v1/listeners "$CSRF" "{\"node_id\":\"$NODEID\",\"name\":\"ss-b\",\"listen_address\":\"127.0.0.1\",\"port\":10800,\"enabled\":true,\"spec\":{\"protocol\":\"shadowsocks\",\"network\":\"tcp\"}}")
C=$(code); [ "$C" != 201 ] && ok "同址同端口的第二个 Listener 被拒绝（$C）" || no "端口冲突未被拦截（$C）"

sect "P-4  Cloudflare 橙云绑定校验（不触网）"
# Configure a zone with a dummy token: SetCloudflareSettings never live-checks
# the token, which is exactly the "不触网" design. Records validate only after a
# zone exists.
body=$(req PUT /api/v1/cloudflare/settings "$CSRF" '{"zone_id":"zone-test-0001","zone_name":"example.com","api_token":"cf-dummy-token-offline"}')
C=$(code); [ "$C" = 204 ] && ok "配置 Cloudflare zone（token 不触网即接受）" || no "Cloudflare 设置失败: $C $body"
# grey-cloud (DNS only) record is accepted
body=$(req POST /api/v1/cloudflare/records "$CSRF" '{"name":"grey.example.com","type":"A","content":"192.0.2.10","ttl":300,"proxied":false}')
C=$(code); [ "$C" = 201 ] && ok "灰云记录（DNS-only）创建成功" || no "灰云记录创建失败: $C $body"
# orange-cloud (proxied) record without a listener binding is rejected. The
# server masks the reason as a generic "invalid request", so assert on status:
# acceptance would be 201, rejection is 400.
body=$(req POST /api/v1/cloudflare/records "$CSRF" '{"name":"orange.example.com","type":"A","content":"192.0.2.20","ttl":1,"proxied":true}')
C=$(code); [ "$C" = 400 ] && ok "橙云记录未绑定 Listener 被拒绝（400）" || no "橙云绑定校验未生效: $C $body"

sect "P-5  客户端订阅预览"
body=$(req POST /api/v1/subscriptions "$CSRF" "{\"kind\":\"client\",\"name\":\"client-1\",\"endpoint_ids\":[\"$E1\",\"$E2\"],\"enabled\":true}")
SUBTOK=$(echo "$body" | jf access_token)
[ -n "$SUBTOK" ] && ok "创建客户端订阅并返回一次性访问令牌" || no "订阅创建失败: $body"
if [ -n "$SUBTOK" ]; then
  sc=$(curl -s -o /tmp/sb_sub -w '%{http_code}' --cacert "$CA" "$BASE/api/v1/subscriptions/access/$SUBTOK")
  [ "$sc" = 200 ] && [ -s /tmp/sb_sub ] && ok "凭访问令牌取回订阅内容（预览可用）" || no "订阅访问失败（$sc）"
fi

sect "P-6  指标能力矩阵与当前连接"
body=$(req GET "/api/v1/nodes/$NODEID/connections" "$CSRF")
C=$(code)
if [ "$C" = 200 ]; then
  echo "$body" | python3 -c 'import sys,json;d=json.load(sys.stdin);assert "connections" in d and "capabilities" in d' 2>/dev/null \
    && ok "连接查询返回 connections + capabilities 结构（缺失字段留空）" || no "连接结构不完整: $body"
else no "连接查询失败: $C $body"; fi

sect "P-7  防火墙真实下发 / 幂等（isolated inet sb_control）"
body=$(req POST "/api/v1/nodes/$NODEID/firewall/rules" "$CSRF" '{"action":"drop","protocol":"tcp","cidr":"203.0.113.0/24","port":25,"enabled":true}')
FRID=$(echo "$body" | jf id)
[ -n "$FRID" ] && ok "创建 nftables 规则（drop 203.0.113.0/24:25）" || no "防火墙规则创建失败: $body"
body=$(req POST "/api/v1/nodes/$NODEID/firewall/publish" "$CSRF" '')
TID=$(echo "$body" | jf id)
st=""
for i in $(seq 1 20); do
  tb=$(req "GET" "/api/v1/tasks?node_id=$NODEID" "$CSRF")
  st=$(echo "$tb" | python3 -c 'import sys,json
d=json.load(sys.stdin); ts=d.get("tasks",[])
fw=[t for t in ts if t.get("kind")=="firewall.apply"]
print(fw[0]["status"] if fw else "")' 2>/dev/null)
  [ "$st" = succeeded ] || [ "$st" = failed ] || [ "$st" = rolled_back ] && break
  sleep 1
done
[ "$st" = succeeded ] && ok "agent 应用防火墙任务成功（status=succeeded）" || no "防火墙任务状态=$st"
if sudo nft list table inet sb_control 2>/dev/null | grep -q '203.0.113.0/24'; then ok "内核 nftables 已加载 inet sb_control 规则"; else no "内核未见 sb_control 规则"; fi
# idempotency: republish identical -> agent returns cached success, no error
before=$(req "GET" "/api/v1/tasks?node_id=$NODEID" "$CSRF" | python3 -c 'import sys,json;print(len(json.load(sys.stdin).get("tasks",[])))')
req POST "/api/v1/nodes/$NODEID/firewall/publish" "$CSRF" '' >/dev/null
sleep 3
st2=$(req "GET" "/api/v1/tasks?node_id=$NODEID" "$CSRF" | python3 -c 'import sys,json
d=json.load(sys.stdin); fw=[t for t in d.get("tasks",[]) if t.get("kind")=="firewall.apply"]
print(fw[0]["status"] if fw else "")' 2>/dev/null)
[ "$st2" = succeeded ] && ok "重复发布保持幂等（再次 succeeded，无副作用）" || no "幂等发布状态=$st2"

sect "P-1(续)  控制通道重连"
sudo kill "$AGENT_PID" 2>/dev/null; sleep 2
sudo nohup "$BIN" agent run --data-dir agent-data --master "$BASE" --master-ca "$CA" --heartbeat-interval 5s >agent2.log 2>&1 &
AGENT_PID=$!
# add a new rule and publish; success proves the reconnected control channel dispatches tasks
req POST "/api/v1/nodes/$NODEID/firewall/rules" "$CSRF" '{"action":"drop","protocol":"udp","cidr":"198.51.100.0/24","port":53,"enabled":true}' >/dev/null
sleep 3
req POST "/api/v1/nodes/$NODEID/firewall/publish" "$CSRF" '' >/dev/null
st3=""
for i in $(seq 1 20); do
  st3=$(req "GET" "/api/v1/tasks?node_id=$NODEID" "$CSRF" | python3 -c 'import sys,json
d=json.load(sys.stdin); fw=[t for t in d.get("tasks",[]) if t.get("kind")=="firewall.apply"]
print(fw[0]["status"] if fw else "")' 2>/dev/null)
  [ "$st3" = succeeded ] && break; sleep 1
done
if [ "$st3" = succeeded ] && sudo nft list table inet sb_control 2>/dev/null | grep -q '198.51.100.0/24'; then
  ok "重启 agent 后控制通道重连并成功执行新任务"
else no "重连后任务执行失败（status=$st3）"; fi
