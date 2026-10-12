#!/usr/bin/env bash
# Build-host wrapper. The archives themselves need neither Go nor Node.
set -euo pipefail
cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
exec go run ./cmd/portablebuild "$@"
