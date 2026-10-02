# GoFeed —— 一个视频feed流系统

接口的当前路径、请求和响应以 [`API.md`](./API.md) 及 `backend/internal/router/router.go` 为准；开发、迁移、配置和提交约束见 [`AGENTS.md`](./AGENTS.md)。

后续任务、Feed F0–F6 演进和待补验收统一维护在 [`docs/DEVELOPMENT_PLAN.md`](./docs/DEVELOPMENT_PLAN.md)。已完成事项只在本文简述，不再保留独立完成方案。

## 快速开始（Docker）

首次使用需要准备两个文件：

1. 在 `backend` 目录创建 `.env`（变量示例见 `backend/.env.example`），设置 `MYSQL_ROOT_PASSWORD`、`MYSQL_DATABASE` 和固定的 `JWT_SECRET`。
2. 复制 `backend/configs/config.example.yaml` 为 `backend/configs/config.yaml`，把 `database.host` 改为 `mysql`；数据库密码由 `backend/.env` 注入，YAML 不包含密码字段。

数据库会由 Compose 自动创建，之后启动会自动跑迁移建表：

```bash
docker compose up -d
```

启动顺序：mysql 健康检查通过 → `init-db` 创建 `MYSQL_DATABASE`（已存在则跳过）→ `migrate`（golang-migrate）应用 `backend/db/migrations` 下的迁移建表 → backend/worker/sweeper 启动。应用本身不负责建库建表。

> 需要 Docker Compose v2.17+（依赖 `service_completed_successfully` 条件）。

## 本地开发（不使用 Compose）

本地开发直接启动后端和前端，数据库依赖本机已运行的 MySQL。应用启动时不会自动建库或迁移，避免服务重启时隐式修改表结构；表结构变更通过版本化迁移显式执行。

### 1. 准备本地配置与数据库

复制 `backend/.env.example` 为 `backend/.env`，复制 `backend/configs/config.example.yaml` 为 `backend/configs/config.dev.yaml`。在 `.env` 中配置本机 MySQL，至少确认以下字段：

```env
MODE=dev
CONFIG_PATH=configs/config.dev.yaml
MYSQL_HOST=127.0.0.1
MYSQL_PORT=3306
MYSQL_USER=root
MYSQL_ROOT_PASSWORD=your-local-mysql-password
MYSQL_DATABASE=feedsystem
JWT_SECRET=replace-with-a-stable-local-secret
```

启动本机 MySQL 后，首次创建业务库。`-p` 会交互式询问密码，不会将密码写入命令历史：

```bash
mysql -u root -p -e "CREATE DATABASE IF NOT EXISTS feedsystem CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;"
```

### 2. 安装并执行数据库迁移

首次安装 [`golang-migrate`](https://github.com/golang-migrate/migrate) CLI：

```bash
go install -tags 'mysql' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.19.1
```

确保 `$(go env GOPATH)/bin` 已加入 `PATH`。若仅需在当前 Bash 会话中使用：

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
```

从项目根目录切换到 `backend` 目录后，执行全部未应用的迁移：

```bash
cd backend
migrate -path ./db/migrations -database "mysql://root:<URL 编码后的密码>@tcp(127.0.0.1:3306)/feedsystem?multiStatements=true" up
```

密码中包含 `@`、`:`、`/`、`?`、`#` 或 `%` 等 URL 特殊字符时必须先编码。每次新增迁移文件后重新执行同一条 `up` 命令即可；`schema_migrations` 会记录已执行版本，因此只会应用尚未执行的迁移。不要修改已执行的迁移文件，应新增一对递增版本的 `.up.sql` 和 `.down.sql` 文件。

跨越草稿、拒绝视频或 outbox 租约版本向下回滚属于维护操作。先停止所有 API、worker 与 sweeper 实例并确认相关进程已完全退出，再执行只读检查；有 `processing`、`rejected` 或 `purging` 行时不要继续回滚相关状态机迁移，应先完成业务回收或按迁移前置条件处理。`000009` 的 down migration 会把遗留 `publishing` 事件恢复为 `pending` 后删除租约列，但仍必须在全部 worker 停止时执行。回滚完成前不得重新启动进程，避免检查与 DDL 之间出现新的状态行。`golang-migrate` 在 down SQL 失败时会将版本留为 dirty，不能把这一检查写成故意失败的迁移 SQL。

```sql
SELECT COUNT(*) AS incompatible_rows
FROM videos
WHERE status IN ('draft', 'purging')
   OR published_at IS NULL
   OR play_url = ''
   OR cover_url = '';
```

### 3. 直接启动后端与前端

后端必须从 `backend` 目录启动，才能读取 `.env` 与默认的 `configs/config.dev.yaml`：

```bash
cd backend
go run ./cmd            # API
# 另开一个 backend 终端；先应用到 000009，验证异步发布闭环时需要 RabbitMQ
# go run ./cmd/worker
# go run ./cmd/sweeper  # 按需启动注销用户和到期视频清扫任务
```

另开一个终端启动前端开发服务器：

```bash
cd frontend
pnpm install        # 首次安装依赖
pnpm dev
```

前端开发服务器会代理 `/api` 和 `/static` 到本机后端 `http://localhost:8080`。日常修改 Go 或 Vue 源码不涉及 Docker 镜像；仅新增数据库迁移时运行一次 `migrate ... up`。

## CI 验证

每次 push、Pull Request 和手动触发都会执行以下门禁：

1. 后端：启动 MySQL 8.0、Redis 7 和 RabbitMQ 3 service，执行 `go vet ./...`、`go build ./...` 和 `go test -race -count=1 ./...`。集成测试会创建临时数据库并应用全部向上迁移；worker 会使用 RabbitMQ 执行视频处理集成用例，Redis 仍只固定服务可用性契约。
2. 部署配置：从 `backend/.env.example` 和 `backend/configs/config.example.yaml` 生成 CI 临时的忽略配置文件，执行 `docker compose config --quiet`，并检查后端 `/ready`、前端 `service_healthy`、Redis/RabbitMQ 健康检查、持久卷及 API 不依赖中间件启动的契约。不使用真实 `.env` 或秘密。
3. 前端：以冻结锁文件安装依赖，执行只读 `pnpm run lint`、Vitest、类型检查与生产构建。

当前 Playwright 用例会 mock 公共 Feed API，可在本地按需运行，因此它验证浏览器中的页面行为，不替代本机 MySQL 下的真实发布和鉴权联调。`pnpm run lint:fix` 与 `pnpm run format` 都会写入文件，只应在本地修复后配合 `git diff` 审查，不能作为 CI 门禁。

## 配置

配置加载顺序：先读取 `CONFIG_PATH` 指定的 YAML（默认 `configs/config.dev.yaml`），再用环境变量覆盖，环境变量优先级最高。数据库、Redis、RabbitMQ 的密码、JWT 密钥和运行模式只从环境变量读取，YAML 中即使存在同名字段也会被忽略。API 已创建可恢复 cache Runtime 并将其接入注册/登录限流；Redis 初始连接失败只记录脱敏事件，API 仍会启动，后续请求由 Runtime 按冷却和单探针机制恢复。Feed 页缓存默认关闭，开启后使用独立 Runtime 和故障恢复状态。worker 通过可重连 runtime 建立 RabbitMQ 连接并运行 relay/consumer，API 与 sweeper 不建立 MQ 连接。`observe.pprof` 仍是后续功能预留，当前加载器不读取它，现有 `/ready` 只依赖 MySQL。

当前生效的配置项：

| 配置项 | 环境变量 | 默认值 / 说明 |
| --- | --- | --- |
| 运行模式 | `MODE` | 仅 `prod` 启用生产模式并关闭 Gin 调试日志；空值、`dev` 或其他值均按 `dev` 处理 |
| 配置文件路径 | `CONFIG_PATH` | 本地默认 `configs/config.dev.yaml`；Docker 由 compose 设为 `/app/configs/config.yaml` |
| HTTP 端口 | `SERVER_PORT` | `8080` |
| MySQL 主机 | `MYSQL_HOST` | 本地默认 `localhost`；Docker 容器由 Compose 覆盖为 `mysql` |
| MySQL 端口 | `MYSQL_PORT` | `3306` |
| MySQL 用户 | `MYSQL_USER` | `root` |
| MySQL 密码 | `MYSQL_ROOT_PASSWORD` | 仅从环境变量读取，优先于 `MYSQL_PASSWORD`；统一存放在 `backend/.env` |
| MySQL 库名 | `MYSQL_DATABASE` | 默认 `feedsystem`；统一存放在 `backend/.env` |
| JWT 密钥 | `JWT_SECRET` | 存放在 `backend/.env`；不设置时每次启动随机生成，重启后所有 token 失效 |
| 注销保留天数 | `RETENTION_USER_DELETED_DAYS` | 默认 `7`；注销账号软删除后经过该天数由 sweeper 硬删除 |
| 视频删除保留天数 | `RETENTION_VIDEO_DELETED_DAYS` | 默认 `7`；视频软删除后经过该天数由 sweeper 删除视频/封面文件并硬删除记录 |
| 草稿/拒绝视频保留时长 | `RETENTION_VIDEO_DRAFT_HOURS` | 默认 `24`；草稿从创建、rejected 视频从 `rejected_at` 起计算，届满后进入不可逆清扫 |
| 媒体孤儿文件宽限时长 | `RETENTION_MEDIA_ORPHAN_HOURS` | 默认 `24`；仅回收本地受控目录中超过该时长、且不再被任一用户或视频记录引用的对象 |
| 清扫间隔 | `SWEEPER_INTERVAL_MINUTES` | 默认 `60`；sweeper 执行用户、已发布视频、草稿/拒绝视频和媒体孤儿清扫的间隔分钟数 |
| 草稿清扫租约 | `SWEEPER_DRAFT_PURGE_LEASE_MINUTES` | 默认 `15`；单条草稿或拒绝视频的 token 围栏租约，过期后可由其他 sweeper 接管 |
| Redis 主机 | `REDIS_HOST` | 本地默认 `localhost`；Docker 容器由 Compose 覆盖为 `redis` |
| Redis 端口 | `REDIS_PORT` | `6379` |
| Redis DB | `REDIS_DB` | `0`；供限流与可选 Feed Runtime 选择逻辑库 |
| Redis 密码 | `REDIS_PASSWORD` | 仅从环境变量读取；Compose Redis 将其传给 `requirepass` |
| Feed 页缓存 | `FEED_PAGE_CACHE_ENABLED` | 默认 `false`，对应 `feed.page_cache_enabled`；环境变量非空但不是合法布尔值时关闭 |
| RabbitMQ 主机 | `RABBITMQ_HOST` | 本地默认 `localhost`；Docker 容器由 Compose 覆盖为 `rabbitmq` |
| RabbitMQ 端口 | `RABBITMQ_PORT` | `5672` |
| RabbitMQ 用户 | `RABBITMQ_DEFAULT_USER` | 默认 `gofeed`；覆盖 YAML 用户名并用于 Compose RabbitMQ 首次初始化 |
| RabbitMQ 密码 | `RABBITMQ_DEFAULT_PASS` | 仅从环境变量读取；用于 Compose RabbitMQ 首次初始化 |

### 本地开发配置

本机 MySQL 的完整初始化、迁移和直接启动流程见上方「本地开发（不使用 Compose）」。`backend/.env` 存放数据库和中间件密码及固定 `JWT_SECRET`，`backend/configs/config.dev.yaml` 存放非敏感配置；从 `backend` 目录运行的 API 和 worker 都会读取这些配置。仅启动 API 仍只要求 MySQL；Redis 已接入注册/登录限流，但不可用时 API 启动和这两个业务接口均按 fail-open 继续。要验证异步发布闭环，需在填好 `backend/.env` 后启动 RabbitMQ 并运行 worker（可执行 `docker compose up -d rabbitmq`，再直接运行 worker）。

Feed 页缓存只作用于 `/api/feed` 的带游标后续页，首屏仍查 MySQL；默认 TTL 30 秒、单次缓存操作上限 100 毫秒。命中后批量检查整页（含探测记录）的当前公开卡片，失效时按原游标整页回源；作者和互动统计实时读取。Redis 失败回源，短超时同步回填失败不影响成功响应，不启动后台回填任务。开启时最多并发处理 32 个 Feed 请求和 16 次缓存操作；请求容量耗尽返回安全的 503，缓存容量耗尽直接回源。旧 `/api/video` 不受这些限制影响。

评审后可在 `backend` 目录用临时环境变量开启；设为 `false` 并重启 API 即回到原读取路径，无需迁移或清理 Redis。下面是操作说明；本轮的缓存验收通过测试内装配的 httptest 服务完成，未以 `go run ./cmd` 常驻启动 API：

```powershell
$env:FEED_PAGE_CACHE_ENABLED = 'true'
go run ./cmd
```

### Docker 部署

Docker 同样采用复制修改的方式：

1. 复制 `backend/configs/config.example.yaml` 为 `backend/configs/config.yaml`（已被 git 忽略，不会入库）。
2. 修改 `database.host` 为 `mysql`；数据库、Redis 和 RabbitMQ 密码均由 `backend/.env` 注入，YAML 内不保存秘密。Compose 会把后端进程的 Redis/RabbitMQ 主机和端口覆盖为服务名及容器端口。
3. compose 将宿主机 `backend/configs` 挂载到容器 `/app/configs`，并设置 `CONFIG_PATH=/app/configs/config.yaml`，容器实际读取的就是这份 `config.yaml`。
4. 在 `backend` 目录创建 `.env`（变量参考 `backend/.env.example`，文件本身不入库）。将 Redis 与 RabbitMQ 的密码替换为非占位符值后再首次启动。Compose 通过 `env_file` 将该文件注入 MySQL、Redis、RabbitMQ、迁移和后端进程；API、worker 与 sweeper 同时将该文件以只读方式挂载到 `/app/.env`，供 `godotenv` 加载。容器内覆盖 `MODE=prod`、MySQL/Redis/RabbitMQ 主机和端口，以及 `CONFIG_PATH=/app/configs/config.yaml`。示例：

```env
MODE=dev
SERVER_PORT=8080
JWT_SECRET=replace-with-a-32-characters-random-secret
MYSQL_HOST=localhost
MYSQL_PORT=3306
MYSQL_USER=root
MYSQL_ROOT_PASSWORD=your-mysql-password
MYSQL_DATABASE=feedsystem
RETENTION_USER_DELETED_DAYS=7
RETENTION_VIDEO_DELETED_DAYS=7
RETENTION_VIDEO_DRAFT_HOURS=24
RETENTION_MEDIA_ORPHAN_HOURS=24
SWEEPER_INTERVAL_MINUTES=60
SWEEPER_DRAFT_PURGE_LEASE_MINUTES=15
REDIS_HOST=localhost
REDIS_PORT=6379
REDIS_DB=0
REDIS_PASSWORD=replace-with-a-long-random-redis-password
RABBITMQ_HOST=localhost
RABBITMQ_PORT=5672
RABBITMQ_DEFAULT_USER=gofeed
RABBITMQ_DEFAULT_PASS=replace-with-a-long-random-rabbitmq-password
```

如需调整 HTTP 端口，修改 `server.port`，并同步 `docker-compose.yml` 中 `8080:8080` 的端口映射。Redis `6379` 与 RabbitMQ `5672` 仅绑定到宿主机回环地址，供本地 worker、诊断或后续直接运行的进程使用；Compose 内服务始终通过 `redis:6379` 和 `rabbitmq:5672` 通信。

### 观测与健康检查

`GET /health` 只检查 API 进程存活，`GET /ready` 还会在 2 秒内探测 MySQL，数据库不可用时返回 `503`。Compose 使用 `/ready` 作为 backend 健康检查，frontend 仅在 backend 健康后启动；Redis/RabbitMQ 各自有容器健康检查，但 Redis Runtime 不进入 API 就绪条件。每个响应会返回 `X-Request-ID`；客户端可复用该请求头值关联服务端的 `http_request`、`http_request_error` 和 `readiness_check` 日志。开启 Feed 缓存后，`feed_page_cache` 日志记录命中、未命中、失效、读写失败、MySQL 读取及容量限制的结果与耗时，不输出游标或 Redis 错误详情；这些基础事件尚未接入指标/告警平台。sweeper 每项清扫和每轮汇总都会记录事件、结果、耗时、删除数量及失败数量。当前未启用或暴露 pprof；后续实现会参考 `feedsystem` 的隔离模式，以独立 `ServeMux`、仅回环监听、显式开关和独立关闭生命周期提供诊断端点，而不将其注册到 Gin 路由。

## 已完成能力概述

以下为源码实现、提交状态与本轮测试验收结论；未完成的验收缺口另列在开发计划中。

- 账户与会话、匿名视频流、草稿上传/异步发布、公开详情、个人主页、我的视频、头像、点赞/评论/关注及对应前端页面；接口契约见 [API.md](./API.md)。
- 视频与 social 列表使用带版本和范围的游标；用户列表兼容分页已提交为 `455849e`，保留旧的无参数读取。
- `internal/error` 统一 user/video/social 的 HTTP 错误分类和安全响应，保持既有状态码与 `{"error":"..."}` 形状；后台任务保留自身错误语义。
- MySQL 事务 + Outbox 可靠发布，RabbitMQ 运行时连接恢复与拓扑重建、publisher confirm、派发租约/退避、消费 CAS 幂等和 `1s/5s/30s` 重试/DLQ。F2-A `48ce8df` 支持按 `event_type` 装配发布目标、快照检查与载荷构造；生产仍只注册并写入 `video.process`，未知类型继续固定退避。
- 数据和媒体清扫、草稿租约、公开视频完整性过滤、请求日志与 MySQL 就绪检查；本地媒体孤儿回收已提交为 `d0902a3`，按宽限期与引用检查清理。
- 登录/注册 Redis 固定窗口限流、故障 fail-open 和冷却/单探针恢复；页面按服务端 `Retry-After` 等待。Redis 不进入 `/ready`。
- F0 新增匿名 Timeline `/api/feed` 的四层读取边界；F1-A 批量公开卡片 `a7e2bd4`、F1-B 轻量页缓存端口与适配 `509c123` 已提交。F1-C 已提交为 `f772349`，接入默认关闭的后续页缓存、命中校验、MySQL 回源、独立 Runtime 与有界并发。首页已通过 `896f4e1` 切换为 `/api/feed?scene=timeline&limit=12`，作者主页继续使用 `/api/video?author_id=...`；取消、去重、分页错误态、手动重试与播放暂停保持原行为。测试覆盖及实际依赖参与情况见 [开发计划](./docs/DEVELOPMENT_PLAN.md) 第 5 节；缓存收益与容量压测仍待验证。

2026-10-02 首页迁移验收：lint、149 个单元用例、构建通过；mock 浏览器桌面/移动 38 通过、2 个真实限流用例未启用。隔离联调工具 `f4af6b8` 在独立 MySQL 测试库准备 13 条可见视频，以真实 Go API 返回 JSON 与游标；缓存关闭/开启各通过桌面与移动浏览器，观测到第二页未命中、回填及重复查询命中，开关前后响应逐字节一致。媒体使用本地夹具，未验证 MQ 发布或性能收益。

重跑隔离联调：从 `backend` 设置当前进程 `$env:GOFEED_TIMELINE_BROWSER='1'` 后执行 `go test -race -count=1 -v -run '^TestTimelineBrowserLive$' ./internal/router`。需已安装前端依赖/Chromium、当前进程 PATH 含 Node，以及可连接的 MySQL/Redis；连接配置只读取现有环境或 `backend/.env`。工具复用 `testutil` 建库、迁移与删库，使用随机 Redis 前缀和精确键清理，自动退出本轮 API/Vite 服务；不复用用户开发服务器，不改私有配置。完整命令及清理证据见开发计划第 5.5 节。

回滚首页模块时，恢复 `896f4e1` 之前的首页读取实现（`usePublishedFeed` 调用 `listPublishedVideos`）。如需连同专用验收工具一起撤销，先 `git revert f4af6b8`，再 `git revert 896f4e1`，并同步文档。重新构建并重新加载页面，清空内存分页状态；不能将 Feed 游标继续用于 `/api/video`，也不能在失败后自动跨接口续页。

2026-10-02 F2-A 验收：后端 `go vet ./...`、全量普通测试及 race 测试通过；真实 MySQL/RabbitMQ 的多路由、原发布闭环和 confirm 后崩溃恢复均通过。6 个未开启的专项用例跳过，完整命令与边界见开发计划第 5.6 节。回滚路由模块可执行 `git revert 48ce8df`，同步进度文档后重新构建并重启 worker；无需数据库迁移。

## 后续开发

首页 Timeline 接入与隔离真实链路验收已完成，Feed 页缓存继续默认关闭。F2-A 事件类型路由已实现；F2-B 契约设计已整理，基础卡片缓存、可靠 `video.published` 写入及消费者均未实现。下一模块先交付可选卡片缓存及实际读取用途，再按同事务 Outbox、独立消费者和有限重试交付发布事件闭环；具体契约、验收与回滚见 [开发计划](./docs/DEVELOPMENT_PLAN.md) 第 3.2 节。F2-A 的运行验收仍见第 5.6 节，缓存收益与容量验证仍待补。

Following、Hot、推荐及其他待开发/评估能力统一见 [开发计划](./docs/DEVELOPMENT_PLAN.md)。每次只实施一个可独立 review 的模块，完成后先等待 review，明确指令后提交。

## 文档维护

根目录只保留 README.md、API.md 和 AGENTS.md；后续任务与必要设计归 `docs/DEVELOPMENT_PLAN.md`，该文件纳入 Git 跟踪。任务完成后更新本文的简要能力说明，移除完成方案；API.md 只描述已注册接口，待补测试和验收不能随完成方案一起丢失。
