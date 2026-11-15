FROM node:22-alpine AS web-build
WORKDIR /src/web-vue
# Keep the verified native binary across base-image updates, partitioned by architecture.
ARG TARGETARCH
RUN --mount=type=cache,id=gpt2api-pnpm-12.5.1-${TARGETARCH},target=/pnpm-bootstrap-cache \
    corepack enable \
    && COREPACK_HOME=/pnpm-bootstrap-cache timeout 120 corepack prepare pnpm@12.5.1 --activate \
    && test "$(COREPACK_HOME=/pnpm-bootstrap-cache timeout 420 pnpm --version)" = "12.5.1" \
    && mkdir -p /opt/corepack \
    && cp -a /pnpm-bootstrap-cache/. /opt/corepack/
ENV COREPACK_HOME=/opt/corepack COREPACK_ENABLE_NETWORK=0
COPY web-vue/package.json web-vue/pnpm-lock.yaml web-vue/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY VERSION CHANGELOG.md /src/
COPY web-vue ./
RUN pnpm run build

FROM golang:1.27.3-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
# Module downloads stay valid when only application source changes.
ARG TARGETARCH
RUN --mount=type=cache,id=gpt2api-go-modules,target=/go/pkg/mod,sharing=locked \
    timeout 300 go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,id=gpt2api-go-modules,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,id=gpt2api-go-build-${TARGETARCH},target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -mod=readonly -trimpath -ldflags="-s -w" -o /gpt2api ./cmd/gpt2api

FROM alpine:3.21 AS app
# CVE-2026-111943: reject compromised eMudhra intermediates even from stale base caches.
RUN apk add --no-cache 'ca-certificates-bundle>=20261020-r0'
RUN adduser -D -H -u 10001 app
WORKDIR /app
COPY --from=go-build /gpt2api /app/gpt2api
COPY --from=web-build /src/web-vue/dist /app/web_dist
COPY VERSION CHANGELOG.md config.example.json ./
COPY LICENSE THIRD_PARTY_LICENSE ./
COPY services/default_prompt_library.json /app/services/default_prompt_library.json
RUN mkdir -p /app/data /app/logs && chown -R app:app /app
USER app
ENV GO_LISTEN_ADDR=:80 \
    GO_STATIC_DIR=/app/web_dist \
    GPT2API_DATA_DIR=/app/data
EXPOSE 80
ENTRYPOINT ["/app/gpt2api"]
