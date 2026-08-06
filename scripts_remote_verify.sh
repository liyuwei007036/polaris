#!/bin/sh
# Deploys this build to a Linux host and checks what a developer workstation
# cannot: that the agent installs and configures Nginx, nftables and Fail2Ban
# by itself, with no manual step on the target.
#
# Usage, from the repository root:
#   ./scripts_remote_verify.sh root@192.0.2.10
#
# Pass --from-scratch to purge Nginx and Fail2Ban first, which proves the
# agent can bring a bare host all the way up on its own. This removes those
# packages from the target, so only use it on a host dedicated to testing.
#
# Authentication is left to ssh, so use a key or an agent. This script never
# handles a password.
set -eu

TARGET=""
FROM_SCRATCH=0
for argument in "$@"; do
    case "$argument" in
        --from-scratch) FROM_SCRATCH=1 ;;
        *) TARGET="$argument" ;;
    esac
done
[ -n "$TARGET" ] || { echo "usage: scripts_remote_verify.sh [--from-scratch] user@host" >&2; exit 2; }

REMOTE_DIR="${SB_REMOTE_DIR:-/opt/sb-control-verify}"
WEB_PORT="${SB_WEB_PORT:-18080}"
AGENT_PORT="${SB_AGENT_PORT:-18443}"
BINARY="artifacts/sb-control-linux-amd64"

echo "==> building web assets and the Linux binary"
npm --prefix webui run build
mkdir -p artifacts
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o "$BINARY" ./cmd/sb-control

echo "==> uploading to $TARGET:$REMOTE_DIR"
# The pkill pattern is bracketed so it cannot match the shell running it.
ssh "$TARGET" "mkdir -p $REMOTE_DIR; pkill -f 'sb-control com[b]ined' >/dev/null 2>&1; sleep 1; exit 0"
scp "$BINARY" "$TARGET:$REMOTE_DIR/sb-control"
ssh "$TARGET" "chmod +x $REMOTE_DIR/sb-control"

if [ "$FROM_SCRATCH" -eq 1 ]; then
    echo "==> purging Nginx and Fail2Ban so the agent has to install them itself"
    ssh "$TARGET" "sh -s" <<'REMOTE'
DEBIAN_FRONTEND=noninteractive apt-get purge -y 'nginx*' 'libnginx*' fail2ban >/tmp/sb-purge.log 2>&1 || true
DEBIAN_FRONTEND=noninteractive apt-get autoremove -y >>/tmp/sb-purge.log 2>&1 || true
rm -rf /etc/nginx /usr/lib/nginx /var/log/nginx /etc/fail2ban
systemctl daemon-reload || true
echo "nginx binary: $(command -v nginx || echo GONE)"
echo "fail2ban binary: $(command -v fail2ban-client || echo GONE)"
REMOTE
    # Forget completed tasks so the agent re-runs them instead of replaying results.
    ssh "$TARGET" "rm -rf $REMOTE_DIR/agent-data/completed-tasks $REMOTE_DIR/agent-data/desired-state"
fi

echo "==> starting master and agent"
ssh "$TARGET" "sh -s" <<REMOTE
set -eu
cd "$REMOTE_DIR"
PUB=\$(./sb-control master show-pubkey --data-dir data)
setsid nohup ./sb-control combined serve \
  --master-data-dir data --agent-data-dir agent-data \
  --web-port $WEB_PORT --agent-port $AGENT_PORT \
  --master 127.0.0.1:$AGENT_PORT --master-pubkey "\$PUB" \
  --allow-insecure-http >combined.log 2>&1 </dev/null &
sleep 10
tail -n 5 combined.log
REMOTE

echo
echo "==> what the agent produced on the target (nothing below is written by hand)"
ssh "$TARGET" "sh -s" <<'REMOTE'
echo "--- sing-box managed configuration ---"
[ -f /etc/sing-box/config.json ] && grep -o '"output"[^,}]*' /etc/sing-box/config.json || echo "(sing-box not installed yet)"
echo
echo "--- managed Nginx stream routing ---"
[ -f /etc/nginx/stream-conf.d/sb-control.conf ] && cat /etc/nginx/stream-conf.d/sb-control.conf || echo "(no shared-port routing configured)"
command -v nginx >/dev/null && nginx -t 2>&1 || echo "(nginx not installed)"
echo "port 80 exposed? $(ss -lnt 2>/dev/null | grep -q ':80 ' && echo YES-PROBLEM || echo no)"
echo
echo "--- managed firewall ---"
nft list table inet sb_control 2>&1 | sed -n '1,20p'
echo "boot unit: $(systemctl is-enabled sb-control-nftables.service 2>&1)"
echo
echo "--- Fail2Ban ---"
command -v fail2ban-client >/dev/null && fail2ban-client status 2>&1 || echo "(fail2ban not installed; it installs on first publish)"
echo "jails not owned by sb-control are left untouched:"
ls /etc/fail2ban/jail.d/ 2>&1
echo
echo "--- services ---"
systemctl is-active nginx fail2ban sing-box 2>&1 || true
REMOTE

echo
echo "Console: http://${TARGET#*@}:$WEB_PORT"
echo "To stop:  ssh $TARGET \"pkill -f 'sb-control com[b]ined'\""
