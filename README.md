# gpt2api

Go 后端与 Vue 控制台，提供 GPT 账号管理和 OpenAI 兼容接口。

## 功能

- GPT 聊天、Responses、Anthropic Messages 兼容接口及流式响应。
- GPT 图片生成、编辑、图库与可编辑文件任务；`gpt-image-2.5`／`gpt-image-2.6` 使用现有图片链路，支持 GIF 输出。
- 超过 1K 的图片请求优先使用有效付费账号，无可用付费账号时允许 Free 账号执行。
- 图片结果轮询对 429 使用 `Retry-After` 与指数退避；持续限流保留原任务续查，不自动重新生成。
- 账号分组、配额检查、凭证刷新、调用统计、代理与运行日志。
- CPA／Sub2API 导入保留渠道绑定，AT 到期或认证失败时向原渠道同步，RT 由原渠道独占管理。
- 可选 Go 图片队列网关、Redis 与本地 iCloud 邮箱服务。

账号自动检查默认每 60 分钟运行，配额恢复到期后补查；账号页可见时每 10 秒读取本地状态。成功／失败按账号业务调用尝试累计，不包含配额检查，不补算无法确认的历史次数。

本地 OAuth 账号轮换成功后先保存新凭证，再查询资料和配额；探测失败不丢凭证。外部托管账号不保存 RT、不调用本地轮换，渠道故障时保留原凭证并退避；没有渠道绑定的 CPA JSON 需先绑定来源。并发更新按凭证版本校验，旧令牌的在途反馈只累计次数，不覆盖新凭证的健康状态。

凭证写盘失败会报错并停止资料探测，新凭证暂留内存并定时补写。修复磁盘后确认写盘成功再重启；补写前进程退出仍可能丢失新凭证。

## Docker 启动

需要 Docker Compose 2.24+。安装脚本仅使用已有工具，不下载或执行远程安装脚本。

```bash
mkdir -p data logs
cp -n config.example.json data/config.json
chmod 600 data/config.json
# 编辑 data/config.json，设置不同的 auth-key 与 admin_key。
bash deploy/install.sh --mode docker --port 3000
```

已有 `data/config.json` 不会被覆盖。访问 `http://127.0.0.1:3000`，用管理密钥登录。默认只绑定回环地址；对外访问应通过 HTTPS 反向代理。

默认 `docker-compose.yml` 使用 `ghcr.io/maiphucgiang/gpt2api:latest`，可用 `GPT2API_IMAGE` 覆盖。安装脚本会先拉取 app 镜像再启动，不强制 `--build`。

```bash
docker compose ps
docker compose logs --tail 100 app
bash scripts/verify_live.sh http://127.0.0.1:3000
```

本地构建完整编排（含图片网关与 iCloud profile）使用：

```bash
docker compose -f docker-compose.local.yml up -d --build
```

本地默认端口为 `8000`。

### 可选组件

```bash
# 发布镜像：设置 IMAGE_SCHEDULER_INTERNAL_KEY 后启动图片队列。
IMAGE_SCHEDULER_INTERNAL_KEY=... docker compose --profile images up -d

# 本地构建：
IMAGE_SCHEDULER_INTERNAL_KEY=... docker compose -f docker-compose.local.yml --profile images up -d --build

# 本地邮箱服务，不单独公开管理端口。
docker compose --profile local-icloud up -d
docker compose -f docker-compose.local.yml --profile local-icloud up -d --build
```

图片网关监听 `127.0.0.1:3001`，Redis 数据继续使用 `data/go-image-redis/`。停止服务时不要使用 `down -v`，升级前先备份数据。

可用 `docker-compose.warp.yml` 叠加 MicroWARP 代理；它需要额外容器能力，仅在信任主机和镜像时启用。外部清障服务通过 `flaresolverr_url` 或 `GO_FLARESOLVERR_URL` 接入。

```bash
docker compose -f docker-compose.yml -f docker-compose.warp.yml up -d
docker compose -f docker-compose.local.yml -f docker-compose.warp.yml up -d --build
```
## 源码运行

主服务需要 Go 1.23+、Node.js 26、pnpm 12.5.1；本地邮箱子模块需要 Go 1.25+。

```bash
pnpm --dir web-vue install --frozen-lockfile --ignore-scripts
pnpm --dir web-vue run build:server
mkdir -p bin
go build -mod=readonly -o bin/gpt2api ./cmd/gpt2api
./bin/gpt2api
```

监听地址默认 `:8080`。源码运行不会读取 `.env`；通过私有的 `data/config.json` 配置密钥与运行参数，环境变量非空时覆盖对应字段。

## 配置与数据

首次安装将 `config.example.json` 复制为 `data/config.json`，权限 `600`。应用优先读取该文件中的运行字段；环境变量作为旧部署回退或显式覆盖。

| JSON 字段 | 环境变量 | 用途 |
|---|---|---|
| `auth-key` | `GPT2API_AUTH_KEY` | API 密钥 |
| `admin_key` | `GPT2API_ADMIN_KEY` | 控制台管理密钥 |
| `webui_key` | `GPT2API_WEBUI_KEY` | 可选的用户界面密钥 |
| | `GPT2API_DATA_DIR` | 持久化目录，默认 `data` |
| | `GO_LISTEN_ADDR` | 监听地址 |
| `public_base_url` | `GO_PUBLIC_BASE_URL` | 返回文件链接使用的公网地址 |
| `proxy.proxy_url` / `proxy.proxy_pool` | `GO_PROXY_URL` / `GO_PROXY_POOL` | 默认代理或代理池 |
| `image_max_concurrency` | `GO_IMAGE_MAX_CONCURRENCY` | 图片全局并发上限 |
| `image_retention_days` | `GO_IMAGE_RETENTION_DAYS` | 图片保留天数，默认 1 天 |
| `image_task_workers` | `GO_IMAGE_TASK_WORKERS` | 控制台图片任务工作线程，默认 2，按单张输出计 |
| `image_task_queue_size` | `GO_IMAGE_TASK_QUEUE_SIZE` | 控制台图片等待容量，默认 64 |
| `image_task_retention_hours` | `GO_IMAGE_TASK_RETENTION_HOURS` | 控制台终态任务记录保留时间，默认 24 小时 |
| `editable_model` | `GO_EDITABLE_MODEL` | PPT/PSD 上游固定模型，默认 `gpt-5-5-thinking` |
| `editable_reasoning_effort` | `GO_EDITABLE_REASONING_EFFORT` | PPT/PSD 上游思考强度，默认 `extended` |
| `flaresolverr_url` / `clearance_enabled` | `GO_FLARESOLVERR_URL` / `GO_CLEARANCE_ENABLED` | 外部清障服务 |
| | `GO_IMAGE_SCHEDULER_KEY` | 源码运行的内部调度密钥；Compose 使用 `IMAGE_SCHEDULER_INTERNAL_KEY` 映射 |

`GO_CONFIG_PATH`、`GO_ACCOUNTS_PATH`、`GO_AUTH_KEYS_PATH`、`GO_REGISTER_PATH` 可覆盖具体路径。默认配置写入 `data/config.json`；首次运行可复制旧根目录 `config.json`，不覆盖已有数据配置。历史认证环境变量仍可作为回退。

宿主机端口、镜像、UID/GID、时区、图片网关和邮箱参数不是 Go 应用 JSON 字段，用 Compose 默认值或命令行覆盖：`GPT2API_PORT`、`GPT2API_BIND`、`GPT2API_IMAGE`、`GPT2API_UID`、`GPT2API_GID`、`TZ`、`IMAGE_GATEWAY_PORT`、`IMAGE_SCHEDULER_INTERNAL_KEY`、`ICLOUD_PRIVACY_MAIL_API_KEY`。

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
| `POST /api/accounts/source` | 校验并绑定原账号的 CPA／Sub2API 来源，管理接口 |

CPA 导入会报告新增、重复、类型不支持及失败数量；类型不明确的文件不导入。OAuth 文件、账号导出和备份均含秘密，应限制访问并离线保存。

### 图片任务与恢复

控制台使用 `POST /api/image-tasks/generations` 或 `/edits` 提交任务；按用户和 `client_task_id` 幂等，同 ID 不同输入返回 409，容量不足返回 429。任务元数据保存在私有的 `data/image_tasks.json`，参考图片只暂存到任务结束；启动和新建任务时清理过期终态记录，最多保留 2000 条。

重启后不自动重新生成。已取得上游会话的失败任务可通过 `POST /api/image-tasks/{id}/resume-poll` 续取，`extra_timeout_secs` 默认 30、范围 1–120；仅查询原账号的原会话，保留已完成输出。凭证轮换和额度耗尽不影响续取资格，禁用或移除原账号后不会切换账号代取。

存储异常会拒绝新任务，`/health` 返回 503。升级前检查 `image_tasks.pending`、HTTP 在途请求及 Redis 网关队列。网关遇到执行结果不明或 `X-Image-Submission-Uncertain: true` 时不自动重发，避免重复生成。内部调度密钥未配置时，内部接口返回 503。

### Responses 边界

`stream:true` 按上游增量发送事件，包含事件序号、内容结束及终态；取消会传播到上游，断流或错误不会伪造 `response.completed`。图片模型仍返回图片链接的 Markdown。

当前 Web 后端不支持函数/内置工具、结构化输出、文本模型的图片输入、服务端保存、后台 Responses 或 `previous_response_id`，这些参数明确返回 400；请携带完整历史并使用 `store:false`。采样和输出长度参数兼容接收，但上游不保证执行；`usage` 为估算。

## 验证

```bash
bash scripts/check.sh test
bash scripts/check.sh build
bash scripts/check.sh static
```

CI 按上述顺序执行；发布流水线通过同一门禁后才构建发布镜像。`build` 构建三个本地镜像，不启动服务；`static` 包含完整 `vue-tsc --noEmit`，前端测试覆盖组件事件、账号轮询和 CPA 导入。

## 文档

- [本地安装](docs/BEGINNER_LOCAL_SETUP.md)
- [Ubuntu 服务](docs/BEGINNER_UBUNTU_SETUP.md)
- [CPA / Sub2API](docs/AUTO_UPLOAD_SUB2API_CPA.md)
- [升级与回退](docs/MIGRATION.md)
- [故障排查](docs/TROUBLESHOOTING_LOG_EXAMPLES.md)

仅用于有权限的账号与符合上游服务条款的场景。许可证见 [LICENSE](LICENSE) 和 [THIRD_PARTY_LICENSE](THIRD_PARTY_LICENSE)。
