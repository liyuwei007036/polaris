#!/bin/sh
# Deploys this build to a Linux host and checks the behaviour that cannot be
# reproduced on a developer workstation: Nginx SNI routing for shared ports,
# Fail2Ban installation and jail startup, and the agent's real-time push.
#
# Usage, from the repository root:
#   ./scripts_remote_verify.sh root@172.220.0.25
#
# Authentication is left to ssh, so use a key or an agent. The script never
# handles a password.
set -eu

TARGET="${1:?usage: scripts_remote_verify.sh user@host}"
REMOTE_DIR="${SB_REMOTE_DIR:-/opt/sb-control-verify}"
WEB_PORT="${SB_WEB_PORT:-18080}"
AGENT_PORT="${SB_AGENT_PORT:-18443}"
BINARY="artifacts/sb-control-linux-amd64"

echo "==> building web assets and the Linux binary"
npm --prefix webui run build
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o "$BINARY" ./cmd/sb-control

echo "==> uploading to $TARGET:$REMOTE_DIR"
ssh "$TARGET" "mkdir -p $REMOTE_DIR"
scp "$BINARY" "$TARGET:$REMOTE_DIR/sb-control"
ssh "$TARGET" "chmod +x $REMOTE_DIR/sb-control"

echo "==> starting master and agent in combined mode"
ssh "$TARGET" "sh -s" <<REMOTE
set -eu
cd "$REMOTE_DIR"
pkill -f "$REMOTE_DIR/sb-control" 2>/dev/null || true
sleep 1
rm -rf data
nohup ./sb-control combined \
  --data-dir data \
  --web-port $WEB_PORT \
  --agent-port $AGENT_PORT \
  --allow-insecure-http >combined.log 2>&1 &
sleep 6
echo "--- startup log ---"
tail -n 30 combined.log
REMOTE

echo
echo "==> waiting for the node to come online and apply its configuration"
ssh "$TARGET" "sh -s" <<REMOTE
set -eu
cd "$REMOTE_DIR"
for i in \$(seq 1 40); do
  if grep -q "listening" combined.log 2>/dev/null; then break; fi
  sleep 1
done
echo "--- sing-box managed configuration ---"
[ -f /etc/sing-box/config.json ] && head -30 /etc/sing-box/config.json || echo "(not written yet)"
echo
echo "--- sing-box log output setting ---"
grep -o '"output"[^,}]*' /etc/sing-box/config.json 2>/dev/null || echo "(none)"
echo
echo "--- managed nginx stream configuration ---"
[ -f /etc/nginx/stream-conf.d/sb-control.conf ] && cat /etc/nginx/stream-conf.d/sb-control.conf || echo "(no shared-port routing configured yet)"
echo
echo "--- nginx syntax check ---"
command -v nginx >/dev/null && nginx -t 2>&1 || echo "(nginx not installed)"
echo
echo "--- fail2ban ---"
command -v fail2ban-client >/dev/null && fail2ban-client status 2>&1 || echo "(fail2ban not installed yet; it installs on first publish)"
echo
echo "--- services ---"
systemctl is-active sing-box nginx fail2ban 2>&1 || true
echo
echo "--- task results reported by the agent ---"
tail -n 40 combined.log
REMOTE

echo
echo "Console: http://\${TARGET#*@}:$WEB_PORT  (default login sb_admin / 123456)"
echo "To stop:  ssh $TARGET 'pkill -f $REMOTE_DIR/sb-control'"
