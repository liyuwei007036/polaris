#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf -- "$WORK"' EXIT

bash -n "$ROOT/install.sh"

PACKAGE="$WORK/package"
mkdir -p "$PACKAGE/deploy"
cp "$ROOT/install.sh" "$PACKAGE/install.sh"
cp "$ROOT"/deploy/* "$PACKAGE/deploy/"
printf '#!/usr/bin/env sh\nexit 0\n' >"$PACKAGE/sb-control"
chmod 0755 "$PACKAGE/install.sh" "$PACKAGE/sb-control"

MASTER_ROOT="$WORK/master-root"
DESTDIR="$MASTER_ROOT" "$PACKAGE/install.sh" master --no-start

test -x "$MASTER_ROOT/usr/local/bin/sb-control"
test -f "$MASTER_ROOT/etc/sb-control/master.yaml"
test -f "$MASTER_ROOT/etc/systemd/system/sb-control-master.service"
test -f "$MASTER_ROOT/etc/systemd/system/sb-control-agent.service"
test -f "$MASTER_ROOT/etc/systemd/system/sb-control-combined.service"
test "$(stat -c '%a' "$MASTER_ROOT/usr/local/bin/sb-control")" = "755"
test "$(stat -c '%a' "$MASTER_ROOT/etc/sb-control/master.yaml")" = "640"
grep -q '^allow_insecure_http: false$' "$MASTER_ROOT/etc/sb-control/master.yaml"

printf 'preserve: true\n' >>"$MASTER_ROOT/etc/sb-control/master.yaml"
DESTDIR="$MASTER_ROOT" "$PACKAGE/install.sh" master --allow-insecure-http --no-start
grep -q '^preserve: true$' "$MASTER_ROOT/etc/sb-control/master.yaml"
grep -q '^allow_insecure_http: false$' "$MASTER_ROOT/etc/sb-control/master.yaml"

INSECURE_ROOT="$WORK/insecure-root"
DESTDIR="$INSECURE_ROOT" "$PACKAGE/install.sh" master --allow-insecure-http --no-start
grep -q '^allow_insecure_http: true$' "$INSECURE_ROOT/etc/sb-control/master.yaml"

AGENT_ROOT="$WORK/agent-root"
DESTDIR="$AGENT_ROOT" \
SB_CONTROL_MASTER_ADDRESS="control.example.com:8443" \
SB_CONTROL_MASTER_PUBKEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" \
  "$PACKAGE/install.sh" agent --no-start
test "$(stat -c '%a' "$AGENT_ROOT/etc/sb-control/agent.yaml")" = "600"
grep -q "^master_address: 'control.example.com:8443'$" "$AGENT_ROOT/etc/sb-control/agent.yaml"
grep -q "^master_public_key: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='$" \
  "$AGENT_ROOT/etc/sb-control/agent.yaml"

DESTDIR="$AGENT_ROOT" "$PACKAGE/install.sh" agent --no-start

INVALID_ROOT="$WORK/invalid-root"
if DESTDIR="$INVALID_ROOT" \
  SB_CONTROL_MASTER_ADDRESS="control.example.com:8443" \
  SB_CONTROL_MASTER_PUBKEY="invalid" \
  "$PACKAGE/install.sh" agent --no-start >/dev/null 2>&1; then
  printf 'install.sh accepted an invalid Master public key\n' >&2
  exit 1
fi

printf 'install.sh isolated installation test passed\n'
