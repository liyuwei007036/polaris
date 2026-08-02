#!/usr/bin/env bash
set -euo pipefail

# Backward-compatible entrypoint. The old TLS/mTLS verification flow no
# longer matches the Noise-encrypted dual-port architecture.
exec bash "$(cd "$(dirname "$0")" && pwd)/scripts_e2e.sh" "$@"
