# Ubuntu 部署

推荐使用 [Docker 安装](../README.md#docker-启动)。先确认数据备份、端口与 HTTPS 反向代理配置，再启动新实例。

## 原生服务

准备 Go 1.23+、Node.js 26 和 pnpm 12.5.1，构建主服务：

```bash
pnpm --dir web-vue install --frozen-lockfile --ignore-scripts
pnpm --dir web-vue run build:server
mkdir -p bin data logs
go build -mod=readonly -o bin/gpt2api ./cmd/gpt2api
```

1. 选用专门的非 root 运行账号，确保它可读程序、可写 `data/` 和 `logs/`。
2. 在私有 `.env` 中设置 `GPT2API_AUTH_KEY`、`GPT2API_ADMIN_KEY`，文件权限为 `600`。
3. 复制 `deploy/systemd/gpt2api.service.example` 为 `gpt2api.service`，替换 `__APP_DIR__`、`__RUN_USER__` 和 `__RUN_GROUP__`。
4. 检查模板后安装到 `/etc/systemd/system/`，使用 `systemctl daemon-reload` 与 `systemctl enable --now gpt2api` 启用。

模板监听 `127.0.0.1:8000`。Nginx/Caddy 应在公网终止 TLS，转发到该地址并为流式请求关闭响应缓冲。

```bash
systemctl status gpt2api --no-pager
journalctl -u gpt2api -n 100 --no-pager
bash scripts/verify_live.sh http://127.0.0.1:8000
```

不要同时运行多个写入同一 JSON 数据目录的主服务。图片队列使用独立 Redis，只有启用 `images` profile 时才需要。

升级前按 [MIGRATION.md](MIGRATION.md) 备份、排空请求并保留旧镜像或二进制。
