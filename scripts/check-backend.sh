#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
modules=(. go-image-gateway deploy/icloud-privacy-mail/source)
case "${1:-}" in
  test)
    for module in "${modules[@]}"; do
      (cd "$module" && go test -race -vet=off -count=1 ./...)
    done
    ;;
  build)
    tag=${CHECK_IMAGE_TAG:-check}
    docker build --target app -t "gpt2api:$tag" .
    docker build -t "gpt2api/image-gateway:$tag" go-image-gateway
    docker build -f deploy/icloud-privacy-mail/Dockerfile -t "gpt2api/icloud-privacy-mail:$tag" .
    docker compose config --quiet
    docker compose --profile images --profile local-icloud config --quiet
    docker compose config --format json | jq -e '.services.app.image == "ghcr.io/maiphucgiang/gpt2api:latest" and (.services.app | has("build") | not)' >/dev/null
    IMAGE_SCHEDULER_INTERNAL_KEY=check-internal-key GO_IMAGE_TASK_WORKERS=3 GO_IMAGE_TASK_QUEUE_SIZE=5 GO_IMAGE_TASK_RETENTION_HOURS=12 docker compose --profile images config --format json |
      jq -e '.services as $c | $c.app.environment as $e | ($e.GO_IMAGE_SCHEDULER_KEY == "check-internal-key") and ($c["image-gateway"].environment.IMAGE_GATEWAY_SCHEDULER_KEY == "check-internal-key") and ($e.GO_IMAGE_TASK_WORKERS == "3") and ($e.GO_IMAGE_TASK_QUEUE_SIZE == "5") and ($e.GO_IMAGE_TASK_RETENTION_HOURS == "12")' >/dev/null
    docker compose -f docker-compose.local.yml config --quiet
    docker compose -f docker-compose.local.yml --profile images --profile local-icloud config --quiet
    docker compose -f docker-compose.local.yml config --format json | jq -e '.services.app.image == "gpt2api:local" and (.services.app.build != null)' >/dev/null
    IMAGE_SCHEDULER_INTERNAL_KEY=check-internal-key GO_IMAGE_TASK_WORKERS=3 GO_IMAGE_TASK_QUEUE_SIZE=5 GO_IMAGE_TASK_RETENTION_HOURS=12 docker compose -f docker-compose.local.yml --profile images config --format json |
      jq -e '.services as $c | $c.app.environment as $e | ($e.GO_IMAGE_SCHEDULER_KEY == "check-internal-key") and ($c["image-gateway"].environment.IMAGE_GATEWAY_SCHEDULER_KEY == "check-internal-key") and ($e.GO_IMAGE_TASK_WORKERS == "3") and ($e.GO_IMAGE_TASK_QUEUE_SIZE == "5") and ($e.GO_IMAGE_TASK_RETENTION_HOURS == "12")' >/dev/null
    docker compose -f docker-compose.yml -f docker-compose.warp.yml config --quiet
    docker compose -f docker-compose.local.yml -f docker-compose.warp.yml config --quiet
    docker compose -f docker-compose.yml -f docker-compose.warp.yml config --format json | jq -e '.services["warp-proxy"].image == "ghcr.io/ccbkkb/microwarp:latest"' >/dev/null
    docker compose -f docker-compose.local.yml -f docker-compose.warp.yml config --format json | jq -e '.services.app.image == "gpt2api:local" and .services["warp-proxy"].image == "ghcr.io/ccbkkb/microwarp:latest"' >/dev/null
    ;;
  static)
    for module in "${modules[@]}"; do
      (cd "$module" && go vet ./...)
    done
    unformatted=$(gofmt -l cmd internal go-image-gateway deploy/icloud-privacy-mail/source)
    [[ -z "$unformatted" ]] || { printf 'Unformatted Go files:\n%s\n' "$unformatted" >&2; exit 1; }
    for script in scripts/*.sh deploy/install.sh; do bash -n "$script"; done
    jq -e 'has("auth-key") and has("admin_key") and has("webui_key") and has("public_base_url") and has("request_timeout_seconds") and has("chat_max_retries") and has("image_account_concurrency") and has("image_max_concurrency") and has("image_retention_days") and has("image_task_workers") and has("image_task_queue_size") and has("image_task_retention_hours") and has("editable_model") and has("editable_reasoning_effort") and has("proxy") and has("flaresolverr_url") and has("clearance_enabled") and (has("GPT2API_PORT") | not) and (has("GPT2API_IMAGE") | not) and (has("GPT2API_BIND") | not) and (has("TZ") | not)' config.example.json >/dev/null
    git diff --check HEAD
    ;;
  *) echo 'Usage: bash scripts/check-backend.sh test|build|static' >&2; exit 2 ;;
esac
