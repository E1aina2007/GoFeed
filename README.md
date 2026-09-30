# GoFeed —— 一个视频feed流系统

接口的当前路径、请求和响应以 [`API.md`](./API.md) 及 `backend/internal/router/router.go` 为准；开发、迁移、配置和提交约束见 [`AGENTS.md`](./AGENTS.md)。

Feed 的分阶段设计与进度（F0–F6：Timeline 兼容边界、缓存、派生事件、Following、Hot、规则推荐和重建）见 [`FEED_CORE_EVOLUTION_PLAN.md`](./FEED_CORE_EVOLUTION_PLAN.md)。F0 后端已分模块提交 `GET /api/feed` 的匿名 Timeline，尚未完成运行验收；旧的 `GET /api/video` 保持兼容，当前前端仍使用旧入口。Feed 缓存和其他场景尚未实现。

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

### Windows 一键启动四个开发进程

基础设施已启动、`backend/.env` 已配置且前端依赖已安装后，从仓库根目录执行：

```powershell
.\scripts\dev.ps1
```

脚本会分别打开 API、前端、worker 和 sweeper 四个 PowerShell 终端，日志保留在各自窗口中。它不会自动安装依赖、执行迁移或启动 MySQL、Redis、RabbitMQ；这些基础设施仍按上文独立管理。每个窗口可用 `Ctrl+C` 停止对应任务。

## 前端开发（pnpm）

前置：npm，并安装 pnpm：

```bash
npm install -g pnpm
```

进入 `frontend` 目录：

```bash
cd frontend
pnpm install        # 首次安装依赖（生成/更新 pnpm-lock.yaml）
pnpm dev            # 启动开发服务器
pnpm lint           # 只检查，不修改文件
pnpm lint:fix       # 明确需要自动修复时再执行
pnpm test:unit      # 单元测试
pnpm test:e2e -- --project=chromium --project="Mobile Chrome" # 浏览器回归
pnpm build          # 类型检查 + 构建
pnpm preview        # 本地预览构建产物
```

安装依赖：`pnpm add <包名>`；开发依赖：`pnpm add -D <包名>`。

## CI 验证

每次 push、Pull Request 和手动触发都会执行以下门禁：

1. 后端：启动 MySQL 8.0、Redis 7 和 RabbitMQ 3 service，执行 `go vet ./...`、`go build ./...` 和 `go test -race -count=1 ./...`。集成测试会创建临时数据库并应用全部向上迁移；worker 会使用 RabbitMQ 执行视频处理集成用例，Redis 仍只固定服务可用性契约。
2. 部署配置：从 `backend/.env.example` 和 `backend/configs/config.example.yaml` 生成 CI 临时的忽略配置文件，执行 `docker compose config --quiet`，并检查后端 `/ready`、前端 `service_healthy`、Redis/RabbitMQ 健康检查、持久卷及 API 不依赖中间件启动的契约。不使用真实 `.env` 或秘密。
3. 前端：以冻结锁文件安装依赖，执行只读 `pnpm run lint`、Vitest、类型检查与生产构建。

当前 Playwright 用例会 mock 公共 Feed API，可在本地按需运行，因此它验证浏览器中的页面行为，不替代本机 MySQL 下的真实发布和鉴权联调。`pnpm run lint:fix` 与 `pnpm run format` 都会写入文件，只应在本地修复后配合 `git diff` 审查，不能作为 CI 门禁。

## 配置

配置加载顺序：先读取 `CONFIG_PATH` 指定的 YAML（默认 `configs/config.dev.yaml`），再用环境变量覆盖，环境变量优先级最高。数据库、Redis、RabbitMQ 的密码、JWT 密钥和运行模式只从环境变量读取，YAML 中即使存在同名字段也会被忽略。API 已创建可恢复 cache Runtime 并将其接入注册/登录限流；Redis 初始连接失败只记录脱敏事件，API 仍会启动，后续请求由 Runtime 按冷却和单探针机制恢复。worker 通过可重连 runtime 建立 RabbitMQ 连接并运行 relay/consumer，API 与 sweeper 不建立 MQ 连接。`observe.pprof` 仍是后续功能预留，当前加载器不读取它，现有 `/ready` 只依赖 MySQL。

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
| Redis DB | `REDIS_DB` | `0`；供注册/登录限流 Runtime 选择逻辑库 |
| Redis 密码 | `REDIS_PASSWORD` | 仅从环境变量读取；Compose Redis 将其传给 `requirepass` |
| RabbitMQ 主机 | `RABBITMQ_HOST` | 本地默认 `localhost`；Docker 容器由 Compose 覆盖为 `rabbitmq` |
| RabbitMQ 端口 | `RABBITMQ_PORT` | `5672` |
| RabbitMQ 用户 | `RABBITMQ_DEFAULT_USER` | 默认 `gofeed`；覆盖 YAML 用户名并用于 Compose RabbitMQ 首次初始化 |
| RabbitMQ 密码 | `RABBITMQ_DEFAULT_PASS` | 仅从环境变量读取；用于 Compose RabbitMQ 首次初始化 |

### 本地开发配置

本机 MySQL 的完整初始化、迁移和直接启动流程见上方「本地开发（不使用 Compose）」。`backend/.env` 存放数据库和中间件密码及固定 `JWT_SECRET`，`backend/configs/config.dev.yaml` 存放非敏感配置；从 `backend` 目录运行的 API 和 worker 都会读取这些配置。仅启动 API 仍只要求 MySQL；Redis 已接入注册/登录限流，但不可用时 API 启动和这两个业务接口均按 fail-open 继续。要验证异步发布闭环，需在填好 `backend/.env` 后启动 RabbitMQ 并运行 worker（可执行 `docker compose up -d rabbitmq`，再直接运行 worker）。

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

`GET /health` 只检查 API 进程存活，`GET /ready` 还会在 2 秒内探测 MySQL，数据库不可用时返回 `503`。Compose 使用 `/ready` 作为 backend 健康检查，frontend 仅在 backend 健康后启动；Redis/RabbitMQ 各自有容器健康检查，但 Redis 的限流 Runtime 不进入 API 就绪条件。每个响应会返回 `X-Request-ID`；客户端可复用该请求头值关联服务端的 `http_request`、`http_request_error` 和 `readiness_check` 日志。sweeper 每项清扫和每轮汇总都会记录事件、结果、耗时、删除数量及失败数量。当前未启用或暴露 pprof；后续实现会参考 `feedsystem` 的隔离模式，以独立 `ServeMux`、仅回环监听、显式开关和独立关闭生命周期提供诊断端点，而不将其注册到 Gin 路由。

## 已完成能力概述

- 已具备匿名短视频流、账户与会话、视频草稿上传/发布、公开详情、个人主页、我的视频、头像和互动（点赞、评论、关注）。当前 API 契约见 [`API.md`](./API.md)。
- 已具备可靠异步发布：MySQL 事务写入 Outbox，Worker 经 RabbitMQ 完成媒体处理；relay 以确认、租约和退避恢复，consumer 使用 CAS 幂等及 `1s/5s/30s` 重试/DLQ。
- 已具备媒体与数据清扫、公开视频完整性过滤、游标分页、请求观测与 MySQL 就绪检查；Redis 仅用于登录/注册限流，故障时 fail-open，`/ready` 仍只依赖 MySQL。
- 用户列表兼容分页（后端及前端）已提交为 `455849e`，旧的无参数读取保持兼容；过期本地媒体孤儿回收已提交为 `d0902a3`，由 sweeper 按配置宽限期执行。
- 前端已提供 Feed、登录/注册、发布、详情、用户/个人主页、我的视频和账户设置；网络暂态失败可恢复，登录/注册限流会展示服务端 `Retry-After`。

## 当前工作与后续

- F0 后端已分模块提交：领域与应用逻辑 `8394035`、既有仓储适配 `224d8ff`、HTTP 入口与 API 契约 `7541269`。新增 `GET /api/feed?scene=timeline`，按 GCFeed 的 `domain/feed`、`application/feed`、`infra/persistence/feed`、`interfaces/http/feed` 目录分层。Feed 用例已接管分页和批量组装，外层适配器复用原仓储及公开规则，HTTP DTO 单独转换；不再调用旧视频 Service。省略场景默认 Timeline，新旧游标不可混用，未启用场景返回 `501`。契约见 [`API.md`](./API.md)。
- F1-A 已按用户指令提交为 `a7e2bd4`：增加 `video.Repository.GetPublishedByIDs` 与独立 Feed `CardReader`，最多读取 51 个去重后的有效视频 ID，沿用公开规则并共享字段转换；当前请求用例尚未调用该能力。编译通过，未进行测试或真实 MySQL 验收；具体边界见 [F1 小步模块与 F1-A 读取契约](./FEED_CORE_EVOLUTION_PLAN.md#f1-小步模块与-f1-a-读取契约)。
- 本轮只完成后端与文档，前端代码和 `*_test.go` 均未改动，未运行测试或联调。实现状态不代表真实 MySQL 或联合验收通过；暂缓项已在 [F0 完成项与本轮暂缓项](./FEED_CORE_EVOLUTION_PLAN.md#f0-完成项与本轮暂缓项) 列明，补齐后再按实际结果更新。
- **待补：前端改动**（Feed 请求切换新入口及对应交互接入）、**后端与前端单元测试**（新契约、分页、组装及失败处理）、真实 MySQL/页面联合验收；前端当前仍请求 `/api/video`。
- 会话校验缓存继续延后，只有可量化收益时才立项。共享存储、时区一致性、`observe.pprof` 与 `gorm.io/gen` 保持独立设计。
- 后续补齐 F0 与 F1-A 的运行验收及所需前端接入；下一项后端模块为 [F1-B 页缓存读写适配](./FEED_CORE_EVOLUTION_PLAN.md#f1-b-下一模块边界)，完成后等待 review，再推进 F1-C 接入新 Feed 用例。Feed Redis 缓存、Following、Hot、推荐和 Reconciler 尚不存在，F2–F6 仍为规划。

每个后续模块均按“设计契约 → 实现 → 验证 → 独立提交 → review”推进；开始前检查工作树、当前路由、迁移和 [`AGENTS.md`](./AGENTS.md)。
