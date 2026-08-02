#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")" && pwd)
GO_BINARY=${GO_BINARY:-go}

cd "$ROOT"
"$GO_BINARY" test -tags=e2e -count=1 -v ./e2e
npm --prefix webui run test:e2e
