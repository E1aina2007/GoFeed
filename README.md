# GoFeed —— 一个视频feed流系统

## 快速开始（Docker）

首次使用需要准备两个文件：

1. 在项目根目录创建 `.env`（变量示例见 `backend/.env.example`，Docker 下使用「Docker 部署」小节的取值），设置 `MYSQL_ROOT_PASSWORD`、`MYSQL_DATABASE`，并建议设置固定的 `JWT_SECRET`（不设置时后端每次重启都会生成随机密钥，登录态全部失效）。
2. 复制 `backend/configs/config.example.yaml` 为 `backend/configs/config.yaml`，把 `database.host` 改为 `mysql`、`database.password` 改为与 `.env` 中 `MYSQL_ROOT_PASSWORD` 一致。

数据库需要手动创建一次，之后启动会自动跑迁移建表：

```bash
docker compose up -d mysql

# 手动建库（只执行一次，后续启动复用）
docker compose exec mysql mysql -uroot -p -e "CREATE DATABASE IF NOT EXISTS feedsystem CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"

docker compose up -d
```

启动顺序：mysql 健康检查通过 → `migrate`（golang-migrate）应用 `backend/db/migrations` 下的迁移建表 → backend/worker/sweeper 启动。应用本身不负责建库建表。

> 需要 Docker Compose v2.17+（依赖 `service_completed_successfully` 条件）。

启动后访问：前端 http://localhost:5173，后端 API http://localhost:8080。

## 本地开发

本地不会自动建库，也不会自动迁移，需要先手动创建数据库，再执行迁移：

```sql
CREATE DATABASE IF NOT EXISTS feedsystem CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
```

```bash
docker compose run --rm migrate
# 或本地安装 golang-migrate CLI 后：
# migrate -path backend/db/migrations -database "mysql://root:<密码>@tcp(localhost:3306)/feedsystem?multiStatements=true" up

go run ./cmd            # API
go run ./cmd/sweeper    # 注销用户和到期软删视频定时清扫；worker 暂为消息队列占位，无需手动启动
```

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
pnpm lint           # 代码检查
pnpm test:unit      # 单元测试
pnpm build          # 类型检查 + 构建
pnpm preview        # 本地预览构建产物
```

安装依赖：`pnpm add <包名>`；开发依赖：`pnpm add -D <包名>`。

## 配置

配置加载顺序：先读取 `CONFIG_PATH` 指定的 YAML（默认 `configs/config.dev.yaml`），再用环境变量覆盖，环境变量优先级最高。模板 `backend/configs/config.example.yaml` 中的 `redis`、`rabbitmq`、`observe` 为后续功能预留，当前版本尚未读取。

当前生效的配置项：

| 配置项 | 环境变量 | 默认值 / 说明 |
| --- | --- | --- |
| 运行模式 | `MODE` | 本地默认 `dev`；Docker 由 compose 注入，缺省 `prod`（关闭 Gin 调试日志） |
| 配置文件路径 | `CONFIG_PATH` | 本地默认 `configs/config.dev.yaml`；Docker 由 compose 设为 `/app/configs/config.yaml` |
| HTTP 端口 | `SERVER_PORT` | `8080` |
| MySQL 主机 | `MYSQL_HOST` | 本地默认 `localhost`；Docker 下在 `config.yaml` 中改为 `mysql`，也可用根目录 `.env` 覆盖 |
| MySQL 端口 | `MYSQL_PORT` | `3306` |
| MySQL 用户 | `MYSQL_USER` | `root` |
| MySQL 密码 | `MYSQL_ROOT_PASSWORD` | 覆盖 `database.password`，优先于 `MYSQL_PASSWORD`；Docker 下默认读取 `config.yaml`，也可用根目录 `.env` 覆盖（两边保持一致） |
| MySQL 库名 | `MYSQL_DATABASE` | 本地默认 `feedsystem`；Docker 下默认读取 `config.yaml`，也可用根目录 `.env` 覆盖（两边保持一致） |
| JWT 密钥 | `JWT_SECRET` | 本地在 `backend/.env`、Docker 在根目录 `.env` 设置；不设置时每次启动随机生成，重启后所有 token 失效 |
| 注销保留天数 | `RETENTION_USER_DELETED_DAYS` | 默认 `7`；注销账号软删除后经过该天数由 sweeper 硬删除 |
| 视频删除保留天数 | `RETENTION_VIDEO_DELETED_DAYS` | 默认 `7`；视频软删除后经过该天数由 sweeper 删除视频/封面文件并硬删除记录 |
| 清扫间隔 | `SWEEPER_INTERVAL_MINUTES` | 默认 `60`；sweeper 执行用户和视频清扫的间隔分钟数 |

### 本地开发

1. 复制 `backend/configs/config.example.yaml` 为 `backend/configs/config.dev.yaml`，把 `database.password` 改成你的 MySQL 密码；或保留默认路径，直接在 `backend/.env` 中用 `MYSQL_*` 覆盖（参考 `backend/.env.example`，环境变量优先）。
2. 在 `backend/.env` 中设置固定 `JWT_SECRET`，避免重启后登录态全部失效。
3. 在 `backend` 目录执行 `go run ./cmd` 启动。

### Docker 部署

Docker 同样采用复制修改的方式：

1. 复制 `backend/configs/config.example.yaml` 为 `backend/configs/config.yaml`（已被 git 忽略，不会入库）。
2. 修改 `database.host` 为 `mysql`，`database.password` 为你的 MySQL 密码（与根目录 `.env` 的 `MYSQL_ROOT_PASSWORD` 一致）。
3. compose 将宿主机 `backend/configs` 挂载到容器 `/app/configs`，并设置 `CONFIG_PATH=/app/configs/config.yaml`，容器实际读取的就是这份 `config.yaml`。
4. 在项目根目录创建 `.env`（变量参考 `backend/.env.example`，文件本身不入库），compose 会把 `MODE`、`SERVER_PORT`、`JWT_SECRET`、`MYSQL_*` 和保留期/清扫参数全部注入 backend / worker / sweeper 容器，可覆盖 `config.yaml` 中的对应字段（环境变量优先）。示例：

```env
MODE=prod
SERVER_PORT=8080
JWT_SECRET=replace-with-a-32-characters-random-secret
MYSQL_HOST=mysql
MYSQL_PORT=3306
MYSQL_USER=root
MYSQL_ROOT_PASSWORD=your-mysql-password
MYSQL_DATABASE=feedsystem
RETENTION_USER_DELETED_DAYS=7
RETENTION_VIDEO_DELETED_DAYS=7
SWEEPER_INTERVAL_MINUTES=60
```

如需调整 HTTP 端口，修改 `server.port`，并同步 `docker-compose.yml` 中 `8080:8080` 的端口映射。

## 项目进度

当前主线：视频内容模块。后端已完成视频/封面上传、发布、公开列表与详情、我的视频、作者删除，并接入会话鉴权；存储侧已落地物理文件名 4 步清洗、实际存储名与用户指定名分离、DB 只存相对路径（迁移 `000002_video_file_names`）。验收测试已补齐两层：仓储集成测试与 httptest 端到端（均跑真实 MySQL），并顺带修复了会话模型与 `auth_sessions` 表的映射 bug、新增模型与迁移的 schema 对齐防护。账号和视频删除均采用软删除 + 7 天宽限期：sweeper 到期硬删除用户；到期视频会同时清除视频/封面文件和数据库记录。CI 已为后端测试引入 MySQL 8.0 service；前端仍是脚手架，尚无业务页面。

下一步优先级：

1. 用户主页视频数统计（`GET /api/user/:id/profile` 的 `video_count` 落地）。
2. 基础 Feed 与前端最小闭环（登录/注册、视频流、上传与发布、我的视频）；接口契约已被端到端测试锁定，可直接启动前端。
3. 互动模块（点赞、评论、关注）及后续演进。
