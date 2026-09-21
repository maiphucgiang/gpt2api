#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
MODE=${MODE:-docker}
PORT=${PORT:-3000}
while (($#)); do
  case "$1" in
    --mode) MODE=${2:?missing mode}; shift 2 ;;
    --port) PORT=${2:?missing port}; shift 2 ;;
    --help|-h) printf 'Usage: bash deploy/install.sh [--mode docker|go] [--port 3000]\n'; exit 0 ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done
[[ "$PORT" =~ ^[0-9]+$ ]] && ((10#$PORT > 0 && 10#$PORT < 65536)) || { echo 'Invalid port' >&2; exit 2; }
[[ "$MODE" == docker || "$MODE" == go ]] || { echo 'Mode must be docker or go' >&2; exit 2; }
cd "$ROOT"
umask 077
if [[ "$MODE" == go ]]; then
  command -v go >/dev/null
  command -v pnpm >/dev/null
  mkdir -p data logs bin
  pnpm --dir web-vue install --frozen-lockfile --ignore-scripts
  pnpm --dir web-vue run build:server
  go build -mod=readonly -trimpath -o bin/gpt2api ./cmd/gpt2api
  export GO_LISTEN_ADDR="127.0.0.1:$PORT"
  exec ./bin/gpt2api
fi
command -v docker >/dev/null
export GPT2API_PORT="$PORT"
export GPT2API_UID=${GPT2API_UID:-$(id -u)} GPT2API_GID=${GPT2API_GID:-$(id -g)}
if [[ "$GPT2API_UID" == 0 ]]; then
  export GPT2API_UID=10001 GPT2API_GID=10001
fi
for directory in data logs; do
  if [[ ! -e "$directory" ]]; then
    if [[ $(id -u) == 0 ]]; then
      install -d -m 0700 -o "$GPT2API_UID" -g "$GPT2API_GID" "$directory"
    else
      mkdir "$directory"
    fi
  fi
done
# Copy only missing configuration; existing volumes and keys remain authoritative.
for file in config.json auth_keys.json; do
  target="data/$file"
  created=false
  if [[ ! -e "$target" ]]; then
    if [[ "$file" == config.json && -f config.json ]]; then
      (set -o noclobber; cat config.json > "$target")
      created=true
    elif [[ "$file" == auth_keys.json ]]; then
      (set -o noclobber; printf '[]\n' > "$target")
      created=true
    fi
  fi
  if [[ "$created" == true && $(id -u) == 0 ]]; then
    chown "$GPT2API_UID:$GPT2API_GID" "$target"
  fi
done
docker compose config --quiet
docker compose up -d --build
printf 'gpt2api: http://127.0.0.1:%s (configure authentication before use)\n' "$PORT"
