#!/bin/sh
# Generic compatibility launcher; mode follows CLI/env/config/default.
set -u
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || exit 1
exec "$ROOT/artex" "$@"
