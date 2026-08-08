# GoFeed —— 一个视频feed流系统

## 快速开始（Docker）

数据库需要手动创建一次，之后启动会自动跑迁移建表：

```bash
docker compose up -d mysql

# 手动建库（只执行一次，后续启动复用）
docker compose exec mysql mysql -uroot -p -e "CREATE DATABASE IF NOT EXISTS feedsystem CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"

docker compose up -d
```

启动顺序：mysql 健康检查通过 → `migrate`（golang-migrate）应用 `backend/db/migrations` 下的迁移建表 → backend/worker 启动。应用本身不负责建库建表。

> 需要 Docker Compose v2.17+（依赖 `service_completed_successfully` 条件）。

## 本地开发

本地不会自动建库，也不会自动迁移，需要先手动创建数据库，再执行迁移：

```sql
CREATE DATABASE IF NOT EXISTS feedsystem CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
```

```bash
docker compose run --rm migrate
# 或本地安装 golang-migrate CLI 后：
# migrate -path backend/db/migrations -database "mysql://root:<密码>@tcp(localhost:3306)/feedsystem?multiStatements=true" up

go run ./cmd
```

连接参数由 `backend/.env` 或环境变量覆盖。
