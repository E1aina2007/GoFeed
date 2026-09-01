# GoFeed 后端开发方案

> 更新日期：2026-09-01
>
> 后端功能基线：`914f343`（公开列表与详情查询预算断言已完成）；当前 checkout 的 `HEAD` 为 `7db27fe`（进度文档提交不计入功能状态），本计划只覆盖后端路线

本文把 [`DEVELOPMENT.md`](./DEVELOPMENT.md) 的「当前路线」细化为可直接开工的模块方案。事实来源仍是 `AGENTS.md`、`DEVELOPMENT.md`、`backend/internal/router/router.go`、`backend/db/migrations` 与真实数据库；本文与它们冲突时，以后者为准。每个模块按「设计契约 → 实现 → 自动化验证 → 页面验收 → 独立提交 → review 暂停」推进，完成一个模块后更新 `DEVELOPMENT.md` 的模块状态、验证记录与下一步，再开始下一个。

## 基线快照

- 阶段一 M1–M6、阶段二 R1/R2 已完成并提交；当前工作树正在收口 R3-A/R3-B，`DEVELOPMENT.md`、`README.md`、`API.md` 已同步后端异步契约
- 迁移最高版本为 `000008`：`000007` 增加 `videos.rejected_at`，`000008` 为历史 rejected 回填时间并增加清扫索引；真实库本次检查为 version 7、dirty 0，尚未应用 000008
- `PublicVideoQuery` 保持 6 个生产调用点：`video_repo.go` 4 处、`social/repo.go` 2 处；公开视频边界集中在 `video` 包
- 列表作者读取已收敛为一次批量查询：`buildListResponse` 截断后收集去重作者 ID，经 `AuthorReader.GetPublicAuthors` 一次读取；详情仍走单条 `GetPublicAuthor`；缺失或已注销作者由适配器补占位资料
- 互动统计读取失败时 fail-closed 返回 `503`（`ErrEngagementUnavailable`，映射在 `video_controller.handleVideoError`）；`Service.engagements` 仍用实体列值预填计数，`videos.likes_count`、`videos.comments_count` 两列依旧没有写入方，删除决策由 R1 迁移处理
- `internal/db` 已内置请求内查询计数（GORM 语句回调）与 200ms 慢查询阈值；`observability.RequestLogger` 在完成日志输出 `db_queries`；公开列表与详情 ≤4 条语句由真实 MySQL e2e 断言（`router/query_budget_test.go`）守护
- social 包的评论、关注、粉丝列表仍使用无版本的 `CommentCursor`/`FollowCursor`（仅 `created_at, id`），未接入 M2 的 v1 游标契约
- 视频状态机与 worker 已落地 `draft → processing → published | rejected`；R3-A/R3-B 在当前工作树补齐状态查询、rejected 主动丢弃和到期清扫
- `cmd/worker/main.go` 只连接数据库等待信号；Compose 的 `worker` 服务未挂载 `backend_uploads`（backend、sweeper 已挂载）
- Redis/RabbitMQ 已有配置、健康检查、Compose 卷与 CI service，Go 侧尚无客户端；API 启动与 `/ready` 不依赖中间件

## 总路线

| 阶段 | 模块 | 产出 | API 变化 | 前端影响 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 一 | M1 查询入口收紧 | `PublicVideoQuery` 模型绑定 | 无 | 无 | 已完成 `0842820` |
| 一 | M2 游标契约 | 游标版本 + 范围绑定 | 400 语义细化 | 无（游标不透明） | 已完成 `61fb00e`（social 列表遗留） |
| 一 | M3 作者批量补全 | 消除列表 N+1 | 无 | 无 | 已完成 `4e253f9` |
| 一 | M4 互动统计故障语义 | 统计失败 503 | 新增 503 | 无（沿用错误重试） | 已完成 `fb867c8` |
| 一 | M5 可观测性与查询预算 | 查询计数与预算断言 | 无 | 无 | 已完成 `69a08c1`、`914f343` |
| 一 | M2 遗留：social 游标版本化 | 评论/关注/粉丝列表接入 v1 契约 | 400 语义细化 | 无（游标不透明） | 已完成（见阶段一记录） |
| 一 | M6 并发与异常测试收尾 | 阶段一回归矩阵 | 无 | 无 | 已完成（见阶段一记录） |
| 二 | R1 状态机与 outbox 迁移 | `000006` + 发布事务改造 | 无（响应形状后移到 R3） | 无 | 已完成 `c2134ac` |
| 二 | R2 relay/worker | 队列拓扑与异步处理闭环 | 无 | 无 | 已完成 `d2294a2` |
| 二 | R3-A API 异步状态 | 202 发布 + 状态查询 | 发布改 202、新端点 | 后续页面跟进 | 本次完成，待 review |
| 二 | R3-B rejected 生命周期 | `000007`/`000008`、主动丢弃、到期清扫 | DELETE 扩展既有契约 | 无 | 本次完成，待 review |
| 三 | C1 限流 | 登录/注册限流 | 新增 429 | 无 | 未开始 |
| 三 | C2 会话校验缓存 | 指标驱动评估 | 待定 | 待定 | 未开始 |
| 三 | C3 用户列表分页 | 用户列表游标化 | `GET /api/user` 加参数 | 可后续跟进 | 未开始 |

依赖关系：M1 → M2 →（M3、M4）→ M5 → social 游标/M6 → R1 → R2 → R3。R3-A/R3-B 后端模块各自独立回归与 review；前端状态展示在后续模块实现。C1 依赖阶段二完成（复用其连接与配置基础）。

---

## 阶段一：Feed 服务端可靠性

### 已完成模块（M1–M5）

设计与验证细节以 `DEVELOPMENT.md` 的「已完成模块」「当前模块验证记录」为准，此处只保留交付结果与执行偏差：

| 模块 | 提交 | 交付结果 |
| --- | --- | --- |
| M1 查询入口收紧 | `0842820` | `PublicVideoQuery` 固定 `Model(&Video{})` 与公开边界，6 个调用点替换且无额外 SQL |
| M2 游标契约 | `61fb00e` | v1 游标绑定 `public`/`author`/`mine` 查询范围，旧格式、版本或范围不符统一 400 |
| M3 作者批量补全 | `4e253f9` | 列表作者读取收敛为一次批量 `IN` 查询，缺失或已注销作者补占位资料；详情仍单条读取 |
| M4 互动统计故障语义 | `fb867c8` | 统计查询失败时列表、详情、我的视频与发布响应统一 503 并整体失败，禁止伪装零计数 |
| M5 可观测性与查询预算 | `69a08c1`、`914f343` | 请求内查询计数与 200ms 慢查询阈值；公开 Feed 首页与详情 ≤4 条语句的真实 MySQL e2e 断言 |

执行偏差记录：

- M4 的 503 映射落在 `video_controller.handleVideoError`（原计划写在 `error/api_error.go`），响应使用固定文案，不回显底层错误；覆盖范围比原计划的「列表与详情」多出「我的视频」与「发布响应」
- M5 按模块内提交边界拆为两个提交：`69a08c1`（计数回调 + 慢查询配置 + 日志字段）与 `914f343`(路由接线与预算断言 e2e)

### M2 遗留：social 列表游标版本化（已完成）

M2 只在 `video` 包两个游标列表落地；按原计划，`social` 包的评论、关注、粉丝列表应「按同一契约紧随一个独立小模块完成，不混入同一提交」。当前三个列表已不再使用无版本的 `CommentCursor`/`FollowCursor`，本模块已补齐：

- `CommentCursor`、`FollowCursor` 增加版本与范围字段，`encodeCursor`/`decodeCursor` 对齐 `video` 包 v1 契约；旧格式、版本或范围不符统一返回 `400`（复用现有 `ErrInvalidCursor` 类路径）
- 范围绑定按列表语义固定：评论列表绑定 `video_id`，关注/粉丝列表绑定目标用户 ID，防止跨列表、跨资源复用游标；解码后的范围与服务层实际参数不一致时按 400 处理
- 错误语义与 M2 决策一致：不区分暴露细节，统一 400，不引入签名
- `API.md` 同步三个列表接口的 400 说明；前端把游标当不透明值，已有失败重试路径无需改动

**边界**：无迁移、无前端改动；不改 `video` 包已落地的游标实现，只对齐契约形状。

**验证**：`go vet ./...`、`go test ./...`、`go test -race -count=1 ./...`；真实 MySQL 路由回归覆盖三个列表的旧格式 400、跨资源复用 400 与正常翻页。

### M6 并发与异常测试收尾

覆盖 `DEVELOPMENT.md` 阶段一第 7 项，作为阶段一收口模块（只加测试与必要的夹具）：

| 场景 | 断言 |
| --- | --- |
| 同一 `published_at` 多条视频 | keyset 排序稳定、翻页不重不漏 |
| 分页期间新增 / 软删除视频 | 顺序可解释，无重复或跳漏 |
| 作者注销 | 占位作者，列表不报错 |
| 六媒体字段残缺 | fail-closed 排除（已有回归，纳入矩阵） |
| 游标篡改 / 跨列表复用 / 旧版本 | 统一 400（video 列表已有回归；social 列表在 M2 遗留落地后纳入） |
| 互动统计查询失败 | 503，零计数不出现在响应 |
| 数据库暂态失败（注入错误） | 错误路径干净，无半组装响应 |
| 重复请求 | GET 幂等，响应一致 |

验证执行 `go test -race -count=1 ./...` 并明确记录真实 MySQL 集成范围。

**阶段一完成标准**（沿用 `DEVELOPMENT.md`）：契约评审通过；单测 / 竞态 / vet 通过；真实 MySQL 迁移与查询回归有证据；`API.md` 已同步（M2 的 400 细化 + social 列表契约 + M4 的 503）。达到前不开始队列或缓存接入。

---

## 阶段二：RabbitMQ 视频处理闭环

### R1 状态机与 outbox 迁移

**迁移 `000006`**（up/down 成对，down 前置条件写进迁移注释）：

1. 新表 `video_outbox_events`：`id`（自增主键）、`event_id`（`CHAR(36)` 唯一键）、`video_id`（索引）、`event_type`、`status`（`pending` / `dispatched`）、`attempt`（int）、`created_at`、`dispatched_at`；索引 `(status, id)` 供 relay 轮询
2. `videos` 增加 `rejected_reason VARCHAR(255) NOT NULL DEFAULT ''`
3. **删除 `videos.likes_count`、`videos.comments_count`**：基线核实两列无任何写入方，读路径已以互动关系表聚合为事实源。决策为「派生字段」，彻底消除双事实源漂移；实体删除对应字段，`Service.engagements` 移除列值预填逻辑

**发布事务改造**（`UpdateDraftPublication`）：同一事务内完成——条件更新 `draft → processing`（要求媒体完整、状态为 `draft`）+ 写入 `published_at` + 插入 outbox 事件。`published_at` 保持在发布请求时刻，公开排序语义与现状一致；`processing` 行因状态条件不满足公开不变量，天然对外不可见。

**状态机**：`draft → processing → published | rejected`，转换全部使用条件更新（CAS）；`rejected` 允许作者主动丢弃进入 `purging`（复用现有清扫机制）。down migration 前必须确认不存在 `processing` / `rejected` 行。

### R2 relay/worker

**连接与拓扑**：新增 `internal/mq`（连接管理、拓扑声明、带 confirm 的发布器）。该包被 relay 与 worker 共同消费，属于横切基础设施，符合现有 `middleware` 层定位；`video` 包不 import `mq`，组合在 worker 完成。

- Exchange `gofeed.events`（topic，durable）；routing key `video.process`
- 队列 `video.process`（durable，`x-dead-letter-exchange=gofeed.dlx`）+ 死信队列 `video.process.dead`
- 发布启用 publisher confirm；API 侧不建任何连接，`/ready` 不新增依赖

**relay**（worker 进程内）：轮询 `video_outbox_events` 中 `pending` 事件（`LIMIT 32`，`FOR UPDATE SKIP LOCKED` 支持多实例），从 `videos` 读取组装消息并发布，confirm 成功后标记 `dispatched`。消息体只含 `event_id`、`video_id`、媒体相对路径、schema 版本，不携带文件内容。

**consumer**：手动 ack；处理动作为本闭环的**最小业务集**——校验共享卷下视频与封面文件存在、大小合法、扩展名与文件头（magic bytes）一致（复用 `video/storage.go` 的校验规则）。通过则 CAS `processing → published`；失败则 `processing → rejected` 并写入 `rejected_reason`。转码、截帧明确不在本阶段。

- 幂等：条件更新 `WHERE id = ? AND status = 'processing'`，`RowsAffected = 0` 视为重复消息直接 ack
- 有限重试：失败且 `attempt < 3` 时按同一 routing key 重发（header 计数）后 ack；达到上限 nack 进死信
- Compose：`worker` 服务补挂载 `backend_uploads`；运行 `docker compose config --quiet` 验证

**必测**：发布事务回滚、relay 崩溃重启、worker 重启、重复消息、重试上限、死信、媒体缺失、`processing → published/rejected` 之外的状态更新被拒绝、消息与数据库不一致。不把「能连接 RabbitMQ」当作闭环完成。

### R3 API 与前端异步状态（后端已完成，待 review）

**建议决策**：发布接口改为异步语义，路径不变。

| 接口 | 变化 |
| --- | --- |
| `POST /api/video/auth/drafts/:id/publish` | 成功返回 `202` + `DraftItem` 形体（`status: "processing"`）；媒体不完整仍 `4xx` |
| `GET /api/video/auth/:id/status`（新增） | 作者本人查询 `processing` / `published` / `rejected`（含 `rejected_at`、`rejected_reason`）；非本人 404 |
| `DELETE /api/video/auth/drafts/:id` | 扩展：允许对 `rejected` 状态调用，转入 `purging` 由 sweeper 清扫 |
| 公开 `GET /api/video/:id` | `processing` 期间维持 404（不变量自然保证），无改动 |

- `GET /mine` 维持只含 `published`；状态轮询走新端点
- sweeper 已扩展：`rejected_at + RETENTION_VIDEO_DRAFT_HOURS` 到期自动转 `purging`，`rejected_at IS NULL` 的旧行由 000008 使用 `updated_at` 回填，避免拒绝件永久占用存储
- 前端状态展示与轮询仍是后续独立页面模块；本次只交付后端契约和清扫生命周期

**R3-B 迁移与兼容**：`000007` 增加可空 `videos.rejected_at`；由于真实库已处于 version 7，不能改写该迁移，`000008` 使用 `updated_at` 回填旧 `rejected` 行并建立 `(status, rejected_at, id)` 索引。`rejected_at IS NULL` 的记录在回填前不会被自动 claim。

**阶段二完成标准**：三个模块均独立回归并有真实 MySQL（与 RabbitMQ 集成）证据；`API.md`、`README.md`、`DEVELOPMENT.md` 同步；Compose 配置校验通过。

---

## 阶段三：Redis 定向能力

### C1 登录 / 注册限流

- 位置：路由中间件（`internal/middleware` 既有层），覆盖 `POST /api/user/register` 与 `POST /api/user/login`
- 算法：固定窗口计数（Lua 脚本 `INCR` + `EXPIRE` 保证原子性）；key `rl:{action}:{ip}`
- 建议阈值：注册 5 次/小时/IP，登录 10 次/分钟/IP；超限返回 `429` + `Retry-After`
- 配置：`RateLimit` 配置段 + `RATE_LIMIT_*` 环境变量，秘密与开关遵循现有配置加载规则
- 降级：Redis 不可用时 fail-open 并记录日志（限流非权威功能，不阻断业务；`/ready` 不受影响）
- 测试：限流器接口 + 内存实现单测；Redis 实现走集成或按可用性明确记录跳过

### C2 会话校验缓存（评估项）

按 `DEVELOPMENT.md`：由命中率与延迟指标驱动，实现前固定 key 命名、TTL、主动失效（登出/改密/注销时删除）与 Redis 故障回退（查 MySQL）。指标不成立则不做。

### C3 用户列表分页

`GET /api/user` 增加 `cursor` / `limit`，按 M2 的游标契约执行（`created_at, id` keyset，版本 + 无范围绑定）；`API.md` 同步后前端跟进。数据量增长前完成即可。

---

## 前端并行边界

- 阶段一 M1–M5 已全部落地，未触及任何前端文件；剩余的 social 游标版本化与 M6 同样只是后端契约细化与测试：游标对前端始终不透明（旧值 400 走既有失败重试路径），503 沿用既有错误界面。**前端不需要等待后端，可直接按既有路线继续页面与交互工作**
- 阶段二 R1、R2 同样无前端影响；**R3 是唯一需要前后端协同的契约点**，顺序是先稳定 `API.md` 再动前端
- C1 的 429、C3 的分页参数按相同原则：后端契约先行，前端随后

## 验证命令

后端（从 `backend` 目录）：

```bash
go vet ./...
go test ./...
go test -race -count=1 ./...
```

涉及迁移、实体或查询时，补充真实 MySQL 的迁移与 schema 对齐测试；无法连接 MySQL 时在完成报告中明确「集成测试跳过」。Compose 改动执行 `docker compose config --quiet`。Windows Go 缓存不可写时使用任务专用 `GOCACHE` 后重跑。

## 明确不做

- 阶段一完成前不接入 Redis/RabbitMQ 业务客户端，不改前端交互
- 当前游标 Feed 不做缓存，除非读压力指标证明收益；Redis 不成为用户、视频、草稿、清扫状态的权威来源；不把 Redis 加入 `/ready` 必需依赖
- 阶段二不搬用草稿清扫租约到视频处理；转码、截帧不入闭环
- `gorm.io/gen` 延后到读取模式稳定后评估；`observe.pprof` 仍是延后事项
- 不修改已应用的迁移；所有 schema 变更走 `000006` 起的递增版本
