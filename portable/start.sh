#!/bin/sh
# Locate the package even when launched from another working directory.
set -u
case $0 in
  */*) SCRIPT_DIR=${0%/*}; [ -n "$SCRIPT_DIR" ] || SCRIPT_DIR=/ ;;
  *) SCRIPT_DIR=. ;;
esac
PACKAGE_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR" && pwd) || exit 1
exec "$PACKAGE_ROOT/artex" -mode portable "$@"
