#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
case "${1:-}" in
  test)
    pnpm --dir web-vue exec vp test --run
    ;;
  build)
    pnpm --dir web-vue exec vp build
    ;;
  static)
    pnpm --dir web-vue exec vue-tsc --noEmit
    git diff --check HEAD
    ;;
  *) echo 'Usage: bash scripts/check-frontend.sh test|build|static' >&2; exit 2 ;;
esac
