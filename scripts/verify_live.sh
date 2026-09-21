#!/usr/bin/env bash
set -euo pipefail
BASE=${1:-http://127.0.0.1:3000}
BASE=${BASE%/}
curl --fail --silent --show-error --max-time 10 "$BASE/health" >/dev/null
code=$(curl --silent --show-error --max-time 10 -o /dev/null -w '%{http_code}' "$BASE/v1/models")
[[ "$code" == 401 ]] || { printf 'Unauthenticated models endpoint returned %s, expected 401\n' "$code" >&2; exit 1; }
if [[ -n ${GPT2API_AUTH_KEY:-} ]]; then
  printf 'Authorization: Bearer %s\n' "$GPT2API_AUTH_KEY" |
    curl --fail --silent --show-error --max-time 10 --header @- "$BASE/v1/models" >/dev/null
fi
printf 'Health and authentication checks passed.\n'
