# 更新记录

## 未发布

## v1.3.0

### 图片生成与任务

- 新增 `gpt-image-2.5` ChatGPT 网页兼容入口：覆盖模型目录、控制台图片选项、文生图、JSON/multipart 参考图编辑和流式/非流式图片聊天，复用现有账号池与图片链路；使用 `auto` 会话模型与 `picture_v2` 图片工具，旧 `gpt-image-2` 路由和默认值保留，实际底层图片版本由上游账号权限决定。
- 新增 `gpt-image-2.6` 兼容入口，复用同一图片链路；GIF 输出保留原始格式与原始字节。
- 超过 1K（最长边 > 1920）的图片请求优先调度订阅有效且工作区、时效均有效的付费账号；无可调度付费账号时允许 Free 兜底，付费并发满时排队；`auto`/1K 请求仍使用全部图片账号，断点续查绑定原账号。
- 图片结果轮询遇到 429 时遵守 `Retry-After`（秒数或 HTTP-date）并指数退避（5 秒基数、60 秒封顶、带随机抖动）；持续限流保留原会话续查，不重复提交生成；429 不再触发代理故障切换，账号冷却按上游重试窗口执行，运行日志新增 `image_poll_backoff` 阶段。
- 图片下载优先使用 canonical 路由 `/backend-api/files/download/{id}`，签名 URL 未就绪时重试一次，404 或缺 URL 时回退兼容路由 `/backend-api/files/{id}/download`。
- 图片任务全程持久化：超时或进程重启后按会话 ID 续查原任务，不重新生成；提交结果不确定时不自动重试；新增内部调度执行接口并接入 Go 图片网关，网关对不确定结果同样不重放。

### 账号与凭证

- CPA/Sub2API 导入账号的 RT 由原渠道独占管理：持久化凭证归属、来源渠道与远端 ID；AT 临近到期或遇到 401 时向原渠道同步；所有本地刷新入口禁止轮换外部 RT，故障不回退本地 OAuth；更新保留本地账号 ID、统计与任务绑定。
- 未绑定的 Codex/本地 CPA JSON 账号默认 `external_unbound`，同样禁止本地轮换，经 `POST /api/accounts/source` 验证后绑定。
- 凭证轮换先原子持久化新 AT/RT，再查询 profile/配额；后续查询失败时保留新凭证并告警、按退避重试，不回滚丢弃上游可能已接受的凭证。
- 账号定时检查保存完整配额快照，账号页可见时自动读取最新状态；按账号每次业务调用尝试持久化成功/失败计数，修正遗漏和重复反馈。
- CPA 仅导入 Codex OAuth，保留完整凭证并报告类型不支持的记录。

### 功能移除

- 移除已废弃的 BPS/BSG：不再提供 `bsg/` 模型、`internal/bps`、`GO_BPS_*` 配置、账号 BPS 开关与工具历史。

### 接口与流式

- Responses 协议复用 Chat 流式管道：SSE 事件增量发出并立即 flush，客户端取消向上游传播，不再等完整答案后一次性写出；新增 Responses 请求校验。

### 搜索

- `/v1/search` 改为专用 ChatGPT Web 搜索链路：默认模型 `gpt-5-5`（替换不可用的 `gpt-4o-search-preview`），prepare 携带 `system_hints:["search"]`，会话携带 `force_use_search:true`；从会话消息提取真实来源（url/link/source_url/metadata.url，带标题、摘要、类型并去重），答案引用标记归一为 `[n]`，`sources` 不再固定为空数组。
- 搜索契约与聊天对齐：未知或图片模型返回 404，账号重试与失败结算同一规则，轮询遵守 Retry-After 与指数退避；DebugCenter 与 Skills 安装指令的来源链接承诺自此成立。

### 可编辑文件任务

- DebugCenter 的 PPT/PSD 参考图改为本地图片多选上传：显示已选文件并可移除，提交前在前端转成后端所需格式，界面不出现 base64 文本；两种任务都要求至少一张参考图。
- editable 只调度 `accountpolicy.SubscriptionEligible` 的付费订阅，拒绝 Free、未知、失效或过期核验，不信任可编辑 plan 字段，不做 Free 兜底。
- 上游接受后立即持久化原会话 ID 和原账号稳定 ID，再轮询；重启后对 queued/running 且有检查点的任务绑定原账号续查，禁止重新 POST；无检查点的旧任务明确不可续查。
- 轮询复用图片/搜索的 Retry-After 与有上限指数退避；429 不换代理；上游终态立即失败，坏 JSON 明确返回。任务总超时固定 25 分钟，轮询至少 20 分钟并受任务上下文封顶，不跟 `GO_REQUEST_TIMEOUT_SECONDS`。本地解码、检查点或落盘错误不记账号失败/冷却；仅本地错误且上游已接受时记业务成功。已接受后的上游 429/500 仍按真实状态记失败和冷却，不重新生成。
- 固定模型与思考强度改为 `GO_EDITABLE_MODEL`、`GO_EDITABLE_REASONING_EFFORT`，默认 `gpt-5-5-thinking` / `extended`。

### 控制台

- 修复概览六张图表的模板引用绑定（`useTemplateRef` 显式绑定），补充首次加载、刷新、缩放、销毁和页面恢复回归；账号页展示凭证归属与来源渠道。

### 工程与安全

- 精简为 GPT-only 功能：移除 Python 服务、captcha-solver 等旧组件（约 3.9 万行），统一部署入口和文档，保留既有账号与图片队列数据兼容。
- 统一为 gpt2api 命名、Go 后端、Vue 前端；项目依赖安装统一使用 pnpm，Docker 仅用 npm 引导安装固定 pnpm 版本，CI 不安装 npm 依赖。
- 运行时升级到 Node 26 LTS 与 Vue 3.5.45：CI setup-node 与 Docker 基础镜像同步 Node 26，Vue 在现有 semver 范围内升级，pnpm 保持 12.5.1。
- 前端工具链迁移到 Vite+ 1.6.0：开发、测试、构建和预览脚本改用 `vp`，Vite+ core/Rolldown 与 Vitest 5 接管构建测试，保留 `vue-tsc` 类型检查。
- 迁移保留 `@` 别名、开发代理、`dist` 输出、Tailwind 3/PostCSS、nanocat-ui、vue-virtual-scroller、happy-dom 和 ECharts vendor 引用；8 个前端测试（23 个用例）含 Dashboard 六图真实挂载回归均通过。
- 增加串行门禁 `scripts/check.sh`：测试 → 构建 → 静态分析；构建产出主服务、Go 图片网关、iCloud 邮箱三个本地镜像并校验 Compose，不启动服务或发布镜像。
- 构建工具链升级到 Go 1.27.3 并更新镜像 CA 证书包，拒绝已泄露的 eMudhra/emSign 中间证书（CVE-2026-111943）。
- 将本地门禁拆成 `scripts/check-backend.sh` 与 `scripts/check-frontend.sh`：根 `scripts/check.sh` 只按 `test` → `build` → `static` 依次调用两个子脚本，不再重复实现检查逻辑。前端子命令分别为 `pnpm --dir web-vue exec vp test --run`、`pnpm exec vp build` 与 `vue-tsc --noEmit`（静态阶段仍跑 `git diff --check HEAD`）。后端子命令覆盖三个 Go module 的 `go test -race`、主服务/图片网关/iCloud 三镜像构建、原有 Compose overlay 与环境 JSON 断言（`node -e` 改为 `jq -e`，键值不变），以及 `go vet`/`gofmt`/`bash -n`/`git diff --check`；后端脚本不调用 Node 或 pnpm。
- 新增 GitHub Actions `frontend.yml` 与 `backend.yml`：二者均声明带路径过滤的 `push`/`pull_request`、`workflow_call` 与 `workflow_dispatch`。前端工作流只安装 Node 26 与 pnpm 12.5.1，按 test → build → static 调用 `check-frontend.sh`，不引入 Go/Docker。后端工作流只安装 Go 1.27.x，按同一顺序调用 `check-backend.sh`，不安装 Node/pnpm。路径覆盖 `web-vue`、Go 源码、Compose、Dockerfile、编排脚本、两个检查工作流、`ci.yml` 与 `docker-publish.yml`，避免共享编排文件变更时没有任何工作流触发。
- `ci.yml` 改为仅 `workflow_call`/`workflow_dispatch` 的薄编排器，按 `needs` 先调用 `frontend.yml` 再调用 `backend.yml`；`docker-publish.yml` 仍 `uses` `ci.yml`，发布前走完整前后端门禁。Docker `web-build` 最后一步改为显式 `pnpm exec vp build`，保留 Node 26、按 `TARGETARCH` 的 npm 缓存、`--allow-scripts=pnpm` 与 pnpm 12.5.1 断言，不再经过 `pnpm run build`。

### 部署与配置

- 整体版本定为 `1.3.0`（`VERSION`、Go 默认版本、Vue `package.json`）。
- 根 `docker-compose.yml` 改为发布镜像优先：app 默认 `ghcr.io/maiphucgiang/gpt2api:latest`（`GPT2API_IMAGE` 可覆盖），不再隐式本地构建；图片网关、Redis、iCloud profile 保留。`deploy/install.sh` 的 docker 模式先 `pull app` 再 `up -d`，不再对无 build 的默认 app 强制 `--build`。
- `docker-compose.local.yml` 改为含 app 构建、图片网关、Redis、iCloud 的完整本地编排，默认镜像 `gpt2api:local`、端口 `8000`。本地构建命令为 `docker compose -f docker-compose.local.yml up -d --build`。删除 `docker-compose.go.yml`。
- 用户配置入口改为 `config.example.json` → `data/config.json`（权限 600，不覆盖已有文件）。应用优先读取 JSON 中的 auth/admin/webui、public_base_url、request/chat/image/editable/proxy/clearance 字段；环境变量非空时覆盖。删除 `.env.example`。宿主机端口、镜像、UID/GID、TZ、网关和邮箱参数仍用 Compose 默认值或命令行覆盖，不写入应用 JSON。
- WARP 叠加改用官方 `ghcr.io/ccbkkb/microwarp:latest`：保留 `NET_ADMIN`/`SYS_MODULE`，补齐 IPv6 sysctl，内部无认证 SOCKS 显式 `ALLOW_NO_AUTH=1`；官方 Alpine 镜像使用内置 `wget` 对 `http://127.0.0.1:1080/` 做 healthcheck，凭证目录挂到 `data/microwarp`。

## v1.2.4

- 提供 Go 服务、GPT 聊天及图片接口、账号与代理管理。
