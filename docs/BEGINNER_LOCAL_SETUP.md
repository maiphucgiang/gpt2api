# 本地运行

## Docker

1. 安装 Docker 和 Compose 2.24+。
2. 在仓库根目录复制 `.env.example` 为 `.env`，权限设为 `600`，填入不同的 API 与管理密钥。
3. 执行 `bash deploy/install.sh --mode docker --port 3000`。
4. 打开 `http://127.0.0.1:3000`，使用管理密钥登录并添加 GPT 账号。

安装脚本只创建缺少的目录、复制缺少的配置，不改写已有账号和密钥。已有数据目录的属主必须与容器 UID/GID 一致；可通过 `GPT2API_UID`、`GPT2API_GID` 显式指定。

## 源码

需要 Go 1.23+、Node.js 26、pnpm 12.5.1。

```bash
pnpm --dir web-vue install --frozen-lockfile --ignore-scripts
pnpm --dir web-vue run build:server
mkdir -p bin
go build -mod=readonly -o bin/gpt2api ./cmd/gpt2api
GO_LISTEN_ADDR=127.0.0.1:8000 ./bin/gpt2api
```

通过进程环境配置 `GPT2API_AUTH_KEY` 和 `GPT2API_ADMIN_KEY`，或使用权限为 `600` 的 `data/config.json`；源码程序不会自动载入 `.env`。

开发前端可运行 `pnpm --dir web-vue dev`。自动配额检查配置在管理接口 `/api/register/openai/survival`，页面可见时自动读取最新本地状态。

## macOS 后台服务

将 `deploy/launchd/com.gpt2api.app.plist.example` 中的 `__APP_DIR__` 替换为仓库绝对路径，保存到 `~/Library/LaunchAgents/com.gpt2api.app.plist`。先构建二进制并设置私有的 `data/config.json`，然后按 macOS 的 launchd 管理方式加载。模板不在 plist 中保存密钥。

## 可选组件

- 图片队列：`.env` 设置内部调度密钥，使用 `docker compose --profile images up -d --build`。
- 本地邮箱：使用 `docker compose --profile local-icloud up -d --build`。
- 两者同时启用：在 `.env` 设置 `COMPOSE_PROFILES=images,local-icloud`。

升级和回退见 [MIGRATION.md](MIGRATION.md)。
