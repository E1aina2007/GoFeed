# GoFeed 后端开发方案

> 更新日期：2026-09-13
>
> 后端功能基线：`fb5de22`（MQ 运维观测生产实现）；专项回归补充为 `6157d66`，文档提交不单独推进功能状态

本文把 [`DEVELOPMENT.md`](./DEVELOPMENT.md) 的「当前路线」细化为可直接开工的模块方案。事实来源仍是 `AGENTS.md`、`DEVELOPMENT.md`、`backend/internal/router/router.go`、`backend/db/migrations` 与真实数据库；本文与它们冲突时，以后者为准。每个模块按「设计契约 → 实现 → 自动化验证 → 页面验收 → 独立提交 → review 暂停」推进，完成一个模块后更新 `DEVELOPMENT.md` 的模块状态、验证记录与下一步，再开始下一个。

实现先复用当前 package 内的 controller/service/repository、`PublicVideoQuery`、列表装配、存储、会话和清扫能力；只有现有契约无法表达的新状态才新增最小逻辑。保持 router 作为组合根和既有依赖方向；新增导出函数及不可自明的小辅助函数使用现有简短中文用途注释，新增 Go 测试注释遵循 `AGENTS.md` 的两行中文格式。

## 基线快照

- 阶段一 Feed 核心（M1–M6）和 social 列表游标版本化均已提交：social 实现为 `22174a5`，范围合同回归为 `62313b1`；阶段二 R1/R2/R3-A/R3-B 后端已提交，前端 R3-C 适配由 `5ada6f9` 提交
- 迁移最高版本为 `000009`：在 `000007`/`000008` 的 rejected 生命周期之后，`000009` 为 outbox 增加 publishing 租约、下次尝试时间、最后尝试时间与错误字段，并回填历史 pending 事件；2026-09-12 已在临时真实 MySQL 上完成全量迁移和结构对齐，目标业务库仍需单独升级
- `PublicVideoQuery` 保持 6 个生产调用点：`video_repo.go` 4 处、`social/repo.go` 2 处；公开视频边界集中在 `video` 包
- 列表作者读取已收敛为一次批量查询：`buildListResponse` 截断后收集去重作者 ID，经 `AuthorReader.GetPublicAuthors` 一次读取；详情仍走单条 `GetPublicAuthor`；缺失或已注销作者由适配器补占位资料
- 互动统计读取失败时 fail-closed 返回 `503`（`ErrEngagementUnavailable`，映射在 `video_controller.handleVideoError`）；`Service.engagements` 仅接受互动关系表聚合结果，`videos.likes_count`、`videos.comments_count` 已由 R1 的 `000006` 迁移删除，避免双事实源
- `internal/db` 已内置请求内查询计数（GORM 语句回调）与 200ms 慢查询阈值；`observability.RequestLogger` 在完成日志输出 `db_queries`；公开列表与详情 ≤4 条语句由真实 MySQL e2e 断言（`router/query_budget_test.go`）守护
- social 包的评论、关注、粉丝列表使用已提交的 v1 `CommentCursor`/`FollowCursor`，分别绑定 `comments + video_id`、`followers`/`following + user_id`；旧格式、未知字段、版本或范围不匹配统一返回 `400`
- 视频状态机与 worker 已落地 `draft → processing → published | rejected`；R3-A/R3-B 已在 `f4fafeb` 提交状态查询、rejected 主动丢弃和到期清扫
- `cmd/worker/main.go` 已连接 MySQL 与 RabbitMQ，运行 relay 和 consumer；`7f97382` 增加运行中重连和先等待两个循环退出再关闭 broker 的生命周期；Compose 的 `worker` 已在 `6eaab01` 挂载 `backend_uploads`
- Redis 只有配置和健康检查，Go 侧仍无 Redis 客户端；RabbitMQ 客户端已由 worker 接入，API 启动与 `/ready` 不依赖中间件

## 总路线

| 阶段 | 模块 | 产出 | API 变化 | 前端影响 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 一 | M1 查询入口收紧 | `PublicVideoQuery` 模型绑定 | 无 | 无 | 已完成 `0842820` |
| 一 | M2 游标契约（video） | 游标版本 + 范围绑定 | 400 语义细化 | 无（游标不透明） | 已完成 `61fb00e`；social 扩展状态见下一行 |
| 一 | M3 作者批量补全 | 消除列表 N+1 | 无 | 无 | 已完成 `4e253f9` |
| 一 | M4 互动统计故障语义 | 统计失败 503 | 新增 503 | 无（沿用错误重试） | 已完成 `fb867c8` |
| 一 | M5 可观测性与查询预算 | 查询计数与预算断言 | 无 | 无 | 已完成 `69a08c1`、`914f343` |
| 一 | M2 遗留：social 游标版本化 | 评论/关注/粉丝列表接入 v1 契约 | 400 语义细化 | 无（游标不透明） | 已完成 `22174a5`、`62313b1` |
| 一 | M6 并发与异常测试收尾 | 阶段一回归矩阵 | 无 | 无 | 已完成（见阶段一记录） |
| 二 | R1 状态机与 outbox 迁移 | `000006` + 发布事务改造 | 无（响应形状后移到 R3） | 无 | 已完成 `c2134ac` |
| 二 | R2 relay/worker | 队列拓扑与异步处理闭环 | 无 | 无 | 已提交；正常消费、死信与 1s 重试回流已通过真实依赖回归 |
| 二 | R3-A API 异步状态 | 202 发布 + 状态查询 | 发布改 202、新端点 | 后续页面已在 `5ada6f9` 跟进 | 已提交；后端回归已完成 |
| 二 | R3-B rejected 生命周期 | `000007`/`000008`、主动丢弃、到期清扫 | DELETE 扩展既有契约 | 无 | 已提交；迁移无待执行、后端回归已完成 |
| 二 | R3-C 前端异步状态 | 状态查询、处理中轮询、拒绝反馈 | 无新增后端接口 | 发布页已跟进 | 已提交；前端门禁未纳入本轮 |
| 二 | R4 MQ 可靠性增强 | MQ 规格、运行中重连、outbox 租约、分级重试/DLQ | 无 | 无 | 核心提交 `7f97382`、`ef94396`；B2 运维观测已提交 `fb5de22`、`6157d66` |
| 三 | C1 限流 | 登录/注册限流 | 新增 429 | 无 | 未开始 |
| 三 | C2 会话校验缓存 | 指标驱动评估 | 待定 | 待定 | 未开始 |
| 三 | C3 用户列表分页 | 用户列表游标化 | `GET /api/user` 加参数 | 可后续跟进 | 未开始 |

依赖关系（按实际提交历史修订）：M1 → M2(video) →（M3、M4）→ M5 → M6 → R1 → R2 → R3-A/R3-B → R3-C → R4；social 游标版本化已独立提交，不阻塞 R1/R2。C1 只依赖 Redis 配置与客户端接入，不依赖 RabbitMQ 验收；API 错误复用与 MQ 可靠性核心实现均已提交。

---

## 阶段一：Feed 服务端可靠性

### 已完成模块（M1–M6，含 social 游标）

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

M2 只在 `video` 包两个游标列表落地的历史缺口已由 `22174a5` 补齐，并由 `62313b1` 增加范围合同回归。实现保留 `social.Repo` 依赖边界，不回溯修改已完成的视频游标模块：

- `CommentCursor`、`FollowCursor` 使用版本、列表类型、资源 ID、时间和记录 ID 的 v1 紧凑字段；严格拒绝旧格式、未知字段和不支持版本
- 评论游标绑定 `comments + video_id`；粉丝与关注分别绑定 `followers`/`following + user_id`，防止跨列表、跨资源复用游标
- `API.md` 已同步三个列表接口的示例和 `400` 说明；前端继续把游标当不透明值，无需改动参数语义

**边界**：无迁移、无前端改动；不改 `video` 包已落地的游标实现，只补齐 social 包契约。

**验证**：2026-09-05 的 `go vet ./...`、`go test -count=1 ./...`、`go test -race -count=1 ./...` 均已在真实 MySQL 下通过；路由回归覆盖三个列表正常翻页、旧格式、评论跨视频、关系跨用户和 `followers`/`following` 互换的 `400`。

### M6 并发与异常测试收尾

覆盖 `DEVELOPMENT.md` 阶段一第 7 项，作为阶段一收口模块（只加测试与必要的夹具）：

| 场景 | 断言 |
| --- | --- |
| 同一 `published_at` 多条视频 | keyset 排序稳定、翻页不重不漏 |
| 分页期间新增 / 软删除视频 | 顺序可解释，无重复或跳漏 |
| 作者注销 | 占位作者，列表不报错 |
| 六媒体字段残缺 | fail-closed 排除（已有回归，纳入矩阵） |
| 游标篡改 / 跨列表复用 / 旧版本 | 统一 400（video 与 social 列表均有回归） |
| 互动统计查询失败 | 503，零计数不出现在响应 |
| 数据库暂态失败（注入错误） | 错误路径干净，无半组装响应 |
| 重复请求 | GET 幂等，响应一致 |

验证执行 `go test -race -count=1 ./...` 并明确记录真实 MySQL 集成范围。

**阶段一完成标准**（沿用 `DEVELOPMENT.md`）：契约评审通过；单测 / 竞态 / vet 通过；真实 MySQL 迁移与查询回归有证据；`API.md` 已同步（视频和 social 游标 400 + M4 的 503）。该标准已满足；Redis 新能力按自身 Redis 依赖与验证推进，不以 RabbitMQ 运行时验收为前置。

---

## 阶段二：RabbitMQ 视频处理闭环

### R1 状态机与 outbox 迁移

**迁移 `000006`**（up/down 成对，down 前置条件写进迁移注释）：

1. 新表 `video_outbox_events`：`id`（自增主键）、`event_id`（`CHAR(36)` 唯一键）、`video_id`（索引）、`event_type`、`status`、`attempt`、`created_at`、`dispatched_at`；初始索引 `(status, id)` 供 relay 轮询，后续 `000009` 扩展 publishing 租约字段和 claim 索引
2. `videos` 增加 `rejected_reason VARCHAR(255) NOT NULL DEFAULT ''`
3. **删除 `videos.likes_count`、`videos.comments_count`**：基线核实两列无任何写入方，读路径已以互动关系表聚合为事实源。决策为「派生字段」，彻底消除双事实源漂移；实体删除对应字段，`Service.engagements` 移除列值预填逻辑

**发布事务改造**（`UpdateDraftPublication`）：同一事务内完成——条件更新 `draft → processing`（要求媒体完整、状态为 `draft`）+ 写入 `published_at` + 插入 outbox 事件。`published_at` 保持在发布请求时刻，公开排序语义与现状一致；`processing` 行因状态条件不满足公开不变量，天然对外不可见。

**状态机**：`draft → processing → published | rejected`，转换全部使用条件更新（CAS）；`rejected` 允许作者主动丢弃进入 `purging`（复用现有清扫机制）。down migration 前必须确认不存在 `processing` / `rejected` 行。

### R2 relay/worker

**连接与拓扑**：`internal/mq` 提供事件/消费规格、连接 runtime、拓扑声明和 confirm 发布。`VideoProcessSpec` 是 exchange、routing key、queue、prefetch 与重试策略的唯一事实源；`video` 包不 import `mq`，组合仍在 worker 完成。

- Exchange `gofeed.events`（topic，durable）；routing key `video.process`
- 队列 `video.process`（durable，`x-dead-letter-exchange=gofeed.dlx`）+ `video.process.retry.1s/5s/30s` 三档 TTL 队列 + 死信队列 `video.process.dead`
- 发布启用 publisher confirm；API 侧不建任何连接，`/ready` 不新增依赖

**relay**（worker 进程内）：在短事务中以 `FOR UPDATE SKIP LOCKED` 批量把到期 pending 或租约过期 publishing 事件 claim 为 `publishing`，递增 `attempt` 作为围栏并设置租约；事务外读取视频快照和 confirm 发布，成功后仅由仍持有同一 attempt 的 relay 标记 `dispatched`，失败则释放为 pending 并安排封顶五分钟的退避。消息体只含 `event_id`、`video_id`、媒体相对路径、schema 版本，不携带文件内容。

**consumer**：手动 ack；处理动作为本闭环的**最小业务集**——校验共享卷下视频与封面文件存在、大小合法、扩展名与文件头（magic bytes）一致（复用 `video/storage.go` 的校验规则）。通过则 CAS `processing → published`；失败则 `processing → rejected` 并写入 `rejected_reason`。转码、截帧明确不在本阶段。

- 幂等：条件更新 `WHERE id = ? AND status = 'processing'`，`RowsAffected = 0` 视为重复消息直接 ack
- 有限重试：基础设施失败先把消息发布到下一档 TTL 重试队列，broker confirm 后才 ack 原消息；初次投递后允许三次延迟重试，第三次重试仍实际处理，只有该次也失败才 nack 进死信
- Compose：`worker` 服务补挂载 `backend_uploads`；运行 `docker compose config --quiet` 验证

**必测**：发布事务回滚、relay 崩溃重启、worker 重启、重复消息、重试上限、死信、媒体缺失、`processing → published/rejected` 之外的状态更新被拒绝、消息与数据库不一致。不把「能连接 RabbitMQ」当作闭环完成。

### R3 API 与前端异步状态（代码已提交，待集成收口）

**建议决策**：发布接口改为异步语义，路径不变。

| 接口 | 变化 |
| --- | --- |
| `POST /api/video/auth/drafts/:id/publish` | 成功返回 `202` + `DraftItem` 形体（`status: "processing"`）；媒体不完整仍 `4xx` |
| `GET /api/video/auth/:id/status`（新增） | 作者本人查询 `processing` / `published` / `rejected`（含 `rejected_at`、`rejected_reason`）；非本人 404 |
| `DELETE /api/video/auth/drafts/:id` | 扩展：允许对 `rejected` 状态调用，转入 `purging` 由 sweeper 清扫 |
| 公开 `GET /api/video/:id` | `processing` 期间维持 404（不变量自然保证），无改动 |

- `GET /mine` 维持只含 `published`；状态轮询走新端点
- sweeper 已扩展：`rejected_at + RETENTION_VIDEO_DRAFT_HOURS` 到期自动转 `purging`，`rejected_at IS NULL` 的旧行由 000008 使用 `updated_at` 回填，避免拒绝件永久占用存储
- 前端状态展示与轮询已由 `5ada6f9` 独立提交；后端契约和清扫生命周期由 `f4fafeb` 提交

**R3-B 迁移与兼容**：`000007` 增加可空 `videos.rejected_at`；由于真实库已处于 version 7，不能改写该迁移，`000008` 使用 `updated_at` 回填旧 `rejected` 行并建立 `(status, rejected_at, id)` 索引。`rejected_at IS NULL` 的记录在回填前不会被自动 claim。

**阶段二完成标准**：R1、R2、R3-A、R3-B、前端 R3-C 与 R4 均有独立提交；目标数据库应用到 `000009`；真实 RabbitMQ 故障矩阵和前端门禁证据齐全；`API.md`、`README.md`、`DEVELOPMENT.md` 同步；Compose 配置校验通过。2026-09-12 已在临时真实 MySQL/RabbitMQ 上通过 vet、常规及 race 全仓回归，覆盖结构对齐、租约并发/接管、真实重连/拓扑恢复、正常消费、1s 回流和最终死信边界；目标库升级、5s/30s 计时、进程级崩溃窗口与前端门禁仍待完成。

### R4 MQ 可靠性增强（核心实现已提交）

`7f97382` 已完成固定 MQ 规格、运行中 connection 重连、outbox claim 租约/退避与 consumer ACK/分级重试/DLQ，`ef94396` 增加真实 broker 断线重连和拓扑恢复回归。`Runtime.Close` 是终止操作，不被测试或业务代码用作“模拟断线”；意外断线才触发下一次调用重建连接和拓扑。MySQL outbox 仍是事件派发状态的事实源，RabbitMQ 只承载至少一次投递。B2 生产实现 `fb5de22` 增加 30 秒周期快照、DLQ 深度读取以及发布失败、租约接管、重连和死信事件日志，专项回归 `6157d66` 覆盖快照、年龄、事件字段、错误传播和生命周期。全仓常规与 race 回归及隔离 MySQL 用例通过；真实 RabbitMQ QueueInspect/联合快照本轮因没有确认测试专用 broker 而跳过。剩余生产闭环还包括目标库升级、5s/30s 延迟计时和整进程故障窗口；监控平台与告警规则不在 B2 范围。

### API 错误处理复用（已实现，2026-09-09）

`API_ERROR_REUSE_PLAN.md` 的公共契约与 video/user/social 迁移已一次实现：`internal/error` 提供公共类别、描述与统一写入，三个 controller 改为模块级规则表；保持现有 HTTP 状态、文案和 `{"error":"..."}` 响应形状，worker/sweeper 仍不依赖 HTTP 映射。旧 `ParseStatusCode` 已删除。按用户要求本轮跳过测试环节，仅执行构建与既有单测回归。

---

## 阶段三：Redis 定向能力

### C1 登录 / 注册限流

- 位置：路由中间件（`internal/middleware` 既有层），覆盖 `POST /api/user/register` 与 `POST /api/user/login`
- 算法：固定窗口计数（Lua 脚本 `INCR` + `EXPIRE` 保证原子性）；key `rl:{action}:{ip}`
- 建议阈值：注册 5 次/小时/IP，登录 10 次/分钟/IP；超限返回 `429` + `Retry-After`
- 配置：`RateLimit` 配置段 + `RATE_LIMIT_*` 环境变量，秘密与开关遵循现有配置加载规则
- 降级：Redis 不可用时 fail-open 并记录日志（限流非权威功能，不阻断业务；`/ready` 不受影响）
- 测试：限流器接口 + 内存实现单测；Redis 实现走集成或按可用性明确记录跳过

**设计校正**：Redis 客户端属于可由 C1/C2 复用的基础能力，应放在 `internal/redis` 的窄接口后，而非 `internal/middleware/redis`。限流 Lua 必须在一次调用中返回计数和剩余 TTL，`Retry-After` 取向上取整后的正秒数；Redis 故障只 fail-open，不改变 `/ready`。

### C2 会话校验缓存（评估项）

按 `DEVELOPMENT.md`：由命中率与延迟指标驱动，实现前固定 key 命名、TTL、主动失效（登出/改密/注销时删除）与 Redis 故障回退（查 MySQL）。缓存只记录活动会话校验所需的会话 ID、用户 ID 和有效期，TTL 取访问令牌剩余时间、会话有效期与上限三者最小值；指标不成立则不做。按用户撤销必须在原 MySQL 事务内收集实际撤销的 session ID、提交后逐个失效，不能 Redis `SCAN`；上线前还要验证登录发会话与改密/注销的并发契约，避免撤销后新建的竞争会话被误称已失效。

### C3 用户列表分页（与 Redis 无关）

`users` 表没有 `created_at`，因此 `GET /api/user` 使用 `id ASC` 单列 keyset；游标必须带 `v: 1` 与固定列表类型 `k: "users"`，严格拒绝跨列表或未知字段。该接口当前前端一次读取全部用户，后端改为默认分页时必须与前端“加载更多”交互、单测和页面验收同一模块发布，不能把旧页面静默截断为第一页。

---

## 前端并行边界

- 阶段一 M1–M6（含 social 游标版本化）已落地；游标对前端保持不透明，旧值 400 走既有失败路径
- 阶段二 R1、R2 无前端影响；R3 后端契约已由 `f4fafeb` 提交，前端状态轮询已由 `5ada6f9` 提交，后续只需补记前端门禁和真实联调结果
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

- 不把 Redis 接入 `/ready` 必需依赖；RabbitMQ 仅由 worker 参与视频处理，API 仍不建立 MQ 连接
- 当前游标 Feed 不做缓存，除非读压力指标证明收益；Redis 不成为用户、视频、草稿、清扫状态的权威来源；不把 Redis 加入 `/ready` 必需依赖
- outbox 与草稿清扫各自维护状态、租约和围栏语义，只复用通用时长/UTF-8 截断辅助；转码、截帧不入闭环
- `gorm.io/gen` 延后到读取模式稳定后评估；`observe.pprof` 仍是延后事项
- 不修改已应用的迁移；每个新 schema 模块在开工时从迁移目录和目标库状态分配下一个空闲递增版本，不在可选步骤中预占 `000009` 之后的文件名
