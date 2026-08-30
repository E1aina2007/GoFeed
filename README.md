# GoFeed —— 一个视频feed流系统

开发流程、当前路线和模块验收规则见 [`DEVELOPMENT.md`](./DEVELOPMENT.md)。

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

跨越草稿版本向下回滚属于维护操作。先停止所有 API 与 sweeper 实例并确认 sweeper 已完全退出，再执行只读检查；有结果时不要运行 `migrate steps -1`，应先显式发布，或运行草稿清扫完成回收。回滚完成前不得重新启动 API 或 sweeper，避免检查与 DDL 之间出现新的 `purging` 草稿。`golang-migrate` 在 down SQL 失败时会将版本留为 dirty，不能把这一检查写成故意失败的迁移 SQL。

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
# go run ./cmd/sweeper  # 按需启动注销用户和到期视频清扫任务
```

另开一个终端启动前端开发服务器：

```bash
cd frontend
pnpm install        # 首次安装依赖
pnpm dev
```

前端开发服务器会代理 `/api` 和 `/static` 到本机后端 `http://localhost:8080`。日常修改 Go 或 Vue 源码不涉及 Docker 镜像；仅新增数据库迁移时运行一次 `migrate ... up`。

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

1. 后端：启动 MySQL 8.0、Redis 7 和 RabbitMQ 3 service，执行 `go vet ./...`、`go build ./...` 和 `go test -race -count=1 ./...`。集成测试会创建临时数据库并应用全部向上迁移；Redis/RabbitMQ 当前只固定服务可用性契约，Go 进程尚不建立客户端连接。
2. 部署配置：从 `backend/.env.example` 和 `backend/configs/config.example.yaml` 生成 CI 临时的忽略配置文件，执行 `docker compose config --quiet`，并检查后端 `/ready`、前端 `service_healthy`、Redis/RabbitMQ 健康检查、持久卷及 API 不依赖中间件启动的契约。不使用真实 `.env` 或秘密。
3. 前端：以冻结锁文件安装依赖，执行只读 `pnpm run lint`、Vitest、类型检查与生产构建。

当前 Playwright 用例会 mock 公共 Feed API，可在本地按需运行，因此它验证浏览器中的页面行为，不替代本机 MySQL 下的真实发布和鉴权联调。`pnpm run lint:fix` 与 `pnpm run format` 都会写入文件，只应在本地修复后配合 `git diff` 审查，不能作为 CI 门禁。

## 配置

配置加载顺序：先读取 `CONFIG_PATH` 指定的 YAML（默认 `configs/config.dev.yaml`），再用环境变量覆盖，环境变量优先级最高。数据库、Redis、RabbitMQ 的密码、JWT 密钥和运行模式只从环境变量读取，YAML 中即使存在同名字段也会被忽略。`redis` 和 `rabbitmq` 配置已由加载器读取，但当前 API、worker 与 sweeper 不创建中间件客户端；`observe.pprof` 仍是后续功能预留，当前加载器不读取它，现有 `/ready` 只依赖 MySQL。

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
| 草稿保留时长 | `RETENTION_VIDEO_DRAFT_HOURS` | 默认 `24`；未发布草稿到期后进入不可逆清扫 |
| 清扫间隔 | `SWEEPER_INTERVAL_MINUTES` | 默认 `60`；sweeper 执行用户、已发布视频和草稿清扫的间隔分钟数 |
| 草稿清扫租约 | `SWEEPER_DRAFT_PURGE_LEASE_MINUTES` | 默认 `15`；单个草稿的 token 围栏租约，过期后可由其他 sweeper 接管 |
| Redis 主机 | `REDIS_HOST` | 本地默认 `localhost`；Docker 容器由 Compose 覆盖为 `redis` |
| Redis 端口 | `REDIS_PORT` | `6379` |
| Redis DB | `REDIS_DB` | `0`；仅供后续 Redis 客户端选择逻辑库 |
| Redis 密码 | `REDIS_PASSWORD` | 仅从环境变量读取；Compose Redis 将其传给 `requirepass` |
| RabbitMQ 主机 | `RABBITMQ_HOST` | 本地默认 `localhost`；Docker 容器由 Compose 覆盖为 `rabbitmq` |
| RabbitMQ 端口 | `RABBITMQ_PORT` | `5672` |
| RabbitMQ 用户 | `RABBITMQ_DEFAULT_USER` | 默认 `gofeed`；覆盖 YAML 用户名并用于 Compose RabbitMQ 首次初始化 |
| RabbitMQ 密码 | `RABBITMQ_DEFAULT_PASS` | 仅从环境变量读取；用于 Compose RabbitMQ 首次初始化 |

### 本地开发配置

本机 MySQL 的完整初始化、迁移和直接启动流程见上方「本地开发（不使用 Compose）」。`backend/.env` 存放数据库和中间件密码及固定 `JWT_SECRET`，`backend/configs/config.dev.yaml` 存放非敏感配置；两者均由从 `backend` 目录运行的 API 读取。当前 API、worker 与 sweeper 不连接 Redis/RabbitMQ，因此日常直接启动仍只要求 MySQL；需要预先启动中间件服务时，在填好 `backend/.env` 后执行 `docker compose up -d redis rabbitmq`。

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

如需调整 HTTP 端口，修改 `server.port`，并同步 `docker-compose.yml` 中 `8080:8080` 的端口映射。Redis `6379` 与 RabbitMQ `5672` 仅绑定到宿主机回环地址，供本地诊断或后续直接运行的进程使用；Compose 内服务始终通过 `redis:6379` 和 `rabbitmq:5672` 通信。

### 观测与健康检查

`GET /health` 只检查 API 进程存活，`GET /ready` 还会在 2 秒内探测 MySQL，数据库不可用时返回 `503`。Compose 使用 `/ready` 作为 backend 健康检查，frontend 仅在 backend 健康后启动；Redis/RabbitMQ 各自有容器健康检查，但尚未进入 API 就绪条件。每个响应会返回 `X-Request-ID`；客户端可复用该请求头值关联服务端的 `http_request`、`http_request_error` 和 `readiness_check` 日志。sweeper 每项清扫和每轮汇总都会记录事件、结果、耗时、删除数量及失败数量。当前未启用或暴露 pprof；后续实现会参考 `feedsystem` 的隔离模式，以独立 `ServeMux`、仅回环监听、显式开关和独立关闭生命周期提供诊断端点，而不将其注册到 Gin 路由。

## 项目进度

当前主线：发布体验与运维。后端已完成草稿聚合上传、发布、公开列表与详情、我的视频、作者删除、头像上传，并接入会话鉴权；用户主页会统计满足公开数据不变量的已发布视频数量。公开视频查询统一排除软删除、缺少发布时间或任一视频/封面媒体字段的记录，服务层也会对异常实体 fail-closed；视频列表游标已升级为绑定查询范围的 v1 契约，跨范围或旧格式值统一返回 `400`。存储侧将清洗后的物理名与用户指定名分离，并为每次保存附加不可复用对象键；DB 只存相对路径。草稿恢复后端已提供状态查询和主动丢弃：媒体完成情况只通过 `has_video`、`has_cover` 暴露，主动丢弃会将草稿原子转入 `purging`，由 sweeper 用 token 租约逐媒体持久化删除进度，最后硬删除；任何失败都不会把 `purging` 草稿恢复为可写状态。账号和已发布视频删除仍采用软删除 + 7 天宽限期。

后端 CRUD 方法命名已统一为 `Get`、`Get...List`、`Create`、`Update`，涉及硬删除的操作使用 `Remove`（提交 `240f3fa`）。互动后端已完成点赞、评论、关注的模型、鉴权接口和软删除语义（提交 `589ec78`，评论删除命名修正提交 `6425fe3`）；互动前端已接入 Feed、详情和作者主页（提交 `bfe518b`）。上述模块均已独立回归并分开提交，接口明细以根目录 [`API.md`](./API.md) 和后端注册路由为准。

前端已完成基础页面和请求层：短视频 Feed、登录、注册、发布、视频详情、用户列表、用户主页、我的视频、账户设置和头像上传；全局操作提示已覆盖登录注册、发布、视频删除和账户资料操作。Feed 已完成请求取消、分页并发控制、ID 去重、页面失焦暂停播放及桌面/移动端回归；对网络错误和临时 `408`/`429`/`5xx` 还会进行最多两次退避重试，页面离开时会取消等待中的恢复请求，重试耗尽后沿用现有错误与手动重试界面。基础 Feed 全链路回归已补齐。接口明细以根目录 [`API.md`](./API.md) 和后端注册路由为准。

## 开发流程与下一步路线

完整的模块开发流程、当前快照、事实来源、下一步路线、验收清单和已知风险统一维护在 [`DEVELOPMENT.md`](./DEVELOPMENT.md)。后续分析先读该文档，再沿任务对应的路由、迁移、源码和测试增量核对，避免重复扫描整个项目。

模块按“设计契约 → 实现 → 自动化验证 → 页面验收 → 独立提交 → review 暂停”推进；提交范围和验证细则见上述文档。

当前 **Feed 数据不变量与查询边界** 已完成并提交 `c79100c`，**模型绑定的公开视频查询入口** 已完成并提交 `0842820`，**视频游标契约** 已完成并提交 `61fb00e`，**作者批量补全** 已完成并提交 `4e253f9`，**互动统计故障语义** 已完成并提交 `fb867c8`（互动统计查询失败时列表、详情、我的视频与发布响应统一返回 `503`）；各模块真实 MySQL 的后端和竞态回归均已通过，列表作者读取已收敛为一次批量查询。下一步是 **可观测性与查询预算**，为公开列表与详情建立路由级查询预算断言。查询模式稳定后再评估 `gorm.io/gen` 的生成字段，Feed 可靠性阶段完成前不接入 Redis/RabbitMQ 客户端，也不先修改前端交互，后续路线为 RabbitMQ 视频处理闭环，再按指标引入 Redis 定向能力。
