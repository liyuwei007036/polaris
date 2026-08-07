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
printf '#!/usr/bin/env sh\nexit 0\n' >"$PACKAGE/polaris"
chmod 0755 "$PACKAGE/install.sh" "$PACKAGE/polaris"

MASTER_ROOT="$WORK/master-root"
DESTDIR="$MASTER_ROOT" "$PACKAGE/install.sh" master --no-start

test -x "$MASTER_ROOT/usr/local/bin/polaris"
test -f "$MASTER_ROOT/etc/polaris/master.yaml"
test -f "$MASTER_ROOT/etc/systemd/system/polaris-master.service"
test -f "$MASTER_ROOT/etc/systemd/system/polaris-agent.service"
test -f "$MASTER_ROOT/etc/systemd/system/polaris-combined.service"
test "$(stat -c '%a' "$MASTER_ROOT/usr/local/bin/polaris")" = "755"
test "$(stat -c '%a' "$MASTER_ROOT/etc/polaris/master.yaml")" = "640"
grep -q '^allow_insecure_http: false$' "$MASTER_ROOT/etc/polaris/master.yaml"

printf 'preserve: true\n' >>"$MASTER_ROOT/etc/polaris/master.yaml"
DESTDIR="$MASTER_ROOT" "$PACKAGE/install.sh" master --allow-insecure-http --no-start
grep -q '^preserve: true$' "$MASTER_ROOT/etc/polaris/master.yaml"
grep -q '^allow_insecure_http: false$' "$MASTER_ROOT/etc/polaris/master.yaml"

INSECURE_ROOT="$WORK/insecure-root"
DESTDIR="$INSECURE_ROOT" "$PACKAGE/install.sh" master --allow-insecure-http --no-start
grep -q '^allow_insecure_http: true$' "$INSECURE_ROOT/etc/polaris/master.yaml"

AGENT_ROOT="$WORK/agent-root"
DESTDIR="$AGENT_ROOT" \
POLARIS_MASTER_ADDRESS="control.example.com:8443" \
POLARIS_MASTER_PUBKEY="AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" \
  "$PACKAGE/install.sh" agent --no-start
test "$(stat -c '%a' "$AGENT_ROOT/etc/polaris/agent.yaml")" = "600"
grep -q "^master_address: 'control.example.com:8443'$" "$AGENT_ROOT/etc/polaris/agent.yaml"
grep -q "^master_public_key: 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA='$" \
  "$AGENT_ROOT/etc/polaris/agent.yaml"

DESTDIR="$AGENT_ROOT" "$PACKAGE/install.sh" agent --no-start

INVALID_ROOT="$WORK/invalid-root"
if DESTDIR="$INVALID_ROOT" \
  POLARIS_MASTER_ADDRESS="control.example.com:8443" \
  POLARIS_MASTER_PUBKEY="invalid" \
  "$PACKAGE/install.sh" agent --no-start >/dev/null 2>&1; then
  printf 'install.sh accepted an invalid Master public key\n' >&2
  exit 1
fi

printf 'install.sh isolated installation test passed\n'
