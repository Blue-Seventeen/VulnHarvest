#!/bin/sh
# Generic compatibility launcher; mode follows CLI/env/config/default.
set -u
case $0 in
  */*) SCRIPT_DIR=${0%/*}; [ -n "$SCRIPT_DIR" ] || SCRIPT_DIR=/ ;;
  *) SCRIPT_DIR=. ;;
esac
ROOT=$(CDPATH= cd -- "$SCRIPT_DIR" && pwd) || exit 1
exec "$ROOT/artex" "$@"
