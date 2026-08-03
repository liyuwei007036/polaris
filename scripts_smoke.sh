#!/usr/bin/env bash
# Throwaway authenticated smoke test against a fresh master instance.
set -u
BIN=/home/pi/sbtest/sb-control.new
W=/tmp/sbv
PORT=8553
AGENT_PORT=8554
BASE=http://127.0.0.1:$PORT
PW='Verify-Pass-7731'
export PATH=$PATH:/usr/local/go/bin
rm -rf "$W"; mkdir -p "$W"; cd "$W"

pass=0; fail=0
ok(){ echo "PASS  $*"; pass=$((pass+1)); }
no(){ echo "FAIL  $*"; fail=$((fail+1)); }

totp(){ python3 - "$1" <<'PY'
import sys,hmac,hashlib,base64,struct,time
s=sys.argv[1].upper(); s+='='*((8-len(s)%8)%8)
k=base64.b32decode(s); c=int(time.time())//30
mac=hmac.new(k,struct.pack('>Q',c),hashlib.sha1).digest()
o=mac[-1]&0x0f
print('%06d'%((struct.unpack('>I',mac[o:o+4])[0]&0x7fffffff)%1000000))
PY
}
jf(){ python3 -c 'import sys,json
try:
 d=json.load(sys.stdin); print(d.get(sys.argv[1],"") if isinstance(d,dict) else "")
except: print("")' "$1"; }

SECRET=$("$BIN" master init-admin --data-dir data --email admin@example.com --password-stdin <<<"$PW" 2>err.log | tail -1)
[ ${#SECRET} -ge 16 ] && ok "init-admin (secret len ${#SECRET})" || no "init-admin: $(cat err.log)"

# No certificate needed anywhere: agent traffic is Noise-encrypted (raw
# public keys, auto-managed by master), and the browser listener is plain
# HTTP by design (put a reverse proxy in front for public HTTPS).
nohup "$BIN" master serve --data-dir data --agent-port "$AGENT_PORT" --web-port "$PORT" --allow-insecure-http >serve.log 2>&1 &
MPID=$!
for i in $(seq 1 40); do
  c=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/auth/login" -H 'Content-Type: application/json' -d '{"email":"x","password":"y"}' 2>/dev/null)
  [ -n "$c" ] && [ "$c" != 000 ] && break; sleep 0.3
done

CJ=/tmp/sbv_cookies; rm -f $CJ
RESP=/tmp/sbv_resp
# req prints the HTTP status code to stdout; response body is written to $RESP.
req(){ local m=$1 p=$2 c=${3:-} b=${4:-}
  local a=(-s -o $RESP -w '%{http_code}' -X "$m" "$BASE$p" -b $CJ -c $CJ)
  [ -n "$c" ] && a+=(-H "X-CSRF-Token: $c")
  [ -n "$b" ] && a+=(-H 'Content-Type: application/json' -d "$b")
  curl "${a[@]}"
}
ok2xx(){ [ "$1" = 200 ] || [ "$1" = 201 ] || [ "$1" = 204 ]; }

code=$(req POST /api/v1/auth/login "" "{\"email\":\"admin@example.com\",\"password\":\"$PW\"}"); body=$(cat $RESP)
CH=$(echo "$body"|jf challenge_id)
[ "$code" = 200 ] && [ -n "$CH" ] && ok "login -> challenge (200)" || no "login: $code $body"
CODE=$(totp "$SECRET")
code=$(req POST /api/v1/auth/mfa "" "{\"challenge_id\":\"$CH\",\"code\":\"$CODE\"}"); body=$(cat $RESP)
CSRF=$(echo "$body"|jf csrf_token); ROLE=$(echo "$body"|jf role)
[ "$code" = 200 ] && [ "$ROLE" = admin ] && [ -n "$CSRF" ] && ok "mfa -> admin session + CSRF (200)" || no "mfa: $code $body"

getcheck(){ local p=$1 key=$2; local code=$(req GET "$p" "$CSRF"); local b=$(cat $RESP)
  if [ "$code" = 200 ]; then
    if echo "$b"|python3 -c 'import sys,json
try:
 d=json.load(sys.stdin)
except Exception:
 sys.exit(1)
sys.exit(0 if (isinstance(d,dict) and sys.argv[1] in d) else 1)' "$key" 2>/dev/null; then ok "GET $p -> 200, has \"$key\""; else no "GET $p 200 but missing \"$key\": $b"; fi
  else no "GET $p -> $code (expect 200): $b"; fi; }

echo "-- read endpoints (the 随便一点 surface) --"
getcheck /api/v1/subscriptions subscriptions
getcheck /api/v1/nodes nodes
getcheck /api/v1/listeners listeners
getcheck /api/v1/tasks tasks
getcheck /api/v1/operators operators
getcheck /api/v1/reality-keys reality_keys
getcheck /api/v1/sing-box/releases releases
getcheck /api/v1/cloudflare/settings configured
getcheck /api/v1/audit-events audit_events
getcheck /api/v1/registrations registrations

echo "-- create flows (node-less) --"
code=$(req POST /api/v1/reality-keys "$CSRF" '{"name":"vk-1"}'); b=$(cat $RESP); ok2xx "$code" && [ -n "$(echo "$b"|jf private_key)" ] && ok "POST reality-keys -> private_key" || no "reality-keys: $code $b"
code=$(req POST /api/v1/operators "$CSRF" '{"email":"op1@example.com","password":"Op1-pass-123","role":"operator"}'); b=$(cat $RESP); ok2xx "$code" && [ -n "$(echo "$b"|jf totp_secret)" ] && ok "POST operators -> totp_secret" || no "operators: $code $b"
SHA=$(printf 'a%.0s' $(seq 1 64))
code=$(req POST /api/v1/sing-box/releases "$CSRF" "{\"version\":\"1.9.0\",\"architecture\":\"arm64\",\"url\":\"https://example.com/x\",\"sha256\":\"$SHA\",\"enabled\":true}"); b=$(cat $RESP); ok2xx "$code" && ok "POST sing-box/releases" || no "release: $code $b"
code=$(req PUT /api/v1/cloudflare/settings "$CSRF" '{"zone_id":"z123","zone_name":"example.com","api_token":"tok-abc"}'); b=$(cat $RESP); ok2xx "$code" && ok "PUT cloudflare/settings" || no "cf settings: $code $b"
# verify a client subscription can be created once an endpoint would exist: check empty-list 200 already covered.

echo "-- managed outbound library (global, not tied to a node) --"
getcheck /api/v1/outbounds outbounds
code=$(req POST /api/v1/outbounds "$CSRF" '{"name":"o1","type":"socks","server":"1.2.3.4","server_port":1080,"username":"","password":"","enabled":true}'); b=$(cat $RESP)
ok2xx "$code" && ok "POST outbounds (no node_id required) -> $code" || no "outbound create: $code $b"
code=$(req POST /api/v1/outbounds "$CSRF" '{"name":"o2","type":"socks","server":"","server_port":0,"username":"","password":"","enabled":true}'); b=$(cat $RESP)
[ "$code" = 400 ] && ok "POST outbounds missing server -> 400" || no "outbound validation: $code $b"
code=$(req POST /api/v1/outbounds "$CSRF" '{"name":"o1","type":"socks","server":"1.2.3.4","server_port":1080,"username":"","password":"","enabled":true}'); b=$(cat $RESP)
[ "$code" = 409 ] && ok "POST outbounds duplicate name -> 409" || no "outbound duplicate: $code $b"
UC=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/v1/outbounds")
[ "$UC" = 401 ] && ok "GET outbounds unauth -> 401" || no "outbound unauth: $UC"
HC=$(curl -s "$BASE/" | grep -c "出站方式")
[ "$HC" -ge 1 ] && ok "GET / serves outbound nav (matches=$HC)" || no "outbound nav missing in served HTML"

echo "---- RESULT pass=$pass fail=$fail ----"
kill $MPID 2>/dev/null; sleep 1
cd /tmp; rm -rf "$W" /tmp/sbv_cookies /tmp/sbv_resp
