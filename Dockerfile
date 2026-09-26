FROM node:22-alpine AS web-build
WORKDIR /src/web-vue
# Corepack caches only the wrapper; prime the verified native binary separately.
RUN corepack enable \
    && timeout 120 corepack prepare pnpm@12.5.1 --activate \
    && test "$(timeout 120 pnpm --version)" = "12.5.1"
ENV COREPACK_ENABLE_NETWORK=0
COPY web-vue/package.json web-vue/pnpm-lock.yaml web-vue/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY VERSION CHANGELOG.md /src/
COPY web-vue ./
RUN pnpm run build

FROM golang:1.23-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -ldflags="-s -w" -o /gpt2api ./cmd/gpt2api

FROM alpine:3.21 AS app
RUN adduser -D -H -u 10001 app
WORKDIR /app
COPY --from=go-build /gpt2api /app/gpt2api
COPY --from=web-build /src/web-vue/dist /app/web_dist
COPY VERSION CHANGELOG.md config.example.json ./
COPY services/default_prompt_library.json /app/services/default_prompt_library.json
RUN mkdir -p /app/data /app/logs && chown -R app:app /app
USER app
ENV GO_LISTEN_ADDR=:80 \
    GO_STATIC_DIR=/app/web_dist \
    GPT2API_DATA_DIR=/app/data
EXPOSE 80
ENTRYPOINT ["/app/gpt2api"]
