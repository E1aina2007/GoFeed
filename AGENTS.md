# GoFeed Agent 规范

## 适用范围与优先级

- 本文件适用于仓库根目录及其 `backend`、`frontend`、Compose 与文档改动
- 先遵循用户当前明确指令，再遵循本文件，最后遵循现有代码和文档约定
- 历史提交、旧计划和会话记录只能作为线索；开始工作前必须检查当前 `git status`、相关源码、迁移和 README
- API 的事实来源是 `backend/internal/router/router.go` 及对应 handler；`API.md` 必须从当前注册路由和响应契约推导
- 数据库结构的事实来源是 `backend/db/migrations` 与真实数据库元数据，不以旧表结构或口头假设为准

## 开始工作

1. 先运行 `git status --short`，识别并保留用户已有改动
2. 读取任务相关的 README、路由、迁移、测试和配置；不要因任务无关文件存在改动而还原或格式化它们
3. 明确本次行为边界：后端、前端、迁移、文档、部署配置应按功能拆分，避免顺带重构
4. 涉及接口时，从 `router.go` 沿 controller、service、repository、实体、前端 API 和页面追踪完整契约
5. 涉及数据库时，先检查 `schema_migrations` 和实际列、索引及数据状态，再决定迁移、恢复或回滚方式

## 交付边界

- 每个模块应当能够独立回归：设计契约、实现、自动化验证、必要的页面验收和提交属于同一模块
- 用户要求分模块 review 时，完成并验证一个模块后停止，等待 review；不要提前开始下一个功能
- 不修改用户未授权的前端、后端、迁移、配置或文档
- 不以临时兼容逻辑、模拟数据或客户端状态替代服务端事实来源
- 避免为单一小适配器新增包；当依赖方向允许时，适配器应位于其所属业务包中

## 本地开发与配置

- 日常开发使用本机 MySQL 加直接启动 Go 与 Vue，不使用 Compose 作为默认开发方式
- 本地启动前，复制并配置 `backend/.env` 与 `backend/configs/config.dev.yaml`；秘密只能保存在被忽略的 `backend/.env`
- 后端从 `backend` 目录运行：`go run ./cmd`
- 按需启动清扫器：`go run ./cmd/sweeper`
- 前端从 `frontend` 目录运行：`pnpm.cmd dev`；Vite 会将 `/api` 和 `/static` 代理至 `http://localhost:8080`
- API 启动不会创建数据库或自动迁移。创建业务库和应用迁移必须显式执行
- 普通 Go/Vue 修改不需要 Docker 重建；Compose 用于部署链路或容器配置验证
- 不手动启动长期运行的预览服务用于代码审查，除非用户明确要求；自动化测试临时启动的服务应在结束后退出

## 配置与秘密

- `Config.Dev`、数据库密码与 JWT 密钥由环境变量控制；环境变量优先于 YAML
- 不在仓库根目录创建或提交 `.env`，不在 YAML、README、测试输出或提交信息中暴露真实密码、令牌或密钥
- Compose 使用 `backend/.env` 作为 `env_file`；需要供 Go 进程读取时，挂载为 `/app/.env:ro`
- `env_file` 只注入环境变量，不创建容器内文件；不要把 `.env not found` 日志单独当作容器故障
- 修改 Compose 后必须运行 `docker compose config --quiet`

## 数据库与迁移

- 从 `backend` 执行迁移时使用相对路径 `-path ./db/migrations`；从仓库根目录执行时使用 `-path ./backend/db/migrations`
- 迁移 DSN 中的密码必须使用占位符或 URL 编码后的值，不能写入文档或源码
- 不修改已经应用的迁移；新增递增版本的 `.up.sql` 和 `.down.sql`
- 迁移需要同时覆盖模型、repository、响应契约和真实 MySQL 集成测试的结构对齐
- 回滚草稿清扫相关迁移属于维护操作：停止 API 与全部 sweeper，确认不存在不兼容的 `draft` 或 `purging` 行后再执行 down migration
- 不通过故意失败的 down SQL 阻止回滚；`golang-migrate` 会将失败迁移标记为 dirty
- GORM 用于 CRUD、事务、映射和软删除；DDL、迁移、`TRUNCATE`、`information_schema` 与需要硬删除的维护操作使用参数化 raw SQL

## 后端实现

- `router` 是 HTTP 组合根：路由注册、中间件、静态文件、健康检查和依赖装配放在这里
- 用户、视频、鉴权、清扫等业务保持既有 package 方向，避免循环依赖
- 媒体持久化存储相对路径；用户展示文件名应使用 `*_original_name`，不要泄露或依赖物理存储名
- 草稿、已发布视频和软删除的状态语义不可混用。清扫、租约和媒体删除改动必须覆盖并发、失败重试和状态不可逆性
- 用户资料默认支持本地头像上传，同时保留现有外部 `avatar_url` 兼容性；不要因本地存储而拒绝已有 OSS URL 契约

## 前端实现

- 保持 Vue/Vite、现有路由、请求层、toast 与样式组织方式；不要无故引入新状态管理或 UI 框架
- 公开 Feed 是匿名读取的短视频流；分页、可见性和播放状态由现有 `usePublishedFeed` 与 `FeedView.vue` 约束
- Feed 请求必须防止失效首屏覆盖、并发分页和视频 ID 重复；路由离开或页面隐藏时暂停播放
- 前后端接口变更先稳定后端契约与 `API.md`，再实现前端调用和交互状态
- 浏览器用例当前可 mock 公共 Feed API；它们验证页面行为，不替代本机 MySQL 下的真实上传、发布和鉴权联调
- 新增 Vitest mock 时遵循 OXLint 规则，为 `vi.fn` 使用真实函数签名的泛型，例如 `vi.fn<typeof fn>()`

## 测试与验证

### 后端

- 常规验证从 `backend` 运行 `go vet ./...`、`go test ./...`；改动共享行为时补充 `go test -race -count=1 ./...`
- Windows 默认 Go 缓存访问失败时，设置当前任务专用且可写的缓存，例如 `$env:GOCACHE = (Join-Path $PWD '.run/gocache')`，再重跑相同范围
- 真实 MySQL 集成测试未配置数据库时会跳过；报告时必须明确“跳过”，不能称为通过的集成回归
- 新迁移或实体字段改动应执行真实 MySQL 的模型与迁移对齐测试
- 修改或新增 Go 测试注释时，使用两行中文格式，且不使用中文句号：

```go
// 测试目标：说明要验证的行为
// 预期效果：说明可观察结果
```

### 前端

- 只读检查使用 `pnpm.cmd run lint`，不要在 CI 或验证命令中使用会写文件的 `lint:fix` 或 `format`
- 常规回归：`pnpm.cmd run lint`、`pnpm.cmd run test:unit -- --run`、`pnpm.cmd run build`
- 涉及交互或视口行为时，增加 `pnpm.cmd run test:e2e -- --project=chromium --project="Mobile Chrome"`
- `pnpm.cmd` 无诊断卡住时，优先使用项目本地 Node 可执行入口运行 Vitest、vue-tsc、ESLint、OXLint、Vite 或 Playwright
- 前端 CI 必须保持冻结锁文件安装、只读 lint、单测、构建和浏览器回归；失败产物应保留报告、截图、视频与 trace

### 部署与文档

- Compose 改动至少验证 `docker compose config --quiet`
- API 变更同步更新根目录 `API.md`，并以路由测试检查注册覆盖
- 本地开发、迁移、部署或下一阶段流程变更同步更新 `README.md`
- 完成前运行 `git diff --check`；只声称实际运行且通过的验证，说明未运行或跳过的范围

## Git 与提交

- 不执行 `git reset --hard`、`git checkout --` 或宽范围删除来清理工作树
- 保留用户已有改动。暂存时明确列出本模块路径，不使用无差别的 `git add .`
- 提交前必须运行：

```powershell
git diff --cached --name-only
git diff --cached --check
```

- 有无关暂存内容时，使用 `git commit --only -m "<message>" -- <本模块路径>`
- 提交信息遵循现有历史：`feat:`、`fix:`、`test:`、`ci:` 等英文前缀加简短中文摘要
- Windows 无法创建 `.git/index.lock` 时，申请必要权限后重试同一精确范围；不要因此扩大暂存或提交范围
- 除非用户明确要求提交，完成一个可回归模块后保留改动并等待 review

## 完成报告

- 说明实现的模块边界、改动文件、实际执行的验证及其结果
- 如测试依赖、MySQL、浏览器或 Docker 不可用，说明原因和未覆盖边界，不以静态检查代替运行结果
- 提交后报告提交哈希、提交范围和最终工作树状态
