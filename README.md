# gpt2api

Go 后端与 Vue 控制台，提供 GPT 账号管理和 OpenAI 兼容接口。

## 功能

- GPT 聊天、Responses、Anthropic Messages 兼容接口及流式响应。
- GPT 图片生成、编辑、图库与可编辑文件任务；`gpt-image-2.5` 为现有图片链路的兼容入口。
- 账号分组、配额检查、凭证刷新、调用统计、代理与运行日志。
- CPA 仅导入 Codex OAuth，保留完整凭证；支持本地 JSON 与远程连接。
- 可选 Go 图片队列网关、Redis 与本地 iCloud 邮箱服务。

账号自动检查默认每 60 分钟运行，配额恢复到期后补查；账号页可见时每 10 秒读取本地状态。成功／失败按账号业务调用尝试累计，不包含配额检查，不补算无法确认的历史次数。

## Docker 启动

需要 Docker Compose 2.24+。安装脚本仅使用已有工具，不下载或执行远程安装脚本。

```bash
cp -n .env.example .env
chmod 600 .env
# 编辑 .env，设置不同的 GPT2API_AUTH_KEY 与 GPT2API_ADMIN_KEY。
bash deploy/install.sh --mode docker --port 3000
```

访问 `http://127.0.0.1:3000`，用管理密钥登录。默认只绑定回环地址；对外访问应通过 HTTPS 反向代理。

```bash
docker compose ps
docker compose logs --tail 100 app
bash scripts/verify_live.sh http://127.0.0.1:3000
```

### 可选组件

```bash
# 在 .env 配置 IMAGE_SCHEDULER_INTERNAL_KEY，沿用安装脚本创建的 data/auth_keys.json。
docker compose --profile images up -d --build

# 本地邮箱服务，不单独公开管理端口。
docker compose --profile local-icloud up -d --build
```

图片网关监听 `127.0.0.1:3001`，Redis 数据继续使用 `data/go-image-redis/`。停止服务时不要使用 `down -v`，升级前先备份数据。

可用 `docker-compose.warp.yml` 叠加可选代理；它需要额外容器能力，仅在信任主机和镜像时启用。外部清障服务通过 `GO_FLARESOLVERR_URL` 接入。

## 源码运行

主服务需要 Go 1.23+、Node.js 22、pnpm 12.5.1；本地邮箱子模块需要 Go 1.25+。

```bash
pnpm --dir web-vue install --frozen-lockfile --ignore-scripts
pnpm --dir web-vue run build:server
mkdir -p bin
go build -mod=readonly -o bin/gpt2api ./cmd/gpt2api
./bin/gpt2api
```

监听地址默认 `:8080`。源码运行不会读取 `.env`；通过进程环境或私有的 `data/config.json` 配置密钥。

## 配置与数据

| 配置 | 用途 |
|---|---|
| `GPT2API_AUTH_KEY` | API 密钥 |
| `GPT2API_ADMIN_KEY` | 控制台管理密钥 |
| `GPT2API_WEBUI_KEY` | 可选的用户界面密钥 |
| `GPT2API_DATA_DIR` | 持久化目录，默认 `data` |
| `GO_LISTEN_ADDR` | 监听地址 |
| `GO_PUBLIC_BASE_URL` | 返回文件链接使用的公网地址 |
| `GO_PROXY_URL` / `GO_PROXY_POOL` | 默认代理或代理池 |
| `GO_IMAGE_MAX_CONCURRENCY` | 图片全局并发上限 |
| `GO_IMAGE_RETENTION_DAYS` | 图片保留天数，默认 1 天 |

`GO_CONFIG_PATH`、`GO_ACCOUNTS_PATH`、`GO_AUTH_KEYS_PATH`、`GO_REGISTER_PATH` 可覆盖具体路径。默认配置写入 `data/config.json`；首次运行可复制旧根目录 `config.json`，不覆盖已有数据配置。历史认证环境变量仍可作为回退。

自动配额检查配置保存在 `data/register.json` 的 `openai_survival` 中；可通过管理接口 `/api/register/openai/survival` 查看或调整。禁用账号和非 GPT 凭证不会自动探测，失败会退避且保留上次配额。

自动注册需要实际配置外部执行器；未配置时返回不可用，不视为注册成功。

## API

业务接口使用 Bearer 鉴权，管理接口使用管理密钥。

| 路径 | 功能 |
|---|---|
| `GET /health` | 健康状态，无需鉴权 |
| `GET /v1/models` | GPT 模型目录 |
| `POST /v1/chat/completions` | 聊天及图片聊天入口 |
| `POST /v1/responses` | Responses 兼容接口 |
| `POST /v1/messages` | Messages 兼容接口 |
| `POST /v1/images/generations` | 图片生成 |
| `POST /v1/images/edits` | 图片编辑 |
| `POST /v1/editable-file-tasks` | 可编辑文件任务 |
| `POST /api/cpa/import` | 本地 Codex OAuth 批量导入，管理接口 |

CPA 导入会报告新增、重复、类型不支持及失败数量；类型不明确的文件不导入。OAuth 文件、账号导出和备份均含秘密，应限制访问并离线保存。

## 验证

```bash
bash scripts/check.sh test
bash scripts/check.sh build
bash scripts/check.sh static
```

CI 按上述顺序执行；发布流水线通过同一门禁后才构建发布镜像。`build` 会构建三个本地镜像，不启动服务。

## 文档

- [本地安装](docs/BEGINNER_LOCAL_SETUP.md)
- [Ubuntu 服务](docs/BEGINNER_UBUNTU_SETUP.md)
- [CPA / Sub2API](docs/AUTO_UPLOAD_SUB2API_CPA.md)
- [升级与回退](docs/MIGRATION.md)
- [故障排查](docs/TROUBLESHOOTING_LOG_EXAMPLES.md)

仅用于有权限的账号与符合上游服务条款的场景。许可证见 [LICENSE](LICENSE) 和 [THIRD_PARTY_LICENSE](THIRD_PARTY_LICENSE)。
