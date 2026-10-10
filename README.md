# GoFeed —— 一个视频feed流系统

接口的当前路径、请求和响应以 [`API.md`](./API.md) 及 `backend/internal/router/router.go` 为准；开发、迁移、配置和提交约束见 [`AGENTS.md`](./AGENTS.md)。

系统阅读项目可从 [`GoFeed 源码导读`](./docs/SOURCE_CODE_GUIDE.md) 开始，包含 Timeline/Following、页与卡片缓存、Outbox 发布与预热、故障恢复，以及向量推荐的当前边界和扩展位置。

后续任务与待补验收统一维护在[开发计划](./docs/DEVELOPMENT_PLAN.md)，开头提供阅读导航。[R2-B 粉丝/关注列表与游标](./docs/DEVELOPMENT_PLAN.md#67-r2-b-粉丝关注列表与游标已提交) 已提交为 `f9481b2`；[R2-C 关系持久化与旧 social 收口](./docs/DEVELOPMENT_PLAN.md#68-r2-c-关系持久化与旧-social-收口已提交) 后端为 `ea36d40`；[R3-A 三个匿名账户读取](./docs/DEVELOPMENT_PLAN.md#69-r3-a-三个匿名账户读取已提交) 后端/API 为 `35a6fe0`。[R3-B 注册接口](./docs/DEVELOPMENT_PLAN.md#610-r3-b-注册接口已提交) 后端/API 已提交为 `a834d46`，保留原绑定、字节长度校验、限流、错误和公开响应，复用原 User 仓储。[R3-C 登录、刷新与退出](./docs/DEVELOPMENT_PLAN.md#611-r3-c-登录刷新与退出已提交) 已提交为 `f20dcdf`，复用原会话、CAS 和 JWT 实现。[R3-D 改密与注销](./docs/DEVELOPMENT_PLAN.md#612-r3-d-改密与注销已提交) 已提交为 `4f4838b`，保留业务更新与全部会话撤销的同一事务。[R3-E 改名、资料与头像](./docs/DEVELOPMENT_PLAN.md#613-r3-e-改名资料与头像已提交) 已提交为 `f5c1260`，保留原头像校验和文件补偿，账户 HTTP 已全部迁入 Account。以上均未推送。[R3-F1 账户跨模块读适配](./docs/DEVELOPMENT_PLAN.md#r3-f1账户跨模块读适配已提交) 已提交为 `c335902`，未推送。[R3-F2 用户持久化](./docs/DEVELOPMENT_PLAN.md#r3-f2用户持久化已提交) 已提交为 `267463e`，未推送；[R3-G1 JWT/认证适配](./docs/DEVELOPMENT_PLAN.md#r3-g1jwt-与认证适配已提交) 已提交为 `e84f783`，未推送；[R3-G2 会话用例与持久化](./docs/DEVELOPMENT_PLAN.md#r3-g2会话用例与持久化已提交) 已提交为 `fe6959d`，未推送；[R4-A1 已发布详情与公开列表](./docs/DEVELOPMENT_PLAN.md#r4-a1已发布详情与公开列表已提交) 已提交为 `d94bff7`，未推送；[R4-A2 本人视频列表](./docs/DEVELOPMENT_PLAN.md#r4-a2本人视频列表已提交) 已提交为 `b4e145b`，未推送；[R4-A3 视频处理状态读取](./docs/DEVELOPMENT_PLAN.md#r4-a3视频处理状态读取已提交) 已提交为 `1b0acfc`，未推送；[R4-B1 草稿创建与读取](./docs/DEVELOPMENT_PLAN.md#r4-b1草稿创建与读取已提交) 已提交为 `2f315e8`，未推送；[R4-B2 共享媒体规则与本地存储](./docs/DEVELOPMENT_PLAN.md#r4-b2共享媒体规则与本地存储归层已提交) 已提交为 `e56d7bf`，未推送；[R4-B3 草稿视频上传](./docs/DEVELOPMENT_PLAN.md#r4-b3草稿视频上传已提交) 已提交为 `6bc4926`，未推送；[R4-B4 草稿封面上传](./docs/DEVELOPMENT_PLAN.md#r4-b4草稿封面上传已提交) 已提交为 `fefc4c4`，未推送；[R4-C1 草稿发布](./docs/DEVELOPMENT_PLAN.md#r4-c1草稿发布已提交) 已提交为 `9f0a393`，未推送；[R4-C2 草稿丢弃](./docs/DEVELOPMENT_PLAN.md#r4-c2草稿丢弃已提交) 已提交为 `1048bc5`，未推送；[R4-C3 已发布视频删除](./docs/DEVELOPMENT_PLAN.md#r4-c3已发布视频删除已提交) 已提交为 `ff11f7f`，未推送；[R4-D1 作者读取消费边界](./docs/DEVELOPMENT_PLAN.md#r4-d1作者读取消费边界已提交) 已提交为 `6455b91`，未推送；[R4-D2 互动统计消费边界](./docs/DEVELOPMENT_PLAN.md#r4-d2互动统计消费边界已提交) 已提交为 `8d72c9f`，未推送；[R4-D3 列表查询位置消费边界](./docs/DEVELOPMENT_PLAN.md#r4-d3列表查询位置消费边界已提交) 已提交为 `ab3cee0`，未推送；[R4-D4 草稿媒体绑定值消费边界](./docs/DEVELOPMENT_PLAN.md#r4-d4草稿媒体绑定值消费边界已提交) 已提交，未推送（提交标识见 Git 历史）；其余模块仍按第 6.14 节分别推进。后续不运行 Go 单元测试，默认仅静态检查、构建与差异检查；构建与源码对照不代表真实作者读取、资料统计或 HTTP 回归。Feed 功能路线分别推进，每个模块先 review 再提交。

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
# 另开一个 backend 终端；先应用到 000010，worker 需要 RabbitMQ，热度写入还需要 Redis
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

配置加载顺序：先读取 `CONFIG_PATH` 指定的 YAML（默认 `configs/config.dev.yaml`），再用环境变量覆盖。数据库、Redis、RabbitMQ 的密码、JWT 密钥和运行模式只从环境变量读取。API 为限流和 Feed 分别创建可恢复 Redis Runtime；Redis 不可用时 API 仍可启动，Feed 缓存读取失败回源 MySQL。页缓存、卡片缓存、发布事件、预热消费、互动事实记录、Relay 与热度消费直接启用，已移除对应布尔配置及环境变量入口。worker 使用可重连 RabbitMQ Runtime；API 与 sweeper 不连接 MQ。`observe.pprof` 仍为预留，现有 `/ready` 只依赖 MySQL。

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
| Redis DB | `REDIS_DB` | `0`；供限流与独立 Feed Runtime 选择逻辑库 |
| Redis 密码 | `REDIS_PASSWORD` | 仅从环境变量读取；Compose Redis 将其传给 `requirepass` |
| 热度代际 | `FEED_HEAT_GENERATION` | 默认 `initial`；同一代际锁定规则指纹，修改规则或重建使用新代际 |
| 热度窗口与保留宽限 | `FEED_HEAT_WINDOW_MINUTES`、`FEED_HEAT_RETENTION_GRACE_MINUTES` | 默认 `60`、`10` 分钟；桶到期由原创建分钟计算，重复投递不延长窗口 |
| 热度去重保留 | `FEED_HEAT_DEDUPE_TTL_HOURS` | 默认 `24` 小时，必须至少覆盖窗口、保留宽限及额外一分钟 |
| 热度权重 | `FEED_HEAT_LIKE_WEIGHT`、`FEED_HEAT_COMMENT_WEIGHT` | 默认 `3`、`5`；新增与撤销归属原互动创建分钟，保留中间负贡献；初始参数不代表效果验收 |
| 分钟桶容量 | `FEED_HEAT_MAX_VIDEOS_PER_MINUTE`、`FEED_HEAT_MAX_EVENTS_PER_MINUTE` | 默认 `10000` 视频、`100000` 事件；超限重试/死信，不伪造计分成功 |
| RabbitMQ 主机 | `RABBITMQ_HOST` | 本地默认 `localhost`；Docker 容器由 Compose 覆盖为 `rabbitmq` |
| RabbitMQ 端口 | `RABBITMQ_PORT` | `5672` |
| RabbitMQ 用户 | `RABBITMQ_DEFAULT_USER` | 默认 `gofeed`；覆盖 YAML 用户名并用于 Compose RabbitMQ 首次初始化 |
| RabbitMQ 密码 | `RABBITMQ_DEFAULT_PASS` | 仅从环境变量读取；用于 Compose RabbitMQ 首次初始化 |

### 本地开发配置

本机 MySQL 的完整初始化、迁移和直接启动流程见上方「本地开发（不使用 Compose）」。`backend/.env` 存放数据库和中间件密码及固定 `JWT_SECRET`，`backend/configs/config.dev.yaml` 存放非敏感配置；从 `backend` 目录运行的 API 和 worker 都会读取这些配置。仅启动 API 仍只要求 MySQL；Redis 已接入注册/登录限流，但不可用时 API 启动和这两个业务接口均按 fail-open 继续。要验证异步发布闭环，需在填好 `backend/.env` 后启动 RabbitMQ 并运行 worker（可执行 `docker compose up -d rabbitmq`，再直接运行 worker）。

Feed 页缓存直接装配，只作用于 `/api/feed?scene=timeline` 的带游标后续页，首屏仍查 MySQL；默认 TTL 30 秒、单次操作上限 100ms。命中后批量检查整页（含探测记录）的当前公开卡片，失效时按原游标整页回源；作者和互动统计实时读取。Redis 失败回源，短超时同步回填失败不影响成功响应，不启动后台回填任务。每实例最多并发处理 32 个 Timeline 缓存链路请求和 16 次缓存操作；请求容量耗尽返回安全 503，缓存容量耗尽直接回源。旧 `/api/video` 不受这些限制影响。

基础卡片缓存直接装配，仅在后续页的页缓存命中路径使用：先按完整公开规则查询 MySQL 的 `id`、`author_id`、`published_at`，再批量读卡片；卡片必须匹配当前状态，缺失或坏值批量回源。作者和统计继续实时读取。Key 为 `gofeed:feed:card:v1:<video_id>`，默认 TTL 30 秒、单卡片上限 16 KiB；超大卡片不回填。卡片与页缓存共享 16 次操作容量和同一 Feed Runtime；缓存故障不影响成功回源，MySQL 故障仍返回错误。`feed_card_cache` 记录命中、回源及载荷/读写失败。

缓存与异步链路不再提供功能开关，旧 `.env` / YAML 中的七个布尔项不会影响当前装配。Redis 故障仍按既有读取规则回源 MySQL；计划性回退需使用对应代码版本，并保留能够识别发布/互动事件的 worker 处理存量。当前没有已发布内容编辑入口，未来新增编辑前仍需补齐内容版本及旧写入围栏。

Worker 参照 GCFeed 的入口编排，在 [cmd/worker/main.go](./backend/cmd/worker/main.go) 的 `startWorkers` 中集中装配各条链路，不新增启动包。主函数处理配置、连接与关闭信号，退出时等待全部循环结束再关闭 Redis、RabbitMQ 和数据库。

F2-B2 的 worker 在实际 `processing → published` 变更时同事务写 `video.published`，经 `feed.card.warm` 预热当前 MySQL 公开卡片。独立消费使用 5 秒上下文、100ms 缓存操作超时、`1s/5s/30s` 重试与 DLQ；重复投递可覆盖当前卡片，不可见视频清理精确键，超大载荷跳过。首次连接和重连均声明 `video.process` 与预热拓扑，发布沿用 mandatory/Return 检查。

启动新版 API/worker 前须应用仓库迁移至 `000010_interaction_outbox`；建议先启动能够识别全部事件的 worker，再启动 API。2026-10-05 本机 `feedsystem` 已完成版本 9→10 的迁移与结构核对，其他部署目标仍须单独确认。历史验收及迁移摘要见开发计划第 5.1 节，当前真实链路缺口见第 5.2 节。

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

`GET /health` 只检查 API 进程存活，`GET /ready` 还会在 2 秒内探测 MySQL，数据库不可用时返回 `503`。Compose 使用 `/ready` 作为 backend 健康检查，frontend 仅在 backend 健康后启动；Redis/RabbitMQ 各自有容器健康检查，但 Redis Runtime 不进入 API 就绪条件。每个响应会返回 `X-Request-ID`；客户端可复用该请求头值关联服务端的 `http_request`、`http_request_error` 和 `readiness_check` 日志。Feed 缓存的 `feed_page_cache` 日志记录命中、未命中、失效、读写失败、MySQL 读取及容量限制的结果与耗时，不输出游标或 Redis 错误详情；这些基础事件尚未接入指标/告警平台。sweeper 每项清扫和每轮汇总都会记录事件、结果、耗时、删除数量及失败数量。当前未启用或暴露 pprof；后续实现会参考 `feedsystem` 的隔离模式，以独立 `ServeMux`、仅回环监听、显式开关和独立关闭生命周期提供诊断端点，而不将其注册到 Gin 路由。

## 已完成能力概述

以下为源码实现、提交状态与历史验收摘要；未完成的验收缺口另列在开发计划中。

- 账户与会话、匿名视频流、草稿上传/异步发布、公开详情、个人主页、我的视频、头像、点赞/评论/关注及对应前端页面；接口契约见 [API.md](./API.md)。
- 视频、互动与关系列表使用带版本和范围的游标；用户列表兼容分页已提交为 `455849e`，保留旧的无参数读取。
- `internal/error` 统一账户、视频、互动与关系的 HTTP 错误分类和安全响应，保持既有状态码与 `{"error":"..."}` 形状；后台任务保留自身错误语义。
- MySQL 事务 + Outbox 可靠发布，RabbitMQ 运行时连接恢复与拓扑重建、publisher confirm、派发租约/退避、消费 CAS 幂等和 `1s/5s/30s` 重试/DLQ。F2-A `48ce8df` 支持按 `event_type` 装配发布目标、快照检查与载荷构造；当前同时装配 `video.process` 与 `video.published` 的派发及消费；未知类型继续固定退避。
- 数据和媒体清扫、草稿租约、公开视频完整性过滤、请求日志与 MySQL 就绪检查；本地媒体孤儿回收已提交为 `d0902a3`，按宽限期与引用检查清理。
- 登录/注册 Redis 固定窗口限流、故障 fail-open 和冷却/单探针恢复；页面按服务端 `Retry-After` 等待。Redis 不进入 `/ready`。
- F0 新增匿名 Timeline `/api/feed` 的四层读取边界；F1-A 批量公开卡片 `a7e2bd4`、F1-B 轻量页缓存端口与适配 `509c123` 已提交。F1-C 已提交为 `f772349`，接入后续页缓存、命中校验、MySQL 回源、独立 Runtime 与有界并发。首页已通过 `896f4e1` 切换为 `/api/feed?scene=timeline&limit=12`，作者主页继续使用 `/api/video?author_id=...`；取消、去重、分页错误态、手动重试与播放暂停保持原行为。测试覆盖及实际依赖参与情况见 [开发计划](./docs/DEVELOPMENT_PLAN.md) 第 5 节；缓存收益与容量压测仍待验证。

历史 Timeline/卡片浏览器验收使用真实 Go/MySQL/Redis，独立观测命中并比较桌面/移动响应；媒体为本地夹具。F2 路由与预热另有真实依赖验收，部分 ACK/拓扑断言使用 fake，未开启专项不计通过。相关工具部分已随测试精简删除，历史结果及剩余缺口见开发计划第 5 节；不据此宣称缓存性能或当前整链验收完成。

切换或回退首页入口后须重新加载并清空分页状态，Feed 游标不能用于旧 `/api/video`。涉及事件生产与消费的回退先处理存量及恢复清单，详见开发计划第 5.3 节。

## 后续开发

首页 Timeline、基础卡片缓存、发布预热和 MySQL Following 已分别提交并保留历史验收记录。2026-10-05 默认装配、worker 入口编排与热度校验整理已按用户指令提交为 `8e059e2`，七项 Feed/互动能力直接装配，不再依赖布尔开关。历史普通/race、MySQL/Redis/RabbitMQ、浏览器证据及跳过范围见 [开发计划](./docs/DEVELOPMENT_PLAN.md) 第 5 节，不能替代本次行为变更的运行验收。

F4-A1 互动事实存储已提交为 `82c01d5`，F4-A2 可靠派发已提交为 `65cebf6`。当前点赞、取消点赞、创建评论和删除评论直接通过互动四层提交业务行与 `interaction_outbox_events`，仅真实变更写事件；插入失败使同一事务失败，提交后统计/作者读取失败不撤销事实。接口与认证契约不变。

2026-10-06 R1-A/R1-B1 将六个互动 HTTP、ORM、直接读取和事务收口到 Interaction；R1-B2 进一步迁入批量互动统计和完整公开视频获赞读取，删除 social 的旧统计方法及 ORM 别名。Domain/Application 只使用独立读模型与端口，外层适配旧 Video、Feed 和用户资料接口；粉丝/关注计数现通过 Relation CountReader 读取。完整公开视频仍复用 `video.PublicVideoQuery`，旧 v1 评论游标、响应、作者占位和事务语义保持兼容。非空互动批次固定两条聚合 SQL，资料统计按获赞→粉丝→关注执行三条 SQL；提交摘要见开发计划第 6.4 节，验证及缺口见第 5 节。

R2-A 将 `GET/PUT/DELETE /api/user/auth/:id/follow` 接入 Relation 的 Domain/Application/Infrastructure/HTTP，保留认证、200 响应、错误文案、自关注校验和重复关注/取关语义。R2-B 将两个匿名粉丝/关注列表接入同一边界：独立领域列表模型/位置/读取端口，Application 负责默认 20、最大 50、limit+1 与绑定列表/用户的原 v1 游标，HTTP 单独组装 DTO；后端与 API 已提交为 `f9481b2`。R2-C 将唯一 Follow ORM、原 SQL 及双向计数迁到 Relation Infrastructure，直接实现领域端口，并切换资料统计与 Following 活动观看者依赖；确认无引用后删除旧 social，后端提交为 `ea36d40`。Following 的视频 SQL 仍归旧 Video；活动观看者不存在仍为 Feed 401，依赖失败仍为安全 503，Relation 保留原 404/500。提交前保留 repo_test.go 已删除的状态，当时为 7 个测试文件、51 个函数；只作必要迁移适配，没有新增测试或扩展断言。原有 router 流程继续验证固定旧 v1 续页及 Following 非空六条/空页三条 SQL；双向列表两条 SQL 的历史断言已删除，资料统计获赞→粉丝→关注三条 SQL 仅核对实现。Go 1.27.1 windows/amd64 下直接执行 vet、普通全量及无缓存 race 全量均通过，普通使用 7 包缓存，race 重跑 7 包；补充 JSON 为 51 PASS/0 FAIL/0 SKIP。验证与未覆盖项见开发计划第 5、6.8 节。

R3-A 将三个匿名账户 GET 接入独立 Account Domain/Application/Infrastructure/HTTP：公开账户与资料模型、ID 位置和小读取端口归 Domain，列表全量/分页双模式及原 v1 游标归 Application，Infrastructure 复用旧 user.Repository、完整公开视频计数和现有资料统计适配器，HTTP 单独组装 DTO。保留 query 存在性、先 limit 后游标、原状态码/文案、公开字段及四项零值统计；正常资料装配仍按账户→视频数→获赞→粉丝→关注读取，共五条 SQL。旧读取方法和无用途助手已删除，R3-A 当时保留登录/刷新/头像使用的 GetByID、publicUser、共享响应及仓储/适配类型；用户和会话 ORM、作者批量读取未迁移。后端/API 已提交为 `35a6fe0`，没有推送。实施轮的普通/race 与 51 PASS/0 FAIL/0 SKIP 是当时 7 文件版本的历史证据；R3-A 提交轮只执行 vet、build 和差异检查，均通过，没有执行 Go 测试。随后已有 `a483843` 删除 Feed/Video 两个测试文件，当前为 5 文件/36 函数。账户旧 v1 的源码兼容证据和未覆盖专项见开发计划第 5、6.9 节。

R3-C 将登录、刷新与退出接入 Account 会话用例和 HTTP，使用独立凭据/会话/令牌模型及小端口。Infrastructure 委托原 User 仓储、bcrypt 比较、SessionService 与 JWT 签发，保留登录限流和 binding、仅用户名 TrimSpace、先比较再建会话、刷新先 CAS 再读用户/签发及失败撤销、退出当前会话和原错误分类。确认引用后删除被替代的旧登录/刷新/退出方法、DTO 与助手，保留改密/注销事务、头像读取、User/AuthSession ORM 和原会话 SQL。仅适配保留测试的装配，未新增测试或断言。Go 1.27.1 下 vet/build、内层依赖和源码对照检查通过；未运行 Go 测试或真实 HTTP/MySQL/Redis 会话回归。已提交为 `f20dcdf`，未推送；提交轮没有 Go 源码变更，沿用实施轮 vet/build 结果并检查精确暂存范围/空白。边界见开发计划第 6.11 节。

R3-D 将改密与注销接入 Account。Domain 保留新密码字节规则与独立输入，Application 编排校验→读取凭据→比较旧密码→哈希→原子写入；Infrastructure 将原密码 CAS/软删除与全部会话撤销分别放在同一个事务中，复用原仓储方法，没有新 SQL、ORM、锁或重试。保留 binding、密码不 Trim、原状态码/文案及成功响应。确认引用后删除替代的旧方法/DTO/错误依赖，保留改名、资料/头像和头像读取；保留测试仅适配装配/调用，断言未改。vet/build、依赖与 35 项源码对照检查通过，未运行 Go 测试或真实改密/注销及事务故障回归。已提交为 `4f4838b`，未推送；提交轮代码未变，沿用实施轮 vet/build 结果并检查精确暂存范围/空白。边界见开发计划第 6.12 节。

R3-E 已提交为 `f5c1260`，将改名、资料与头像的 HTTP/用例迁入 Account，账户入口全部归四层。Domain 保留独立输入、原改名字节规则及头像大小/文件头规则；Application 使用小读取/写入/存储端口编排头像保存、写库及新旧对象清理。Infrastructure 复用原 User 仓储及 LocalStorage，不改 SQL、ORM、路径或文件补偿顺序。确认引用后删除旧 Controller/avatar.go 和替代的 Service 写方法/DTO，保留测试夹具读取、仓储/ORM与旧统计适配接口。实施轮 vet/build、内层依赖及 36 项源码对照检查通过，30 个保护源码及 5 个测试文件未改；未运行 Go 测试或真实改名/资料/头像、文件补偿回归。范围与证据见开发计划第 6.13 节。

R3-F1 已提交为 `c335902`，将作者读取迁入现有 Account Infrastructure，通过独立 PublicAccountReader 返回公开账户，批量仍委托原 GetByIDs 的三字段投影；保留 0 占位、首次出现去重、单次查询、nil 行过滤、缺失/注销占位及故障传播。Interaction 直接返回 Domain Account ProfileMetrics，账户统计/视频数适配使用领域小接口并保留 nil 和 accountError。确认引用后仅删除旧作者适配、旧统计接口/结果；User/AuthSession ORM、User 仓储、夹具 Service.GetByID 和仍用错误保留。vet/build 已通过；未运行 Go 测试、真实作者读取、资料统计或 HTTP 回归。提交轮 Go 源码未改，检查 11 个精确暂存路径及空白后提交，未推送。

R3-F2 已提交为 `267463e`，将唯一 User ORM 和原仓储全部 12 个方法迁入 Account Infrastructure，Creator/Reader/CredentialReader/ProfileWriter/SecurityWriter、router 和 sweeper 接入新仓储。保留全部 ORM 标签、SQL/投影/keyset/软删除、1062、密码 CAS、同一事务的账户更新与会话撤销，以及清会话后硬删用户；AuthSession 与会话算法未迁。确认引用后删除旧 user 包、无用途 DTO 和夹具 Service，保留 5 个测试文件/36 个函数，仅作必要迁移与装配/符号适配。vet/build 与源码检查通过，目标库仅只读核对版本 10、dirty=false 及列/索引；未运行 Go 测试或真实仓储/事务/HTTP/清扫回归。提交轮 Go 源码未改，沿用实施轮检查结果，核对精确暂存范围/空白后提交，未推送。

R3-G1 已提交为 `e84f783`，将 JWT 签发/解析、Secret 与随机生成迁入 infra/jwt，将共享 Authorization/上下文适配迁入 Interfaces HTTP Auth；会话校验使用仅含 Validate 的消费方小端口。保留 HS256、user_id/username/sid、十五分钟访问令牌、原密钥缓存/回退和解析规则、认证顺序、401 文案、Gin 上下文及 nil 服务拒绝认证。旧 SessionService 与账户签发适配仅换 JWT 调用，router/各 HTTP 消费方切换新包；当时会话 ORM/SQL、哈希、CAS 和编排留 G2。两个保留测试只改导入/符号，断言未改；vet/build、源码/依赖与文档/差异检查通过，未运行 Go 测试或真实 JWT/认证/HTTP 回归。提交轮代码未变，沿用实施轮结果，核对 17 个精确路径与暂存差异后提交，未推送。

R3-G2 已提交为 `fe6959d`，将会话创建/轮换/校验迁入 Account Application，唯一 AuthSession ORM 与原六个仓储方法迁入 Account Persistence；随机、SHA-256 hex 与访问令牌签发通过小端口接 infra/jwt。保留两次 32 字节随机生成顺序、七天固定会话到期、入库后签发、先查→生成→CAS→读账户→签发、失败残留/尽力撤销和改密/注销同一 tx 的撤销。确认全部引用后删除旧 auth 与冗余 legacy_sessions/持久化 issuer 转换，router 和一个保留测试仅换装配/类型，断言未改。vet/build、32 项源码及内层依赖检查通过，目标库版本 10、dirty=false，列/索引及用户/会话聚合实施前后相同；没有数据库写入，未运行 Go 测试或真实会话/认证/HTTP/事务回归。提交轮源码未改，核对 15 个精确路径/暂存差异后提交，未推送。

R4-A1 已提交为 `d94bff7`，将 `GET /api/video` 的全局/按作者列表与 `GET /api/video/:id` 已发布详情迁入 Video 四层：Domain 提供独立公开快照、状态和小端口，Application 保留原 v1 视频游标、limit+1 及读取顺序，HTTP 单独组装原 DTO，Infrastructure 仍委托原仓储与作者/统计适配。完整公开规则只有一份，旧 Video/Feed 通过字段桥接复用；SQL、ORM、认证、JSON/omitempty 和错误优先级未变。确认引用后仅移除旧公开入口及独占助手，本人列表与处理状态当前已随下述 R4-A2/A3 迁移，草稿创建/读取当前已随 R4-B1 迁移，媒体上传及其余写入留后续。vet/build、45 项源码对照及文档/差异检查通过；全部 5 个测试文件未改，未运行 Go 测试或真实视频读取/HTTP 回归。目标库只读核对版本 10、dirty=false、videos 21 列/8 索引，实施前后元数据/聚合相同，无数据库写入。提交轮源码未改，核对 15 个精确路径/暂存差异后提交，未推送。

R4-A2 将 `GET /api/video/auth/mine` 接入现有 Video 四层，独立 AuthorVideoListReader 只含一个方法，仍委托原 GetAuthorVideoList；Application 复用原列表组装、互动/作者批量逻辑和唯一 v1 编解码，mine 继续绑定当前用户。JWT/session→当前用户→limit 文本→ID/依赖/limit 范围/游标→limit+1 查询→过滤截断→互动→作者顺序及原 JSON/错误优先级保持，源码预算仍非空五条/空页两条 SQL（含会话）。确认引用后删除旧 mine 入口、独占组装/游标助手与 VideoItem/ListResponse，旧 Service 移除无用途作者/统计依赖；唯一 ORM、完整仓储和仍用类型/错误保留。vet/build、38 项源码对照及文档/差异检查通过，全部 5 个保留测试文件未改；目标库仅只读核对版本 10、dirty=false、videos 21 列/8 索引，元数据/聚合前后相同，无数据库写入。未运行 Go 测试或真实本人列表/认证/游标/HTTP 回归，源码与构建不代表运行验收；提交轮源码未改，核对 13 个精确路径/暂存差异后提交为 `b4e145b`，未推送。

R4-A3 仅将 `GET /api/video/auth/:id/status` 接入现有 Video 四层。Domain 提供独立状态快照/结果和单方法 ProcessingStatusReader，Infrastructure 仍只调用一次原 GetByID，Application 按原顺序检查 ID、依赖、所属作者和 processing/published/rejected；不增加媒体完整或发布时间检查。HTTP 保留认证、路径解析、原四个顶层字段、null 时间、空拒绝理由及错误文案/分类，正常装配源码预算仍两条 SQL（会话与视频各一条）。仅删除旧状态 Handler/Service 方法及 DTO；原仓储/SQL/ORM、写事务与其他消费者保持。worker 保留夹具仅改一个导入与三个 DTO 类型引用，断言不变，未运行。vet/build、28 项源码检查、内层依赖与文档/差异检查通过；目标库 SELECT-only 元数据/聚合前后相同，无数据库写入。未运行 Go 测试或真实状态读取/认证/HTTP/故障回归；提交轮源码未改，沿用实施轮检查，核对 12 个精确路径/暂存差异后提交为 `1b0acfc`，未推送。

R4-B1 仅将 `POST /api/video/auth/drafts` 与 `GET /api/video/auth/drafts/:id` 迁入现有 Video 四层。Domain 提供独立草稿模型、单方法创建/读取端口与原 trim/rune 校验；HTTP 保留原 ShouldBindJSON、binding/JSON/omitempty、认证、201/200 的 draft 包装及错误文案。Infrastructure 分别只委托一次原 Create/GetByID，创建仍四字段写入并使用原数据库回填，读取仍先作者 403、再 draft/purging 或 404；没有新 SQL、事务、预查或重读。媒体完成标识复用唯一 Domain 三字段非空规则，旧写入只改两处标量调用；仅删替代入口/用例和旧 DraftRequest，仍用 DraftItem、ORM、仓储方法与错误保留。vet/build、33 项源码、内层依赖及文档/差异检查通过，全部 5 个测试文件原样保留、未运行；数据库仅只读元数据/聚合前后核对，无数据库写入。未运行真实草稿创建/读取、认证/HTTP 或故障回归；提交轮 Go 源码未改，沿用实施轮 vet/build，重新核对 33 项源码、文档及精确暂存差异后提交为 `2f315e8`，共 12 文件，未推送；媒体上传/存储、发布/丢弃留后续。

R4-B2 已提交为 `e56d7bf`，将独立 MediaKind/SavedFile、保存/删除/枚举小端口与唯一共享纯规则迁入 Domain Video；唯一 LocalStorage 和已存储媒体校验归 [Infrastructure 媒体存储](./backend/internal/infra/storage/media/local.go)。现有 Video Infrastructure 转换旧类型/结果与媒体错误，保持可选 Remove、nil、错误原文及新旧错误链；router、Account 头像、Worker 校验、cmd/sweeper 只切换必要依赖/装配。原上传 HTTP/用例和锁行绑定事务不变，Account 校验/保存写库补偿、Worker 拒绝重试/ACK、Sweeper 用例/租约/SQL/关闭均保留。仅删除旧 LocalStorage/构造器及被替代的 media_validation.go 实现，旧媒体类型/接口/错误与标量纯规则桥接继续服务未迁消费者。53 项源码检查、50 个内层 Go 文件/10 包依赖、vet/build、文档链接及差异检查通过；一个 worker 保留夹具仅两个导入和一处构造器适配，断言不变。未运行 Go 测试、真实上传/路径安全/头像补偿/Worker/Sweeper/HTTP 或文件故障回归，未访问数据库或启动服务。既有三份文档分析改动保留并同步，本模块已按用户指令提交，未推送；R4-B3 视频上传、R4-B4 封面上传的后续实施结果见下段。

R4-B3 仅将 `POST /api/video/auth/drafts/:id/play` 接入 Video 四层。HTTP 保留认证/路径、原 FormFile/201/四个顶层字段及 200 MiB + 1 MiB 限制、512 字节文件头和 Seek；Application 保存成功后计算展示名，再经 Domain 唯一标量规则校验，检查绑定端口/原名兜底并调用一次原事务，绑定失败仅可选 Remove 尽力清理、忽略删除错误。Infrastructure 只转换值与原错误，不新增 SQL、事务、预读/重读或媒体能力。B3 当时封面入口和旧绑定用例保留，只将原条件改为共享 Domain 调用；删除旧视频上传 wrapper 和两个无引用的私有规则桥接。55 项源码检查、52 个内层文件/10 包依赖、vet/build 与文档/差异检查通过；全部保留测试原样，无夹具适配、未运行。目标 localhost:3306 拒绝连接，真实元数据核对未完成，无数据库写入或服务启动；未运行真实上传/补偿/HTTP/并发/文件或数据库故障回归。实施轮停止等待 review；2026-10-08 用户要求提交并继续，提交轮 Go 源码未变，沿用实施轮 vet/build，重核 55 项源码/内层依赖、文档与 12 个精确暂存路径后提交为 `6bc4926`，未推送；随后进入 R4-B4。

R4-B4 将 `POST /api/video/auth/drafts/:id/cover` 接入同一 Video 四层。B3 上传文件/类型改名为 DraftMediaUpload，视频和封面共用 HTTP 前缀与 Application 保存/绑定/可选删除编排；原视频行为逐段对照不变，封面仍 10 MiB + 1 MiB、前 512 字节/Seek 与 201 四个顶层 cover_* 字段。Domain、唯一存储、绑定适配/原锁行事务/完整仓储/SQL/ORM、Account/Worker/Sweeper 保持。确认生产/测试引用后删除旧封面 wrapper/共享上传 helper、旧 Service 媒体用例、保存适配/端口和四个标量上传桥接；旧发布/丢弃/删除、仍用值/错误/删除枚举适配及无关助手保留。60 项源码检查、52 个内层文件/10 包依赖、270 个保护跟踪文件、vet/build、文档链接与差异检查通过；5 个保留测试文件原样，无夹具适配，未运行。目标 localhost:3306 仍拒绝连接，元数据未核对，无 SELECT/数据库写入或服务启动；未运行真实上传/补偿/HTTP/预算/并发或文件/数据库故障回归。实施轮停止等待 review；按用户指令，9 个精确后端路径分离暂存后提交为 `fefc4c4`，Git 按一处改名统计 8 文件，未推送；共享文档统一同步并单独提交。

当前 API 互动写入和 worker Relay 都依赖迁移 `000010_interaction_outbox`，启动前必须确认已应用。2026-10-05 已使用当前后端配置将本机 `localhost:3306/feedsystem` 从版本 9 迁移至 10，dirty=false；互动事实表的 18 个字段、7 个索引已核对，原有七张业务表记录数未变。这只证明迁移与结构，不代表真实互动/消费链路验收。worker 直接运行独立互动 Relay 与 F4-B1 热度消费（实现提交 `26a3f95`），沿用持久载荷、租约/attempt 围栏、publisher confirm、mandatory/Return、`1s/5s/30s` 重试/DLQ、重连与关闭生命周期。coverage 保持 unverified，真实依赖验收仍待补齐。

热度新增与撤销都计入原互动创建分钟。Lua 在状态 Hash 中同时记录 event_id 收据与绝对分数，再写分钟 ZSET；重复投递可修复未完成的 ZSET 写入而不再次累加，负贡献不在写入时截断。分钟桶按原始时间到期，过期桶不重建；同一代际锁定权重/时间/容量规则。`unverified` 表示尚未证明整个窗口的事实与消费覆盖，当前代码没有将它改为完整的路径；它不是 Redis 连接状态。没有 MySQL 热榜快照、自动重建或完整消费水位，`scene=hot` 仍为 501，派发完成也不表示热榜完整。

R4-C1 仅将 `POST /api/video/auth/drafts/:id/publish` 接入 Video 四层，复用独立 DraftPublisher、小草稿快照与原 DTO。HTTP 保留认证→最多一字节请求体检查→路径→用例→202 draft 包装；Application 保留 ID→端口可用→一次原子写→结果组装。Infrastructure 仅委托原锁行/作者/draft/完整媒体/CAS/pending video.process Outbox 的同一事务，原 SQL、时钟/UUID、JSON/omitempty、错误优先级/cause 与重复发布语义保持，不增加预读/重读或直接 MQ 发送。只删除旧发布 Handler/用例及两个无用途导入，完整仓储/ORM、丢弃/删除和后台保持。vet/build、57 项源码对照、54 个内层文件/10 包、274 个既有工作文件、文档/差异检查通过；五个保留测试文件原样，无夹具适配、未运行。目标库拒绝连接，元数据未核对，无 SELECT/写库或服务启动；未运行真实发布/HTTP/事务/Outbox 故障/预算或 Worker/Sweeper 回归。现有 B4 改动保留；本轮按用户指令，7 个精确后端路径提交为 `9f0a393`，未推送，共享文档统一同步并单独提交；随后仅进入 R4-C2。

R4-C2 仅将 `DELETE /api/video/auth/drafts/:id` 接入 Video 四层，复用独立草稿快照、唯一媒体完成规则与原十字段 DTO。HTTP 保留认证→路径→用例→202 draft 包装；Application 保留 ID→端口可用→一次原子写→结果组装。Infrastructure 委托原锁行/作者/状态事务，将原 GORM 未找到优先归类迁到外层，错误分类/文案保持；draft/rejected 仍转 purging 并清空租约/检查点，重复 purging 仍成功且不写库，请求内不删文件。确认全部引用后仅清理旧丢弃 HTTP/用例、无用途 draftItem helper 与旧 DraftItem；已发布删除、完整仓储/唯一 ORM/SQL、上传/发布、Account/Worker/Sweeper 保持。vet/build、68 项源码对照、56 个内层文件/10 包、277 个保护文件及文档/差异检查通过；保留测试原样，无夹具适配，未运行。目标库拒绝连接，元数据未核对，无 SELECT/写库或服务启动；未运行真实丢弃/HTTP/认证/重复并发/事务故障/查询预算或清扫回归。2026-10-09 按用户指令核对源码/文档与 11 个精确暂存路径，沿用实施轮已通过的 vet/build 后提交为 `1048bc5`，未推送，提交后工作树干净；随后只分析 C3，未实施。

已实现的 [R4-C3 已发布视频删除](./docs/DEVELOPMENT_PLAN.md#r4-c3已发布视频删除已提交) 仅迁原 DELETE HTTP/用例与装配：认证→路径/id→单一依赖→读取→作者→published→条件软删除→空体 204，不读取或绑定请求体。独立两字段快照与小端口复用一次原 GetByID、一次原 DeletePublishedVideo；原仓储全文/SQL/事务/软删除作用域、两阶段未找到优先转换/其他错误链、nil 语义、重复删除 404、原保留期和源码三次 CRUD 预算保持。只清理无用途旧 Controller/Service/依赖接口与私有助手，仍用错误/公开规则桥接/唯一 ORM/完整仓储留 R4-D。vet/build、103 项源码/依赖检查、58 个内层文件/10 包、282 个保护文件与文档/差异检查通过；五个保留测试原样，无夹具适配、未运行。目标库本轮 TCP 拒绝（10061），元数据未核对，无 SELECT/写库/服务启动；未运行真实 HTTP/删除/并发/事务/查询预算/媒体或路径安全/Worker/Sweeper 回归。既有三份文档改动保留并同步；2026-10-09 按用户指令复核 10 个精确路径后提交 C3 为 `ff11f7f`，未推送；随后仅进入 R4-D1。

R4-D1 [作者读取消费边界](./docs/DEVELOPMENT_PLAN.md#r4-d1作者读取消费边界已提交) 已提交为 `6455b91`，未推送。Account 作者适配直接实现 Domain Video.AuthorReader，Video/Feed 接同一领域作者；旧 Author/AuthorReader 与无用途值转换已删除。原单读/批量算法、0/注销占位、首次出现去重、空批次不查/非空一次 GetByIDs、投影/软删除过滤、错误/cause/nil 语义及 Video map 复制保持。ORM/SQL/其他消费者未迁，HTTP/DTO/装配/后台未改。vet/build、89 项源码/依赖检查、58 个内层文件/10 包、283 个保护文件与文档/差异检查通过；保留测试原样，无夹具适配，未运行。目标库本轮 TCP 拒绝（10061），元数据未核对，无 SELECT/写库/服务启动；没有真实作者/HTTP/Feed/预算/故障回归。2026-10-09 按用户指令复核 9 个精确路径（含改名）、283 个保护文件摘要与暂存差异，沿用实施轮 vet/build 后提交为 `6455b91`（Git 计 8 文件），未推送；随后仅进入 R4-D2。

R4-D2 [互动统计消费边界](./docs/DEVELOPMENT_PLAN.md#r4-d2互动统计消费边界已提交) 已提交为 `8d72c9f`，未推送。Interaction 统计适配直接返回 Domain Video.EngagementCounts，Video/Feed 改接领域小端口；清理旧计数值/端口与冗余值转换，保留两层 map 分配/复制、空批次不查、nil 语义及原错误/cause。原两条聚合 SQL、评论软删除作用域、零计数、资料统计顺序、HTTP/DTO/装配和全部 ORM/后台保持。vet/build、93 项源码/依赖检查、58 个内层文件/10 包、283 个保护文件与文档/差异检查通过；保留测试原样，无夹具适配，未运行。目标库实施轮 TCP 拒绝（10061），元数据未核对，无 SELECT/写库/服务启动；没有真实统计/HTTP/Feed/预算/故障或 Worker/Sweeper 回归。2026-10-10 按用户指令完成 D2 提交为 `8d72c9f`，未推送；提交轮重新通过 vet/build、93 项源码/依赖和保护文件摘要检查，仅分析下一步，未实施新模块。

R4-D3 [列表查询位置消费边界](./docs/DEVELOPMENT_PLAN.md#r4-d3列表查询位置消费边界已提交) 已提交为 `ab3cee0`，未推送。原公开、本人作者与 Following 三类列表仓储及四个外层适配复用现有 Domain Video.ListPosition；只改位置类型与必要 import，两个 Video 适配仍复制位置后委托一次，Feed 仍复制各自游标的时间和 VideoID。三个查询函数体/SQL、公开规则、校验顺序和外部游标全文保持；完整引用核对后仅删除旧 Cursor/CursorKind 及对应常量/注释，唯一 Video/Outbox ORM 和完整 Repository 未迁。vet/build 与源码、依赖、文档和差异检查通过；五个保留测试原样，无夹具适配、未运行。目标 localhost:3306/feedsystem 本轮 TCP 拒绝（10061），元数据未核对，无 SELECT/写库/服务启动；没有真实 HTTP、分页、查询预算、数据库或 Worker/Sweeper 回归。实施轮完成后等待 review；本轮按用户指令精确范围提交为 `ab3cee0`，未推送。ORM/仓储方法族与 sweeper 取消仍待独立推进。

R4-D4 [草稿媒体绑定值消费边界](./docs/DEVELOPMENT_PLAN.md#r4-d4草稿媒体绑定值消费边界已提交) 已提交，未推送。仅原 UpdateDraftMedia 与绑定适配消费现有 Domain MediaKind/SavedFile，保留显式两字段结果复制；事务仅替换两个同值常量的归属，锁行→作者→draft→槽位→Save、原错误及单次委托保持。只清理无用途旧 SavedFile；清扫仍用的旧 MediaKind/常量、唯一 ORM/完整仓储/其他 SQL 与媒体能力保持。vet/build、完整源码/引用/依赖及文档/差异检查通过；五个保留测试原样，无夹具适配、未运行。目标 feedsystem 本轮只读核对 version=10、dirty=false、videos 21 列/8 索引，元数据/状态聚合前后相同，无写库/业务绑定/服务启动；不代表真实上传、事务、补偿、查询预算、HTTP 或 Worker/Sweeper 验收。2026-10-10 按用户“提交并继续下一步”指令精确范围提交，未推送；提交标识见 Git 历史。

取消 sweeper、客户端删除请求内立即删除已纳入[独立计划](./docs/DEVELOPMENT_PLAN.md#取消-sweeper-与请求内立即删除已纳入计划未实施)，原清扫归层工作暂缓。当前四类后台回收与删除语义保持，后续须单独处理过期草稿/拒绝视频、注销用户、孤儿媒体与部分失败；C2 兼容迁移保持请求内不删文件与原 202/purging 语义，C3 仍保留条件软删除。

现有指标配置与 Feed 请求回调已单独提交为 `2ff0364`，采集器/监听出口仍未装配，容量工具继续暂缓。F4-A/B1 的并发、投递与消费专项仍待补；删除前的四种互动写入事实失败回滚/恢复记录不代表整个热度链路已验收。Feed 功能路线后续按独立模块推进 F4-B2 的覆盖契约、有界事实扫描、代际重建与 MySQL 快照，再接 F4-C Hot。Following 混合推拉仍需容量收益证据。详细范围与待补验收见 [开发计划](./docs/DEVELOPMENT_PLAN.md) 第 3.5–3.8、5、6 节。架构模块 R3-A/B/C/D/E/F1/F2/G1/G2 已提交，R4-A1 已发布详情与公开列表已提交，R4-A2 本人列表已提交，R4-A3 处理状态读取已提交，R4-B1 草稿创建/读取已提交；R4-B2 共享媒体规则/存储已提交，R4-B3 视频上传已提交为 `6bc4926`，未推送，R4-B4 封面上传已提交为 `fefc4c4`、R4-C1 草稿发布已提交为 `9f0a393`，共享文档/清扫器取消计划提交为 `69d5305`，均未推送；R4-C2 草稿丢弃已提交为 `1048bc5`，未推送；R4-C3 已发布删除已提交为 `ff11f7f`，未推送；R4-D1 作者读取消费边界已提交为 `6455b91`，未推送；R4-D2 互动统计消费边界已提交为 `8d72c9f`，未推送；R4-D3 列表查询位置消费边界已提交为 `ab3cee0`，未推送；R4-D4 草稿媒体绑定值消费边界已提交，未推送（提交标识见 Git 历史）。其余 Video、Worker/Sweeper 与技术包按第 6.14 节独立迁移，最终形成四层职责结构。

F3-A 后端支持 `GET /api/feed?scene=following`：复用 JWT/session 与活动观看者校验，在 MySQL 内关联当前关注关系、活动作者和完整公开视频，使用绑定观看者的独立 keyset 游标，并批量读取作者与当前统计。Following 响应为私有且不使用 Timeline 缓存；Timeline 保持匿名，Hot/Recommend 保持 501。真实 MySQL 用例验证非空页 6 次 SQL、空页 3 次，0/1/32/128 个关注作者下已执行 EXPLAIN ANALYZE；样本不代表生产容量或 p95。已提交的 F3-B 页面支持场景切换、独立分页、`/?scene=following` 与登录回跳；接口见 [API](./API.md)，历史验证与剩余范围见开发计划第 5、3.4 节。

## 文档维护

根目录只保留 README.md、API.md 和 AGENTS.md；后续任务与必要设计归 `docs/DEVELOPMENT_PLAN.md`，该文件纳入 Git 跟踪。任务完成后更新本文的简要能力说明，移除完成方案；API.md 只描述已注册接口，待补测试和验收不能随完成方案一起丢失。
