# GoFeed 开发流程与路线

> 更新日期：2026-09-12
>
> 功能快照基线：`7f97382`（`feat: 完善视频处理消息可靠性`）；文档提交不单独推进功能状态

本文是 GoFeed 后续开发分析和实施的主入口。它把 README 中的开发流程、当前进度、验收规则与下一阶段建议集中在一起，减少每次任务都从头扫描项目的需要。本文记录的是当前 checkout 的快照；开始新任务时仍需先检查工作树，并核对任务涉及的事实来源。
以下模块状态只统计已提交的提交历史；当前工作树中的未提交改动不计入完成判定。

## 使用方式

后续分析按以下顺序进行：

1. 先阅读本文的“当前快照”“当前路线”和“事实来源”
2. 执行 `git status --short --branch`，确认基线、用户未提交改动和正在 review 的模块
3. 只沿任务对应的事实来源追踪契约、实现和测试，不默认遍历整个仓库
4. 完成一个模块后，把提交哈希、验证结果、未覆盖边界和下一步更新到本文

本文不能替代仓库根目录的 [`AGENTS.md`](./AGENTS.md)；安全、配置、迁移和提交规则以它为准。接口明细仍以 [`API.md`](./API.md) 和当前注册路由为准。

## 提交历史校准

截至 `2026-09-12`，最近一段线性提交已改变此前的“待开发”边界：

功能状态按实际代码、迁移或测试变更判定；`docs`、注释整理和依赖声明提交只更新快照，不单独推进功能模块。

- `c2134ac`、`8342fa2` 完成视频 `draft → processing` 状态机、迁移和事务内 outbox 写入
- `b0321ef`、`d2294a2`、`6eaab01` 完成 RabbitMQ 拓扑、relay/worker 消费闭环和 Compose 共享媒体卷；`1678360` 只修正集成测试信道清理
- `5ada6f9` 已提交前端异步发布状态适配（调用状态查询 API、轮询、处理中/拒绝状态和页面单测）
- `f4fafeb` 已提交 R3 后端状态查询、拒绝生命周期、`000007`/`000008` 迁移及契约文档
- `5daf4e7` 仅清理 worker 注释，不改变功能状态；它与 `1678360` 都不应被当作新的业务模块
- `3053536` 仅将 `google/uuid` 与 `amqp091-go` 的依赖类型改为 direct，不改变业务行为
- `1298b2f` 将 `social.Service` 的依赖接口从 `Store` 重命名为 `Repo` 并清理/调整注释，不改变路由、游标格式、状态机或消息处理行为
- `220aff0` 只补充前端恢复未完成发布记录，不改变后端路由或 social 游标契约
- `22174a5` 完成 social 评论、关注和粉丝列表的 v1 游标实现；`62313b1` 补齐范围合同路由回归。两者均已提交，R1/R2 无需因此回溯重排
- `7f97382` 完成 MQ 单一契约、运行中重连、outbox claim 租约与退避、分级延迟重试队列及 worker 优雅退出；新增 `000009` 迁移

### 按任务定位文件

| 任务 | 首先检查 | 然后检查 |
| --- | --- | --- |
| HTTP 接口或响应 | `backend/internal/router/router.go` | 对应 controller、service、repository、entity、`API.md` 和路由测试 |
| 数据库或状态机 | `backend/db/migrations` | 实体、repository、`schema_alignment_test.go`、真实 MySQL 的 `schema_migrations` 与 `information_schema` |
| Feed 读取 | `backend/internal/video` | `frontend/src/features/video`、`FeedView.vue` 及对应单测/E2E |
| 互动数据 | `backend/internal/social` | video 消费方接口、迁移、controller 和前端互动组件 |
| 运行时或部署 | `backend/internal/config`、配置示例 | `docker-compose.yml`、Dockerfile、CI workflow 和健康检查 |
| 清扫或媒体 | `backend/cmd/sweeper`、`backend/internal/sweeper`、`video/storage.go` | 草稿迁移、进程生命周期、共享卷和失败重试测试 |

## 当前快照

### 模块状态

| 模块 | 状态 | 代表提交 | 当前边界 |
| --- | --- | --- | --- |
| Feed 稳定性与基础回归 | 已完成 | `8a05880`、`d7392b4` | 请求取消、分页并发控制、ID 去重、播放暂停和基础全链路回归 |
| Feed 暂态故障自愈 | 已完成 | `4a54301` | 网络及 `408`/`429`/`5xx` 最多两次退避重试，不改变游标和去重语义 |
| 后端 CRUD 命名 | 已完成 | `240f3fa` | 硬删除操作统一使用 `Remove` |
| 互动后端 | 已完成 | `589ec78`、`6425fe3` | 点赞、评论、关注及评论软删除，接口已同步 `API.md` |
| 互动前端 | 已完成 | `bfe518b` | Feed、详情和作者主页的互动控件与列表 |
| 草稿恢复与主动丢弃 | 已完成 | `7afa144`、`2028f95` | 服务端状态是事实来源，`purging` 草稿由 sweeper 不可逆清扫 |
| 上传取消与媒体预览 | 已完成 | `655e878`、`ccbd659` | 可中止上传、本地预览生命周期和错误分类 |
| 运维可观测性 | 已完成 | `1b702cd` | 请求 ID、请求完成日志、MySQL 就绪检查和 sweeper 轮次汇总 |
| 中间件接入准备 | 已完成 | `487d700`、`b0321ef` | Redis 仍只有配置；RabbitMQ 已由 worker 建立连接并参与视频处理闭环 |
| Feed 数据不变量与查询边界 | 已完成 | `c79100c` | 公开详情、Feed、我的视频、作者视频计数和互动视频校验统一排除残缺记录 |
| 模型绑定的公开视频查询入口 | 已完成 | `0842820` | `PublicVideoQuery` 固定 `Video` 模型并统一复用公开边界，六个查询点无额外 SQL |
| 视频游标契约 | 已完成 | `61fb00e` | v1 游标绑定 `public`、`author`、`mine` 查询范围，旧格式和范围不匹配统一返回 400 |
| 作者批量补全 | 已完成 | `4e253f9` | 列表作者读取收敛为一次批量查询，详情仍走单条读取，缺失或已注销作者保持占位资料 |
| 互动统计故障语义 | 已完成 | `fb867c8` | 互动统计查询失败时列表、详情、我的视频与发布响应统一返回 503 并整体失败 |
| 可观测性与查询预算 | 已完成 | `69a08c1` | 请求内查询计数随完成日志输出 `db_queries`，公开列表与详情 e2e 断言 ≤4 条语句 |
| 并发与异常测试收尾 | 已完成 | `d185645` | 同刻排序、分页变更、注销作者占位、统计失败 503、注入错误与 GET 幂等矩阵落地 |
| R1 状态机与 outbox 迁移 | 已完成 | `c2134ac` | 发布事务 draft→processing 并原子写入 outbox 事件；冗余计数值列已删除 |
| R2 relay/worker | 已提交，成功消费路径已观察 | `b0321ef`、`d2294a2`、`6eaab01`、`1678360` | worker 已实际启动并完成 `video_id=10` 消费；真实 broker 故障、重启与死信矩阵本轮未执行 |
| R3-A API 异步状态 | 已提交，后端回归完成 | `f4fafeb` | 发布返回 202 + DraftItem，新增作者状态查询，processing/rejected 结果字段固定 |
| R3-B rejected 生命周期 | 已提交，迁移与后端回归完成 | `f4fafeb` | rejected 可主动丢弃；按 rejected_at 自动进入 purging；000008 无待执行迁移 |
| R3-C 前端异步状态 | 已提交，前端门禁未纳入本轮 | `5ada6f9` | 状态查询 API、处理中轮询、拒绝提示和发布页面单测已落地 |
| M2 遗留：social 列表游标版本化 | 已完成 | `22174a5`、`62313b1` | 评论、关注、粉丝 v1 游标绑定列表类型与视频/目标用户范围；旧格式和跨范围值返回 400 |
| MQ 可靠性增强 | 已提交，待真实依赖验收 | `7f97382` | MQ 规格为唯一事实源；runtime 自动重连；outbox 使用 publishing 租约、围栏与退避；consumer 使用 1s/5s/30s 分级重试队列 |

### 已确认的系统边界

- MySQL 是用户、视频、草稿、互动和清扫状态的唯一业务事实源
- 公开视频必须同时满足 `status = 'published'`、未软删除、`published_at IS NOT NULL` 及视频/封面六个媒体字段非空；视频与互动仓储查询、公开实体映射和路由回归已共同执行该边界
- 当前公开视频条件使用 GORM `clause` 参数化值，`PublicVideoQuery` 已将 `Video` 模型绑定与公开边界组合为单一入口，不增加额外 SQL 查询
- 公开视频列表使用 `(published_at DESC, id DESC)` keyset 游标，并通过 `limit+1` 判断是否有下一页
- 视频列表游标使用版本 `1`，并绑定查询范围：全局列表为 `public`，指定作者列表为 `author + author_id`，我的视频为 `mine + viewer_id`；旧格式、未知字段、版本或范围不匹配统一返回 `400`，不引入签名
- social 评论、关注、粉丝列表使用已提交的 v1 `CommentCursor`/`FollowCursor`：`comments` 绑定视频 ID，`followers`/`following` 分别绑定目标用户 ID；旧格式、未知字段、版本或范围不匹配统一返回 `400`
- 列表作者读取已收敛为一次批量查询：`buildListResponse` 在截断后收集去重作者 ID，经 `AuthorReader.GetPublicAuthors` 一次读取；详情仍走单条 `GetPublicAuthor`
- `user.Repository.GetByIDs` 一次参数化 `IN` 查询只投影 `id`、`username`、`avatar_url`；GORM 默认软删除作用域排除已注销用户，缺失标识由适配器补占位资料
- 互动统计读取已支持按视频 ID 批量查询；查询失败时读路径 fail-closed 返回 `503`（`ErrEngagementUnavailable`），零计数只允许来自真实聚合结果；`videos.likes_count`、`videos.comments_count` 两列已在 `000006` 删除，互动关系表聚合是计数的唯一事实源
- 请求内数据库查询由 `internal/db` 的 GORM 语句回调计数，`observability.RequestLogger` 在完成日志输出 `db_queries`；公开列表与详情的预算为 4 条语句，由真实 MySQL e2e 断言守护
- 慢查询阈值 200ms 由 `internal/db` 显式配置，预期内的记录不存在不再按错误级别刷日志
- 视频状态机为 `draft → processing → published | rejected`：发布事务原子完成 `draft → processing`、写入发布时刻与 outbox 事件，worker 校验通过后 CAS 为 `published`；作者可查询 `processing`/`published`/`rejected`，`rejected` 可主动丢弃或在 `rejected_at + RETENTION_VIDEO_DRAFT_HOURS` 到期后由 sweeper 转入 `purging`
- 发布接口响应为 `202 + {"draft": DraftItem}`；`processing` 行在公开列表、详情与 `GET /mine` 中不可见，worker 校验通过后自动转为可见
- 仓库迁移文件的最高版本为 `000009`；该迁移为 `video_outbox_events` 增加 `next_attempt_at`、`locked_until`、`last_attempt_at`、`last_error` 与 `(status, next_attempt_at, id)` claim 索引，并把历史 pending 事件回填为立即可派发。2026-09-05 的 `no change` 只证明当时 `000008` 已应用，不能作为 `000009` 已应用的证据
- worker 进程内运行 relay 与 consumer：relay 在短事务内把到期 pending 或过期 publishing 事件批量 claim 为 `publishing`，以 `attempt` 作为围栏、`locked_until` 作为租约；publisher confirm 成功后标记 `dispatched`，失败时回到 `pending` 并按 1 秒起步、最高 5 分钟退避
- MQ 的 `EventSpec`、`ConsumerSpec` 与 `RetryPolicy` 是 exchange、routing key、queue、prefetch 和重试档位的唯一事实源；runtime 在意外断线后重建连接、发布器和拓扑，显式 `Close` 后永久终止
- consumer 手动 ack；基础设施失败依次投递到 `video.process.retry.1s`、`video.process.retry.5s`、`video.process.retry.30s`，TTL 到期后回主队列。初次投递加三次重试均会实际处理，最后一次仍失败才进入 `video.process.dead`
- 消息载荷只含 schema 版本、event_id、video_id 与媒体相对路径；API 不建立 MQ 连接，MySQL outbox 仍是待派发事件的唯一事实源
- API 与 sweeper 使用 `./.run/uploads`，worker 经 Compose 的 `backend_uploads` 共享卷访问同一存储；本地直连运行时 worker 使用相同相对路径
- RabbitMQ 参与 worker 闭环且 worker 启动要求其可达；API 侧不建立 MQ 连接，`/ready` 不新增依赖；Redis 仍无客户端连接

## 模块开发流程

每个模块都按“设计契约 → 实现 → 自动化验证 → 页面验收 → 独立提交 → review 暂停”推进。一个模块完成并提交后暂停，等待 review，再开始下一个模块；不要把多个行为边界混进同一提交。

### 1. 设计契约

在改代码前写清楚：

- 请求、响应、状态转换、错误码和兼容性边界
- 数据库字段、索引、迁移顺序、回滚前置条件
- 并发、重复请求、进程崩溃、重试和不可逆操作语义
- 哪个系统是事实来源，以及缓存或队列不可用时的降级策略
- 需要更新的 `API.md`、README/本文、监控事件和测试夹具

接口改动先稳定后端契约和 `API.md`，再接入前端。迁移改动先确认真实数据库的 `schema_migrations`、列、索引和数据状态，不以旧文档或口头假设替代检查。

### 2. 实现与边界

只修改本模块拥有的后端、前端、迁移、配置或文档文件。先复用当前 controller/service/repository、存储、会话、清扫和列表装配能力；适配器放在所属业务包中，不为单一小适配器新增包。保持既有 package 依赖方向与代码风格；新增导出函数和不可自明的小辅助函数用现有简短中文用途注释说明目的，新增 Go 测试注释按 `AGENTS.md` 使用两行中文格式。服务端状态和数据库事实优先于客户端临时状态、模拟数据或兼容性旁路。

### 3. 自动化验证

先运行本模块最小回归，再按共享行为的影响范围补充完整回归。真实 MySQL 未配置时，必须明确记录“集成测试跳过”，不能把跳过当作通过。

### 4. 页面验收

涉及页面、播放、上传或视口行为时，至少检查加载、空状态、失败恢复、取消、路由离开和桌面/移动视口；文字、提示和原生视频控制不能重叠或溢出。浏览器测试 mock 公共 Feed API 时，只能证明页面行为，不能替代真实 MySQL 下的发布、鉴权和媒体联调。

### 5. 独立提交与 review

提交前执行并检查输出：

```bash
git status --short
git diff --cached --name-only
git diff --cached --check
git commit --only -m "<type>: <简短中文摘要>" -- <本模块路径>
```

不得使用无差别的 `git add .`。提交信息沿用 `feat:`、`fix:`、`test:`、`ci:`、`docs:` 等历史格式。提交后更新本文的模块状态、提交哈希、验证结果和下一阶段；README 只保留运行说明、项目摘要和本文入口。

## 当前路线

路线顺序是：**Feed 服务端可靠性 → RabbitMQ 视频处理闭环 → Redis 定向能力**。在前一阶段的契约和回归稳定前，不提前引入后一阶段的业务客户端。

### 下一步执行顺序（按提交历史校准）

1. **先完成 `000009` 与真实依赖验收**：在隔离 MySQL 测试库应用全部迁移，核对 outbox 列、默认值、回填与 claim 索引；再用真实 RabbitMQ 覆盖意外断线重连、拓扑恢复、1s/5s/30s 延迟、最终死信、并发 relay、租约接管和重复投递。测试媒体目录必须临时隔离
2. **补齐 MQ 运维观测**：基于现有状态字段增加 pending/publishing 数量、最老待派发年龄、租约接管、发布失败、重连与 DLQ 深度指标和告警；先观测单队列压力，再决定是否拆分更多事件类型
3. **进入 Redis C1**：登录/注册限流只依赖 Redis 客户端、429 契约、TTL 和 Redis 故障降级；会话缓存按指标评估。用户列表分页是独立的 API 与前端交付，不依赖 Redis
4. **持续复用横切错误契约**：后续 HTTP 模块直接复用 `apierror.Rule`/`Write`，不重新建立模块私有的状态码分支

阶段一的全部列表游标契约已闭合；MQ 可靠性代码已经提交，但在第 1 项完成前只能称为“静态与无外部依赖回归通过”，不能宣称真实运行时故障闭环已验收。

### 阶段一：Feed 服务端可靠性（已完成）

目标是让公开 Feed 在并发新增、删除、作者变化和暂态故障下仍保持稳定顺序、完整响应和可解释错误。此阶段只改后端及必要的契约/测试，不接入 Redis/RabbitMQ，也不先改前端交互。

建议拆成以下可独立回归的模块：

1. **Feed 数据不变量与查询边界（已完成，`c79100c`）**：公开详情、列表、我的视频、作者计数和互动资源校验统一要求发布状态、未软删除、非空发布时间及完整媒体字段；服务层对异常实体 fail-closed，真实 MySQL 与路由回归已覆盖残缺记录
2. **模型绑定的公开视频查询入口（已完成，`0842820`）**：将 scope 设为 `video` 包内私有，导出 `PublicVideoQuery(db)` 固定 `Model(&Video{})` 与公开视频条件；视频和互动仓储只追加作者、游标等局部条件，不新增迁移、API、前端或额外查询
3. **游标契约（已完成，`61fb00e`）**：保留 `(published_at, id)` 的 keyset 顺序和 `limit+1`；游标使用版本 `1` 并绑定 `public`、`author`、`mine` 范围，旧格式、篡改、版本或范围不匹配统一返回 `400`
4. **作者批量补全（已完成，`4e253f9`）**：`AuthorReader` 扩展批量入口，列表作者读取收敛为每次请求一次批量查询；保持 `video → user` 依赖方向，缺失或已注销作者继续返回稳定占位资料，详情仍走单条读取
5. **互动统计故障语义（已完成，`fb867c8`）**：统计查询失败时列表、详情、我的视频与发布响应统一返回 `503` 并整体失败，禁止把失败伪装成零计数；发布响应组装失败时客户端应改查公开详情而不是重复提交发布
6. **可观测性与查询预算（已完成，`69a08c1`）**：GORM 语句回调实现请求内查询计数并随请求完成日志输出；公开 Feed 首页与详情的查询预算 ≤4 条语句由真实 MySQL e2e 断言，慢查询阈值 200ms 显式配置
7. **并发和异常测试（已完成，`d185645`）**：以表级故障注入夹具在真实 MySQL 上覆盖同刻发布排序、分页期间新增/软删除、作者注销占位、统计失败 503、暂态错误干净路径与 GET 幂等；残缺记录排除与视频游标 400 矩阵引用既有回归
8. **social 列表游标版本化（已完成，`22174a5`、`62313b1`）**：评论、关注、粉丝列表接入版本 `1`；评论游标绑定 `comments + video_id`，两类关系列表分别绑定 `followers`/`following + user_id`；严格拒绝旧格式、未知字段、版本、跨资源和跨列表类型不匹配值，`API.md` 与真实 MySQL 路由合同已同步

### 查询入口类型安全演进

当前公开视频入口使用 GORM 的结构化条件和值参数绑定，M1 已由 `PublicVideoQuery` 固定模型与公开边界；主要剩余风险不是 SQL 注入，而是手写列名与模型漂移

- `PublicVideoQuery(db *gorm.DB)` 负责绑定 `Video` 模型并应用唯一的公开视频边界
- `publicVideoScope` 只在 `video` 包内使用，避免其他包把公开条件套到错误模型
- `WithContext(ctx)` 仍由仓储调用方传入，事务、取消和连接生命周期保持可见
- 详情、列表、计数和互动校验均从该入口开始，再追加各自的 ID、作者或游标条件

该封装是 API 级别的边界收紧，不能阻止调用方显式使用 `Unscoped`、`Table` 或原生表达式，因此真实 MySQL 行为测试仍是必要的。查询模式稳定后，再单独评估 `gorm.io/gen`：用生成字段替代公开视频读取路径中的手写列名，并通过 `go generate` 与 CI 检查生成代码是否过期；不把生成器引入迁移、DDL 或清扫维护操作。

阶段一核心可靠性已完成并支撑了后续 R1/R2；social 列表游标补丁已提交且不回滚或阻塞已经提交的异步发布闭环。Redis C1 按自身 Redis 依赖推进，不等待阶段二真实 broker 验收。

### 阶段二：RabbitMQ 视频处理闭环（可靠性增强已提交，待真实依赖验收）

RabbitMQ 首个业务闭环只负责视频异步处理；outbox 与草稿清扫分别维护自己的租约和围栏语义。当前拆为六个已交付模块：

1. **状态机与 outbox 迁移（已完成，`c2134ac`）**：`000006` 新增 `video_outbox_events` 表（`event_id` 唯一、`(status, id)` 轮询索引）与 `videos.rejected_reason` 列，删除派生的计数值列；发布事务在同一事务内完成媒体完整性校验、`draft → processing` CAS、发布时刻与 outbox 事件写入
2. **relay/worker（已完成，`d2294a2`）**：`internal/mq` 提供连接、拓扑与 confirm 发布器；relay 轮询 outbox 派发，consumer 手动 ack 并按媒体校验结果 CAS 到 `published`/`rejected`；启动期 MySQL 与 RabbitMQ 连接按退避重试，Compose worker 挂载 `backend_uploads`。其最初的进程内重试和无租约轮询已由第 6 项替换
3. **R3-A API 异步状态（已提交，`f4fafeb`）**：发布接口返回 `202 + {draft: DraftItem}`；新增作者状态查询端点，顶层固定返回 `status`、`published_at`、`rejected_at`、`rejected_reason`；公开读取继续隐藏 processing/rejected
4. **R3-B rejected 生命周期（已提交，`f4fafeb`）**：`DELETE /api/video/auth/drafts/:id` 接受 `draft`/`rejected`；`GetExpiredDraftPurgeList` 和 claim 按各自时间基准筛选，`000007` 保持不变，`000008` 回填历史 `rejected_at` 并增加 `(status, rejected_at, id)` 索引
5. **R3-C 前端异步状态（已提交，`5ada6f9`）**：发布页面消费状态查询接口，轮询 `processing`，展示 `published`/`rejected`，并在组件销毁时取消轮询；提交同时更新视频 API 类型和页面单测
6. **MQ 可靠性增强（已提交，`7f97382`）**：`internal/mq` 集中事件与消费规格并由 runtime 管理连接生命周期；`000009` 为 outbox 增加 publishing 租约、attempt 围栏、失败原因和下次尝试时间；relay 批量 claim，consumer 经三个 TTL 重试队列延迟投递，worker 在关闭 broker 前等待 relay/consumer 退出

阶段二当前的收口门槛包括真实 MySQL/RabbitMQ 集成和前端门禁记录：必须验证 `000009` 迁移与回填、relay 崩溃及租约接管、worker 重启、连接重建后的拓扑恢复、重复消息、三档延迟与最终死信、媒体缺失、状态不可逆和消息/数据库不一致，并补记 R3-C 的 lint、单测、构建及必要的浏览器回归。不要把 fake dialer 单测或“能连接 RabbitMQ”当作真实故障闭环完成。

2026-09-05 的后端收口记录：目标库执行 `migrate ... up` 返回 `no change`；`go vet ./...`、`go test -count=1 ./...`、`go test -race -count=1 ./...` 均在隔离 MySQL 测试库通过。运行日志观察到 worker 启动、consumer 订阅 `video.process` 并完成 `video_id=10`，sweeper 轮次成功且无待清扫项。本轮明确不执行前端、Docker/Compose 或真实 RabbitMQ 故障矩阵；因此这些未覆盖项不构成已完成的阶段二运行时验收。

### 横切计划：API 错误处理复用（已实现，2026-09-09）

`API_ERROR_REUSE_PLAN.md` 的公共契约与 video/user/social 迁移已一次实现，未按阶段 A–D 拆分提交。`internal/error` 提供 `Code`/`Descriptor`/`Rule`/`Resolve`/`HTTPStatus` 与 `Write`/`WriteJSON`/`WriteUnauthorized`/`WriteCode`；三个 controller 改为模块级 `[]apierror.Rule` 映射，保留现有 `{"error":"..."}` 响应、登录专用 401、统计 503 固定文案和 `errors.Is` 语义。旧 `ParseStatusCode` 已删除（全仓库无调用者且无法表达 403/409/413/503）。`jwt` 中间件、worker 和 sweeper 仍不接入 HTTP 映射。按用户要求本轮跳过测试环节，仅执行构建与既有单测回归。

### 阶段二后续：消息队列可靠性（核心实现已提交）

`7f97382` 已实现 `MESSAGE_QUEUE_RELIABILITY_PLAN.md` 的核心代码边界：MQ 规格单一事实源、运行中重连、outbox claim 租约/退避，以及 consumer 分级重试/DLQ。尚未完成的是在真实 MySQL/RabbitMQ 上执行迁移与故障矩阵，以及积压、租约接管、重连和 DLQ 的运维指标；只有指标证明必要时才增加多类型队列。

### 阶段三：Redis 定向能力

先依据指标选择短生命周期能力，优先级建议为登录/注册/上传限流，再评估会话校验缓存、短期幂等键或异步进度。每项能力在实现前固定：key 命名、TTL、主动失效、并发原子性、命中率指标和 Redis 故障时的降级（回退 MySQL 或拒绝请求）。

Redis 不成为用户、视频、草稿或清扫状态的权威来源；当前游标 Feed 不做缓存，除非读压力指标证明收益；也不把 Redis 自动加入现有 `/ready` 的必需依赖。

## 延后事项与风险

- 媒体文件已落盘、数据库绑定前进程崩溃时可能产生孤儿文件，需要补偿清扫或可审计的回收策略
- 本地磁盘存储只适合单机或共享卷部署；异步 worker、API、sweeper 必须明确共享卷或对象存储契约
- 用户列表目前没有分页，数据量增长后会影响响应体和查询成本
- `observe.pprof` 在配置示例中存在，但当前配置加载器不读取；启用时要使用独立、仅回环监听的 `ServeMux` 和独立关闭生命周期
- 阶段一所有列表游标契约均已提交并通过回归；R1、R2、R3-A/R3-B、R3-C 及 MQ 可靠性增强均已提交，阶段二仍缺 `000009` 的真实 MySQL 对齐和真实 RabbitMQ 故障矩阵
- 手写 GORM 列名尚未达到完整编译期类型安全；`gorm.io/gen` 延后到读取模式稳定后评估，避免当前引入生成代码和持久化层大范围改写
- 草稿 `purging` 是不可逆状态；涉及 `000004` 回滚时必须停止 API 与所有 sweeper，确认不存在不兼容行后再执行 down migration，不能用故意失败的 SQL 阻止回滚

## 验证与验收

### 后端

从 `backend` 目录执行：

```bash
go vet ./...
go test ./...
go test -race -count=1 ./...
```

涉及迁移、实体或查询时，补充真实 MySQL 的迁移和 schema 对齐测试。Windows 默认 Go 缓存不可写时，使用当前任务专用的仓库内 `GOCACHE` 后重跑相同命令。

### 当前模块验证记录

- Feed 数据不变量与查询边界：`go vet ./...`、`go test ./... -count=1`、`go test -race -count=1 ./...` 均已在真实 MySQL 下通过；覆盖状态过滤、软删除、空发布时间、六个媒体字段缺失、详情 404、列表排除和作者统计
- 本模块未修改迁移、`API.md`、前端或游标格式；代码已提交为 `c79100c`，文档路线此前已提交为 `1135c1c`
- 模型绑定的公开视频查询入口：`go vet ./...`、`go test ./... -count=1`、`go test -race -count=1 ./...` 均通过；真实 MySQL 集成覆盖入口自行绑定模型、公开边界和软删除过滤，代码已提交为 `0842820`
- 视频游标契约：`go vet ./...`、`go test ./... -count=1`、`go test -race -count=1 ./...` 均通过；真实 MySQL 路由回归覆盖全局、作者和我的视频列表的范围复用，以及旧格式游标的 `400` 语义；代码已提交为 `61fb00e`，API 契约已同步
- 作者批量补全：`go vet ./...`、`go test ./... -count=1`、`go test -race -count=1 ./...` 均已在真实 MySQL 下通过；覆盖批量 `IN` 查询、软删除过滤与公开列投影、空/零值输入短路、缺失作者占位补全、批量错误透传，以及列表单次批量读取、详情单条读取的行为断言；代码已提交为 `4e253f9`
- 互动统计故障语义：`go vet ./...`、`go test ./... -count=1`、`go test -race -count=1 ./...` 均已在真实 MySQL 下通过；覆盖统计失败时公开列表、我的视频、详情与发布响应的哨兵错误及底层原因保留，控制器 503 映射与固定文案不回显内部错误，正常路径统计值逐项透出；`API.md` 已同步四个接口的 503 说明，代码已提交为 `fb867c8`
- 可观测性与查询预算：`go vet ./...`、`go test ./... -count=1`、`go test -race -count=1 ./...` 均已在真实 MySQL 下通过；覆盖计数回调按语句递增、重复注册幂等、多上下文独立、完成日志携带 `db_queries`，以及公开 Feed 首页与详情 ≤4 条语句的预算断言；代码已提交为 `69a08c1`，预算断言 e2e 已提交为 `914f343`
- 并发与异常测试收尾：`go vet ./...`、`go test ./... -count=1`、`go test -race -count=1 ./...` 均已在真实 MySQL 下通过；表级故障注入夹具覆盖同刻发布排序翻页、分页期间新增/软删除、注销作者占位、统计失败 503（无零计数半组装响应）、注入错误干净路径与 GET 幂等；代码已提交为 `d185645`
- R1 状态机与 outbox 迁移：`go vet ./...`、`go test ./... -count=1`、`go test -race -count=1 ./...` 均已在真实 MySQL 下通过；覆盖 `000006` 迁移与模型对齐、发布事务 CAS 与 outbox 原子写入、重复发布拒绝、outbox 失败整体回滚、processing 期间公开不可见；代码已提交为 `c2134ac`，发布事务改造与回归已提交为 `8342fa2`
- R2 relay/worker：实现提交为 `b0321ef`、`d2294a2`，共享卷为 `6eaab01`，集成测试信道清理为 `1678360`；单测覆盖拓扑声明、confirm 发布、派发标记、发布失败保持 pending、孤立事件跳过、媒体校验发布/拒绝、重复消息幂等、重试退避与计数头、上下文取消重投、死信动作，以及 worker 启动期连接退避。2026-09-05 的 `go vet ./...`、`go test -count=1 ./...`、`go test -race -count=1 ./...` 均通过；worker 实际订阅并完成 `video_id=10`。真实 RabbitMQ 的派发闭环、死信和真实队列重发未在本轮执行
- R3-A/R3-B：已提交 `f4fafeb`，覆盖 `202 + DraftItem`、状态查询顶层字段、rejected 主动丢弃、按 `rejected_at` 到期候选、租约 claim、000008 回填与索引。2026-09-05 目标库 `migrate ... up` 返回 `no change`，并重新通过 `go vet ./...`、`go test -count=1 ./...`、`go test -race -count=1 ./...` 与 `git diff --check`
- R3-C 前端异步状态：提交 `5ada6f9` 更新视频 API 类型、状态查询、发布页轮询和 `PublishVideoView` 单测；本轮按范围不执行前端门禁，不能将其写为新的前端验收结论
- social 列表游标版本化：`22174a5`、`62313b1` 已提交；2026-09-05 的 `go vet ./...`、`go test -count=1 ./...`、`go test -race -count=1 ./...` 均已在真实 MySQL 下通过。`social` codec/service 测试与 `router.TestSocialCursorScopeContract` 覆盖正常分页、旧格式、跨视频、跨用户和 `followers`/`following` 互换游标的 `400` 语义，未新增迁移或前端改动
- API 错误处理复用（2026-09-09）：公共契约与 video/user/social 迁移一次交付，未新增迁移或前端改动。本轮按用户要求跳过测试补写，仅执行 `go build ./...`、`go vet ./...` 与既有单测 `go test -count=1 ./internal/error/... ./internal/video/... ./internal/user/... ./internal/social/...`，结果全部通过；`internal/error` 暂无测试文件。新增的 `apierror` 规则矩阵、cause 保留和默认文案断言未补，属未覆盖边界
- MQ 可靠性增强（2026-09-12）：代码提交为 `7f97382`；`go vet ./...`、`go test -count=1 ./...`、`go test -race -count=1 ./...` 与 `git diff --check` 均通过。无外部依赖的契约、runtime、退避和 UTF-8 截断用例实际运行；需要数据库的 schema/outbox/worker 用例因未配置可连接 MySQL 而跳过，真实 RabbitMQ 重连、延迟队列和死信链路也未执行，不能称为集成回归通过
- 阶段状态：Feed 核心可靠性与阶段二代码已提交；`000009` 的真实 MySQL 对齐、RabbitMQ 故障矩阵和前端门禁仍待收口；API 错误复用与 MQ 可靠性核心实现均已完成

### 前端

从 `frontend` 目录执行只读检查：

```bash
pnpm.cmd run lint
pnpm.cmd run test:unit -- --run
pnpm.cmd run build
pnpm.cmd run test:e2e -- --project=chromium --project="Mobile Chrome"
```

最后一项按交互/视口改动执行；`lint:fix` 和 `format` 会写文件，不作为 CI 门禁。新增 Vitest mock 时为 `vi.fn` 使用真实函数签名泛型。

### 部署与本地验收

- Compose 或健康检查改动执行 `docker compose config --quiet`
- 本地默认使用“本机 MySQL + 直接启动 Go/Vue”：从 `backend` 运行 `go run ./cmd`，按需运行 `go run ./cmd/sweeper`，从 `frontend` 运行 `pnpm.cmd dev`
- 先执行未应用迁移，再按以下顺序做基础验收：
  1. `/health` 返回 `200`；MySQL 可用时 `/ready` 返回 `200`；前端 `/api` 与 `/static` 代理可用
  2. 注册、登录、刷新令牌和退出；未登录访问受保护页面会跳转登录
  3. 上传视频和封面并发布，接口先返回 `202`，发布页轮询 `processing`/`published`/`rejected`；本地启动 RabbitMQ 服务并运行 worker 后，成功视频自动流转为可见
  4. Feed 首屏、游标分页、到底、空状态和失败重试可用，视频按纵向短视频流播放
  5. 详情、作者主页、用户列表和我的视频可访问；删除自己的视频后列表与提示正确更新
  6. 账户设置中的用户名、资料、密码和注销操作反馈正确
  7. 桌面与移动视口各走一遍，确认导航、提示、文字和视频控制无重叠或溢出
- CI 继续保持冻结锁文件安装、只读 lint、单测、类型检查、构建和浏览器回归；失败产物保留报告、截图、视频和 trace

## 文档维护规则

每个模块提交后更新以下四项：

1. 当前状态和提交哈希
2. 实际运行且通过的验证；跳过项及原因
3. 新增的事实来源、迁移版本或契约变化
4. 下一模块及其前置决策

README 的“项目进度”只做摘要，并链接到本文；详细路线、验收和风险只在本文维护。若代码与本文冲突，以当前路由、迁移和真实数据库状态为准，并在本模块完成时修正文档。
