#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
case "${1:-}" in
  test|build|static)
    bash scripts/check-backend.sh "$1"
    bash scripts/check-frontend.sh "$1"
    ;;
  *) echo 'Usage: bash scripts/check.sh test|build|static' >&2; exit 2 ;;
esac
