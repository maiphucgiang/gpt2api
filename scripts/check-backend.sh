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
    IMAGE_SCHEDULER_INTERNAL_KEY=check-internal-key GO_IMAGE_TASK_WORKERS=3 GO_IMAGE_TASK_QUEUE_SIZE=5 GO_IMAGE_TASK_RETENTION_HOURS=12 docker compose --profile images config --format json |
      jq -e '.services as $c | $c.app.environment as $e | ($e.GO_IMAGE_SCHEDULER_KEY == "check-internal-key") and ($c["image-gateway"].environment.IMAGE_GATEWAY_SCHEDULER_KEY == "check-internal-key") and ($e.GO_IMAGE_TASK_WORKERS == "3") and ($e.GO_IMAGE_TASK_QUEUE_SIZE == "5") and ($e.GO_IMAGE_TASK_RETENTION_HOURS == "12")' >/dev/null
    docker compose -f docker-compose.yml -f docker-compose.local.yml config --quiet
    docker compose -f docker-compose.yml -f docker-compose.warp.yml config --quiet
    ;;
  static)
    for module in "${modules[@]}"; do
      (cd "$module" && go vet ./...)
    done
    unformatted=$(gofmt -l cmd internal go-image-gateway deploy/icloud-privacy-mail/source)
    [[ -z "$unformatted" ]] || { printf 'Unformatted Go files:\n%s\n' "$unformatted" >&2; exit 1; }
    for script in scripts/*.sh deploy/install.sh; do bash -n "$script"; done
    git diff --check HEAD
    ;;
  *) echo 'Usage: bash scripts/check-backend.sh test|build|static' >&2; exit 2 ;;
esac
