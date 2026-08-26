# GoFeed —— 一个视频feed流系统

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

启动后访问：前端 http://localhost:5173，后端 API http://localhost:8080。

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

```powershell
mysql -u root -p -e "CREATE DATABASE IF NOT EXISTS feedsystem CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;"
```

### 2. 安装并执行数据库迁移

首次安装 [`golang-migrate`](https://github.com/golang-migrate/migrate) CLI：

```powershell
go install -tags 'mysql' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.19.1
```

确保 `$(go env GOPATH)\bin` 已加入 `PATH`。若仅需在当前 PowerShell 会话中使用：

```powershell
$env:Path += ";$(go env GOPATH)\bin"
```

从项目根目录切换到 `backend` 目录后，执行全部未应用的迁移：

```powershell
Set-Location backend
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

```powershell
Set-Location backend
go run ./cmd            # API
# go run ./cmd/sweeper  # 按需启动注销用户和到期视频清扫任务
```

另开一个终端启动前端开发服务器：

```powershell
Set-Location frontend
pnpm.cmd install        # 首次安装依赖
pnpm.cmd dev
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

1. 后端：启动 MySQL 8.0 service，执行 `go vet ./...`、`go build ./...` 和 `go test -race -count=1 ./...`。集成测试会创建临时数据库并应用全部向上迁移。
2. 部署配置：从 `backend/.env.example` 和 `backend/configs/config.example.yaml` 生成 CI 临时的忽略配置文件，再执行 `docker compose config --quiet`。不使用真实 `.env` 或秘密。
3. 前端：以冻结锁文件安装依赖，执行只读 `pnpm run lint`、Vitest、类型检查与生产构建。
4. 浏览器：安装 Chromium，运行 Playwright 的 `chromium` 和 `Mobile Chrome` 项目；失败时上传 HTML 报告、截图、视频和 trace。

当前 Playwright 用例会 mock 公共 Feed API，因此它验证浏览器中的页面行为，不替代本机 MySQL 下的真实发布和鉴权联调。`pnpm run lint:fix` 与 `pnpm run format` 都会写入文件，只应在本地修复后配合 `git diff` 审查，不能作为 CI 门禁。

## 配置

配置加载顺序：先读取 `CONFIG_PATH` 指定的 YAML（默认 `configs/config.dev.yaml`），再用环境变量覆盖，环境变量优先级最高。数据库密码、JWT 密钥和运行模式只从环境变量读取，YAML 中即使存在同名字段也会被忽略。模板 `backend/configs/config.example.yaml` 中的 `redis`、`rabbitmq`、`observe` 为后续功能预留，当前版本尚未读取。

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

### 本地开发配置

本机 MySQL 的完整初始化、迁移和直接启动流程见上方「本地开发（不使用 Compose）」。`backend/.env` 存放数据库密码和固定 `JWT_SECRET`，`backend/configs/config.dev.yaml` 存放非敏感配置；两者均由从 `backend` 目录运行的 API 读取。

### Docker 部署

Docker 同样采用复制修改的方式：

1. 复制 `backend/configs/config.example.yaml` 为 `backend/configs/config.yaml`（已被 git 忽略，不会入库）。
2. 修改 `database.host` 为 `mysql`；数据库密码由 `backend/.env` 注入，YAML 内不保存秘密。
3. compose 将宿主机 `backend/configs` 挂载到容器 `/app/configs`，并设置 `CONFIG_PATH=/app/configs/config.yaml`，容器实际读取的就是这份 `config.yaml`。
4. 在 `backend` 目录创建 `.env`（变量参考 `backend/.env.example`，文件本身不入库）。Compose 通过 `env_file` 将其注入 MySQL、迁移和后端进程；API、worker 与 sweeper 同时将该文件以只读方式挂载到 `/app/.env`，供 `godotenv` 加载。容器内覆盖 `MODE=prod`、`MYSQL_HOST=mysql` 与 `CONFIG_PATH=/app/configs/config.yaml`。示例：

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
```

如需调整 HTTP 端口，修改 `server.port`，并同步 `docker-compose.yml` 中 `8080:8080` 的端口映射。

## 项目进度

当前主线：互动基础。后端已完成草稿聚合上传、发布、公开列表与详情、我的视频、作者删除、头像上传，并接入会话鉴权；用户主页会统计已发布且未软删除的视频数量。存储侧将清洗后的物理名与用户指定名分离，并为每次保存附加不可复用对象键；DB 只存相对路径。未发布草稿到期后由 sweeper 用 token 租约认领，逐媒体持久化删除进度，最后硬删除；任何失败都不会把 `purging` 草稿恢复为可写状态。账号和已发布视频删除仍采用软删除 + 7 天宽限期。

前端已完成基础页面和请求层：短视频 Feed、登录、注册、发布、视频详情、用户列表、用户主页、我的视频、账户设置和头像上传；全局操作提示已覆盖登录注册、发布、视频删除和账户资料操作。Feed 已完成请求取消、分页并发控制、ID 去重、页面失焦暂停播放及桌面/移动端回归；基础 Feed 全链路回归已补齐。接口明细以根目录 [`API.md`](./API.md) 和后端注册路由为准。

## 下一步开发流程

### 工作原则

每个模块都按“设计契约 → 实现 → 自动化验证 → 页面验收 → 独立提交”的顺序推进。一个模块完成并提交后暂停，等待 review，再开始下一个模块；不把多个行为边界混在同一个提交中。

提交前必须确认：

1. 只暂存本模块文件，使用 `git diff --cached --name-only` 检查范围。
2. 使用 `git diff --cached --check` 检查空白和冲突标记。
3. 运行本模块对应的测试；跨模块改动再补跑完整回归。
4. 提交摘要沿用历史格式，例如 `feat(frontend): ...`、`feat(video): ...`，使用简短中文说明。

### 当前执行顺序

| 顺序 | 模块 | 主要工作 | 完成标准 |
| --- | --- | --- | --- |
| 已完成 | Feed 稳定性 | 为首屏和游标分页增加请求取消、并发锁、视频 ID 去重；路由切换和页面失焦时暂停播放；保留加载、空状态、错误重试和到底提示 | `8a05880` 已完成单测和桌面/移动端浏览器回归 |
| 已完成 | 基础 Feed 回归 | 回归认证、发布、Feed、详情、作者、我的视频、账户设置和提示反馈 | `d7392b4` 已补充前后端基础 Feed 回归 |
| 1 | 互动后端基础 | 设计点赞、评论、关注的数据模型、迁移、repository/service/controller/路由及鉴权；同步更新 `API.md` | 每类互动都有成功、重复操作、未登录、资源不存在和软删除场景测试；迁移可独立执行和回滚 |
| 2 | 互动前端页面 | 在后端契约稳定后接入点赞、评论、关注按钮和列表；复用现有 API 错误处理与 toast | 未登录操作能跳转登录；重复点击有明确状态；网络失败可重试；Feed、详情、作者主页状态一致 |
| 3 | 发布体验与运维 | 增加上传进度/取消、媒体预览和更细的错误分类；持续补充日志、健康检查和 CI 回归 | 大文件上传、取消、失败重试可验证；后端、前端、浏览器和 Compose 门禁均通过 |

### 当前模块：互动后端基础

先完成互动后端，不提前混入互动前端页面。建议按以下小步完成，但最终只在该模块整体验收后提交：

1. 明确点赞、评论、关注的资源归属、唯一约束、计数更新策略与软删除语义，新增递增迁移文件，不修改已执行迁移。
2. 实现 repository、service、controller 和 `router.go` 路由，保证重复写操作的响应可预测且鉴权边界明确。
3. 从注册路由和响应结构更新 `API.md`，再由前端以该文档和实际响应为唯一契约。
4. 为成功、重复操作、未登录、资源不存在、作者或视频软删除补充真实 MySQL 集成测试，并执行迁移后的模型对齐检查。

互动后端完成并提交后暂停 review；review 通过后再进入互动前端，不提前混入发布体验或运维改动。

### 基础 Feed 验收清单

本地验收使用“本机 MySQL + 直接启动前后端”方式：先执行未应用的 `migrate ... up`，再从 `backend` 启动 API、从 `frontend` 启动 Vite。验收顺序如下：

1. `GET /health` 返回 `200`，前端 `/api` 与 `/static` 代理可用。
2. 注册新用户、登录、刷新令牌、退出；访问受保护页面时未登录会跳转登录。
3. 上传视频和封面，发布后回到 Feed，能看到成功提示、封面、标题和作者入口。
4. Feed 首屏、继续滚动分页、到底状态、空状态和失败重试均可用；视频按短视频全屏纵向流播放。
5. 打开视频详情、作者主页、用户列表和我的视频；删除自己的视频后列表和提示正确更新。
6. 在账户设置修改用户名、资料、密码和注销账号，错误和成功反馈清晰。
7. 在桌面和移动视口各走一遍，检查导航、视频原生控制、提示和文字没有重叠或溢出。

后端回归从 `backend` 执行 `go test ./...`；前端回归使用 `pnpm.cmd run lint`、`pnpm.cmd run test:unit -- --run`、`pnpm.cmd run build`，并在涉及页面交互时执行 `pnpm.cmd run test:e2e -- --project=chromium --project="Mobile Chrome"`。部署配置改动还必须执行 `docker compose config --quiet`；真实发布、鉴权和媒体流仍按上述本地 MySQL 验收清单联调。
