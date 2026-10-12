#!/bin/sh
# Locate the package even when launched from another working directory.
set -u
PACKAGE_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd) || exit 1
exec "$PACKAGE_ROOT/artex" -mode portable "$@"
