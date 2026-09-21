#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
bash scripts/check.sh test
bash scripts/check.sh build
bash scripts/check.sh static
