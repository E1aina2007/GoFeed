# GoFeed 源码导读

> 阅读基线：2026-10-08，`F:\work\Feed\GoFeed`。Interaction 已完成 HTTP、持久化及统计迁移，Relation 的五个 HTTP、用例与原 v1 游标已迁入四层，R2-B 后端/API 已提交为 `f9481b2`。R2-C 已迁关系 ORM/SQL、计数与 Following 活动观看者依赖并删除旧 social，后端为 `ea36d40`；Following 视频 SQL 仍在 Video。R3-A 三个匿名账户 GET 已迁入独立 Account 四层，后端/API 为 `35a6fe0`；R3-B 注册后端/API 已提交为 `a834d46`。R3-C 登录、刷新与退出提交为 `f20dcdf`；R3-D 改密与注销提交为 `4f4838b`；R3-E 改名、资料与头像提交为 `f5c1260`。R3-F1 作者读取与资料统计已解除旧 user 类型耦合，提交为 `c335902`。R3-F2 唯一 User ORM/仓储已归 Account，旧 user 包已删除，提交为 `267463e`。R3-G1 JWT 与 HTTP 认证适配已归 Infra/Interfaces，提交为 `e84f783`。R3-G2 会话用例和唯一 AuthSession ORM/仓储已归 Account，旧 auth 已删除，提交为 `fe6959d`，未推送；R4-A1 已发布详情与公开列表已迁入 Video 四层，提交为 `d94bff7`，未推送；R4-A2 本人列表已提交为 `b4e145b`，未推送；R4-A3 处理状态读取已提交为 `1b0acfc`，未推送；R4-B1 草稿创建/读取已提交为 `2f315e8`，未推送；R4-B2 共享媒体规则/唯一存储已归 Domain/Infrastructure，提交为 `e56d7bf`，未推送；R4-B3 视频上传已接 Video 四层，提交为 `6bc4926`，未推送；R4-B4 封面上传提交为 `fefc4c4`、R4-C1 草稿发布提交为 `9f0a393`，均未推送；账户 HTTP 全部归 Account，本地媒体统一使用 infra/storage/media，视频/封面上传均已接 Video 四层，所有绑定事务继续保留。实施边界见开发计划第 6.8–6.14 节，提交摘要见第 6.4 节，验证与缺口见第 5 节；第 6.14 节其余 R4–R6 模块尚未实施。本文从当前源码推导；Hot/Recommend、完整热度覆盖及指标出口尚未实现。
>
> 本文用于理解源码。运行与配置看 [README](../README.md)，接口字段看 [API](../API.md)，未完成设计与历史验收看 [开发计划](./DEVELOPMENT_PLAN.md)。本文中的“源码入口”均可直接点击。

## 阅读导航

- [1. 项目全貌与能力边界](#1-项目全貌与能力边界)
- [2. 目录、分层与依赖方向](#2-目录分层与依赖方向)
- [3. 数据模型与公开不变量](#3-数据模型与公开不变量)
- [4. Timeline：一次 Feed 请求怎样完成](#4-timeline一次-feed-请求怎样完成)
- [5. Following：关注关系如何变成视频流](#5-following关注关系如何变成视频流)
- [6. 多层缓存：页缓存、卡片缓存与实时数据](#6-多层缓存页缓存卡片缓存与实时数据)
- [7. 发布链路：事务、Outbox、消费与预热](#7-发布链路事务outbox消费与预热)
- [8. 抗风险亮点：降级、隔离、围栏与恢复](#8-抗风险亮点降级隔离围栏与恢复)
- [9. 账户、互动与媒体清扫](#9-账户互动与媒体清扫)
- [10. 前端：分页并发与播放生命周期](#10-前端分页并发与播放生命周期)
- [11. 观测、运行与验证边界](#11-观测运行与验证边界)
- [12. 向量与推荐：当前边界及未来接入位置](#12-向量与推荐当前边界及未来接入位置)
- [13. 建议阅读顺序与自检题](#13-建议阅读顺序与自检题)

## 1. 项目全貌与能力边界

GoFeed 是一个 Go/Gin + Vue 的短视频系统。它已覆盖账户会话、草稿上传、异步发布、视频流、关注、点赞、评论和后台回收。理解项目时，先记住三个职责：

1. **MySQL 保存业务事实**：用户、会话、视频状态、互动关系和待派发事件。
2. **Redis 加速或限制访问**：当前用于注册/登录限流、Timeline 页缓存、基础卡片缓存及热度分钟桶；热度是 MySQL 互动事实的派生结果，尚未用于 Hot 读取。
3. **RabbitMQ 承接异步处理**：当前用于视频媒体校验与发布后卡片预热，事件从 MySQL Outbox 派发；独立互动 Relay 向 `feed.heat` 投递事实，当前 worker 同时运行幂等热度消费者。

视频文件保存在本地媒体目录；MySQL 保存相对访问地址和关联信息。数据库中的媒体字段完整，不等于文件此刻一定可读。

```mermaid
flowchart LR
    Browser["Vue 页面"] --> API["Go API / Gin"]
    API --> MySQL["MySQL：业务事实与 Outbox"]
    API --> Redis["Redis：限流、缓存与派生热度"]
    Browser --> Static["/static：本地媒体访问"]
    API --> Files["本地媒体目录"]
    Static --> Files
    Worker["Worker：Relay 与消费者"] -->|读取 Outbox| MySQL
    Worker -->|确认发布| MQ["RabbitMQ"]
    MQ -->|视频处理、卡片预热、热度消费| Worker
    Worker -->|状态变更| MySQL
    Worker -->|校验媒体| Files
    Worker -->|卡片预热与热度分钟桶| Redis
    Sweeper["Sweeper：定期清扫"] --> MySQL
    Sweeper --> Files
```

### 1.1 哪些能力已经进入请求链路

| 内容                       | 当前状态                 | 阅读时应关注的边界                                        |
| -------------------------- | ------------------------ | --------------------------------------------------------- |
| Timeline 最新视频流        | 已实现，匿名读取         | MySQL 时间排序；首页使用 `/api/feed?scene=timeline`       |
| Following 关注视频流       | 后端与页面已实现         | 认证后查询当前关注关系；当前全部读 MySQL                  |
| Timeline 页缓存            | 已实现，直接装配         | 只读写带游标的后续页，首屏直接查询 MySQL                  |
| 基础卡片缓存               | 已实现，直接装配         | 与页缓存一起装配；只用于后续页的页缓存命中路径          |
| 发布事件与卡片预热         | 已实现，直接装配         | `video.published` → `feed.card.warm`；重读当前 MySQL 卡片 |
| 互动事实存储与可靠派发     | 已提交，直接装配         | `interaction_outbox_events` → Relay → `feed.heat`；派发不是覆盖完成 |
| 热度消费与分钟桶           | 已实现，直接装配         | 原创建分钟归属、Hash 去重及绝对分数 → ZSET；coverage 为 unverified |
| 注册/登录限流              | 已接入                   | Redis 固定窗口；Redis 故障时放行                          |
| Feed 指标与容量基线        | 仅配置及请求回调已提交   | 无采集器、监听装配或容量工具；第 11 节标明边界            |
| Following 混合推拉 / Inbox | 尚未实现                 | 当前关注流没有 Redis Inbox 或粉丝写扩散                   |
| Hot 热榜                   | 尚未实现                 | `scene=hot` 返回 501；已有分钟桶代码，没有重建或快照      |
| Recommend / 向量召回       | 尚未实现                 | `scene=recommend` 返回 501；没有向量模型、索引或检索链路  |

源码入口：[Feed 场景分派](../backend/internal/application/feed/service.go) · [实际路由装配](../backend/internal/router/router.go) · [运行参数配置](../backend/internal/config/config.go)。

## 2. 目录、分层与依赖方向

### 2.1 先找到三个进程入口

| 入口                                                          | 负责什么                                        | 主要依赖                                    |
| ------------------------------------------------------------- | ----------------------------------------------- | ------------------------------------------- |
| [backend/cmd/main.go](../backend/cmd/main.go)                 | API 启动、资源装配、HTTP 生命周期               | MySQL；可恢复 Redis Runtime；不建立 MQ 连接 |
| [backend/cmd/worker/main.go](../backend/cmd/worker/main.go)   | 视频与互动 Relay、视频/预热/热度消费者及队列观测 | MySQL、RabbitMQ、Redis、共享媒体 |
| [backend/cmd/sweeper/main.go](../backend/cmd/sweeper/main.go) | 到期用户、视频、草稿和孤儿媒体清扫              | MySQL、媒体目录；不建立 MQ 连接             |

三个入口可以独立运行。API 接受发布请求与 worker 完成发布是两个不同阶段。

worker 的 `main` 负责加载配置、连接 MySQL/视频 MQ、接收退出信号；`startWorkers` 负责组件构造和八个循环的统一启动与收尾。循环分别是视频 Relay/消费者/MQ 观测、卡片预热消费者/队列观测、互动 Relay、热度消费者/队列观测。热度规则和投影仍在既有 domain/application/infra 包中，入口只组合依赖，没有独立启动包。

这里参考的是 GCFeed 的 `cmd/worker/main.go:startWorkers` 编排方式。GCFeed 没有独立热度消费者，互动应用服务直接调用 Redis 计分；GoFeed 保留 MySQL 同事务事实 → Outbox → Relay → 热度消费，避免把参考项目的请求内 Redis 写入当成当前实现。

### 2.2 既有三层业务与四层模块并存

```text
backend/
├─ cmd/                         API、worker、sweeper 入口
├─ db/migrations/               表、索引与状态机字段的版本迁移
└─ internal/
   ├─ router/                   HTTP 组合根：创建依赖、注册路由
   ├─ video/                   未迁丢弃/删除、媒体兼容值、唯一 Video/Outbox ORM/SQL
   ├─ domain/video/            状态、公开/处理状态/草稿/媒体模型、小读写/存储/绑定端口与纯规则
   ├─ application/video/       公开/本人/处理状态、草稿创建/读取与视频上传编排、原 v1 视频游标
   ├─ infra/persistence/video/ 原公开/本人列表/GetByID/Create/媒体绑定、作者/互动与错误的外层转换
   ├─ interfaces/http/video/  公开/本人/处理状态/草稿 GET、草稿/视频上传 POST、DTO 与错误
   ├─ domain/account/          账户/资料、注册/改密/改名/头像规则、凭据/会话及小端口
   ├─ application/account/     账户读写/原 v1 游标、会话与头像文件补偿编排
   ├─ infra/persistence/account/ 唯一 User/AuthSession ORM 与原 SQL、公开账户/作者/统计/媒体适配、原子写入及 bcrypt
   ├─ interfaces/http/account/ 全部账户 HTTP 的参数/DTO/错误及 multipart 解析
   ├─ infra/jwt/               JWT 签发/解析、密钥与刷新随机/哈希
   ├─ interfaces/http/auth/    共享 Authorization、会话校验及 Gin 上下文
   ├─ domain/feed/             Feed 读模型、场景、读取端口、热度规则
   ├─ application/feed/        分页编排、缓存、卡片预热与热度事实映射
   ├─ infra/persistence/feed/  适配既有 MySQL 仓储
   ├─ infra/cache/feed/        Redis 键、编码、Lua 与超时
   ├─ interfaces/http/feed/    HTTP 参数、认证、响应 DTO
   ├─ domain/interaction/      互动规则、不可变事实与 Outbox 端口
   ├─ application/interaction/ 读写编排、评论游标与租约派发
   ├─ infra/persistence/interaction/ 读取适配、同事务业务/事实及派发状态
   ├─ interfaces/http/interaction/ 点赞状态、评论列表与四种写入口
   ├─ domain/relation/         关注规则、状态、独立列表/位置与读取端口
   ├─ application/relation/    关注读写编排、列表分页与 v1 游标
   ├─ infra/persistence/relation/ 唯一 Follow ORM、关系 SQL 与领域端口实现
   ├─ interfaces/http/relation/ 三个认证关注入口及两个匿名列表的 HTTP/DTO
   ├─ middleware/cache/        Redis Runtime 的故障与恢复状态
   ├─ middleware/ratelimit/    注册/登录限流策略
   ├─ mq/ worker/              MQ 拓扑、连接、确认与事件处理
   ├─ sweeper/                 数据和媒体回收
   └─ observability/ db/ error/ 日志、就绪检查、查询计数、错误映射
```

Feed 采取渐进拆层：通过读取边界和小接口复用已有仓储。互动事实写入、Relay 与热度消费直接装配；六个互动入口、ORM、直接读取及批量统计已归 Interaction。R1-B2 通过外层适配器将领域统计注入 Feed/Video，并组合用户获赞与关注计数；R2-A/B 已迁入五个关系 HTTP、用例及游标，R2-C 将 ORM/SQL 和计数收口到 Relation，并接管 Following 活动观看者检查。Following 的完整视频查询保留在 Video。

账户 HTTP 已全部迁入四层。R3-F1 将作者读取迁入 Account Infrastructure，公开读模型及资料统计使用 Domain Account 小端口；R3-F2 已迁唯一 User ORM/仓储，删除旧 user 包；R3-G1 将 JWT 实现归 Infra、共享认证/上下文归 Interfaces；R3-G2 已迁会话 ORM/编排并删除旧 auth。R4-A1/A2/A3 的公开/本人/处理状态读取已提交，R4-B1 接入草稿创建/读取，原 SQL 与未迁消费者继续保留。R4-B2 已将共享媒体规则/存储归 Domain/Infrastructure，提交为 `e56d7bf`；R4-B3 视频上传已提交为 `6bc4926`，未推送。后续按[开发计划第 6.14 节](./DEVELOPMENT_PLAN.md#614-r3-后续收口与-r4r6-重构路线)迁其他 Video、Worker/Sweeper 及技术包；其余模块尚未实施。

```mermaid
flowchart TD
    Root["router.New：创建并注入实现"] --> HTTP["interfaces/http/feed"]
    HTTP --> App["application/feed"]
    App --> Domain["domain/feed：模型与读取端口"]
    SQL["infra/persistence/feed"] -->|实现端口| Domain
    SQL --> Legacy["Video / Account 仓储"]
    SQL --> Relation["Relation 活动观看者读取"]
    RC["infra/cache/feed"] -->|实现缓存端口| App
    Root --> SQL
    Root --> RC
```

这里的箭头表示依赖/装配关系。应用层依赖接口，`router.New` 把实现注入进去；实际请求会调用注入的 SQL 或缓存适配器。Domain 不依赖 Gin、GORM 或 Redis 驱动。HTTP 的 JSON 字段集中在 [dto.go](../backend/internal/interfaces/http/feed/dto.go)，不由 Domain 实体承担。

重点读 [domain/feed/repository.go](../backend/internal/domain/feed/repository.go) 与 [legacy_reader.go](../backend/internal/infra/persistence/feed/legacy_reader.go)：后者复用 `video.Repository`，不会调用旧 `video.Service`，也不会把 Feed 外部游标转换成旧视频外部游标。

## 3. 数据模型与公开不变量

### 3.1 数据表解决什么问题

| 表                    | 保存的事实                                         | 关键源码 / 迁移                                                                                                                         |
| --------------------- | -------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| `users`               | 用户资料与软删除状态                               | [Account User ORM](../backend/internal/infra/persistence/account/user.go)                                                                                    |
| `auth_sessions`       | 会话、刷新令牌哈希、到期与撤销状态                 | [Account AuthSession](../backend/internal/infra/persistence/account/auth_session.go)                                                                                  |
| `videos`              | 作者、媒体引用、发布状态、排序时间、删除及清扫进度 | [video_entity.go](../backend/internal/video/video_entity.go)                                                                            |
| `video_likes`         | 哪个用户点赞了哪个视频                             | [000005](../backend/db/migrations/000005_social_interactions.up.sql)：`(video_id, user_id)` 唯一键                                      |
| `user_follows`        | 谁关注了谁                                         | [000005](../backend/db/migrations/000005_social_interactions.up.sql)：`(follower_id, followee_id)` 唯一键                               |
| `video_comments`      | 评论内容、作者与软删除状态                         | [interaction_model.go](../backend/internal/infra/persistence/interaction/interaction_model.go)                                            |
| `video_outbox_events` | 视频事务产生的待派发事件及派发租约                 | [000006](../backend/db/migrations/000006_video_outbox.up.sql)、[000009](../backend/db/migrations/000009_outbox_publishing_lease.up.sql) |
| `interaction_outbox_events` | 不可变互动事实及可变派发状态，无原对象级联删除外键 | [000010](../backend/db/migrations/000010_interaction_outbox.up.sql)；迁移文件存在不表示目标库已应用 |

点赞数、评论数由关系表按当前页视频 ID 聚合。[000006](../backend/db/migrations/000006_video_outbox.up.sql) 删除了 `videos` 中的冗余计数列，避免关系表与视频计数同时成为事实来源。

### 3.2 “公开”是一组条件

[PublicVideoQuery](../backend/internal/video/video_scope.go) 固定绑定 Video 模型，并同时要求：

- GORM 软删除作用域生效，`deleted_at` 为空。
- `status = published`，`published_at` 非空。
- 视频与封面的 URL、存储文件名、原始文件名均非空。

[Domain IsPublicVideo](../backend/internal/domain/video/video.go) 是唯一内存公开规则，额外排除零时间；旧 [Video IsPublicVideo](../backend/internal/video/video_service.go) 只桥接状态/删除/时间/媒体字段。公开 Video 用例、Feed 页读取、批量卡片和轻量公开状态复用同一规则，公开统计的 SQL 仍复用原作用域。

**`published_at` 在接受发布请求、进入 `processing` 时就写入。** 视频只有在 worker 校验后进入 `published` 才可见，所以时间非空并不能单独证明发布完成。公开排序使用发布请求时刻，不是 worker 完成时刻。

当前统一公开规则没有自动排除已注销作者的所有视频：Timeline 作者读取可返回“已注销用户”占位资料；Following 的 SQL 额外 JOIN 活动作者。这两个场景的作者边界需要分别理解。

### 3.3 读索引时把排序与过滤一起看

以下索引以迁移声明说明用途。R4-A1 已只读核对目标 feedsystem 的 videos 21 列/8 索引与原迁移一致，版本 10、dirty=false；表中其余关系/Outbox 索引并非本轮逐一核对结论：

| 索引                                                                               | 与读取行为的关系                             |
| ---------------------------------------------------------------------------------- | -------------------------------------------- |
| `idx_videos_published_id(published_at DESC, id DESC)`                              | 对应 Timeline 的时间与 ID 排序位置           |
| `idx_videos_author_published(author_id, published_at DESC)`                        | 对应作者历史视频与 Following 的作者候选读取  |
| `uq_user_follows_follower_followee(follower_id, followee_id)`                      | 防止重复关注，并支持当前观看者的关注关系查询 |
| `uq_video_likes_video_user(video_id, user_id)`                                     | 防止重复点赞，并以视频为前导列支持计数读取   |
| `idx_video_comments_video_visible(video_id, deleted_at, created_at DESC, id DESC)` | 对应可见评论与评论分页                       |
| `idx_video_outbox_events_claim(status, next_attempt_at, id)`                       | 对应待派发事件的状态、退避时间与限批位置     |

源码入口：[000001](../backend/db/migrations/000001_init.up.sql) · [000005](../backend/db/migrations/000005_social_interactions.up.sql) · [000009](../backend/db/migrations/000009_outbox_publishing_lease.up.sql)。

存在索引不表示优化器必定使用，也不表示 SQL 不需要扫描或排序。Following 同时过滤关系、作者和公开状态，关注规模及数据分布会影响计划；应对照真实 `EXPLAIN ANALYZE`，不能仅凭 keyset 或 SQL 条数给出容量结论。

### 3.4 已发布视频详情与作者页列表

[Video HTTP](../backend/internal/interfaces/http/video/handler.go) → [Application 读取](../backend/internal/application/video/service.go) → [Domain 小端口](../backend/internal/domain/video/video.go) → [Infrastructure](../backend/internal/infra/persistence/video/reader.go) → 原 [Video Repository](../backend/internal/video/video_repo.go)。`GET /api/video` 始终分页，author_id 空/0 是全局，非零按作者过滤；作者页继续调用这个 URL。`GET /api/video/:id` 仍匿名返回 video 包装，未找到/非公开为 404。本人列表与处理状态当前已随 R4-A2/A3 接入同一四层；草稿创建/读取随 R4-B1、共享媒体规则/存储随 B2、视频上传随 B3、封面上传随 B4 迁移；草稿发布随 C1 迁移；丢弃/删除、ORM 和所有 SQL 未迁。

列表用原 limit 默认 20/最大 50 与 limit+1；先完整公开过滤并截断，再读互动，最后一次批量读最终页去重作者。详情先完整公开检查，再作者、再互动。统计 nil 仍零值，空页不读作者/互动；数据库错误传播，统计故障不以零值伪装成功。[作者/互动转换](../backend/internal/infra/persistence/video/enrichment.go)复用 Account 和 Interaction 原实现，没有预读或额外重读；按源码，非空列表四条/空页一条 SQL，作者非零的详情四条，这些预算本轮未运行验证。

[Application 视频游标](../backend/internal/application/video/cursor.go)保留原 RawURL Base64 v1 的 v/k/a/p/i 顺序、public/author 范围与时间/ID 校验，不与 Feed 游标通用；R4-A2 的本人列表复用同一编解码，旧 Service 的重复编解码已删除；旧 Cursor 仅作为仓储适配位置保留。HTTP 仍先 limit 文本后 author_id，使用原 Gin Query 语义；[DTO](../backend/internal/interfaces/http/video/dto.go)逐字段保留原 JSON/omitempty，items 空数组、next_cursor 省略及 author.avatar_url 始终输出均保持。[外层错误转换](../backend/internal/infra/persistence/video/errors.go)保留原优先级和错误链，统计错误包裹未找到仍 404，普通统计故障 503，未知错误 500。

R4-A1 已提交为 `d94bff7`，未推送；实施轮 45 项源码对照、内层依赖及 vet/build 通过；5 个测试文件/36 函数未改且未运行。目标库仅 SELECT 元数据/聚合核对，实施前后相同，无数据库写入；没有真实公开视频、作者、统计、游标或 HTTP 回归。范围和未覆盖项见[开发计划 R4-A1](./DEVELOPMENT_PLAN.md#r4-a1已发布详情与公开列表已提交)。

### 3.5 本人已发布视频列表

`GET /api/video/auth/mine` 经原 JWT/session 中间件 → [Video Handler.GetMyVideoList](../backend/internal/interfaces/http/video/handler.go) → [Application 本人列表](../backend/internal/application/video/mine.go) → [Domain 单方法 AuthorVideoListReader](../backend/internal/domain/video/video.go) → [Infrastructure 适配](../backend/internal/infra/persistence/video/author_list_reader.go) → 原 Repository.GetAuthorVideoList。当前用户身份先于 limit 解析，作者范围来自认证上下文，不读取 query author_id；SQL 中仍过滤作者、published、软删除、发布时间和完整媒体，按原时间/ID keyset 排序。

用例保留用户 ID→依赖→limit→游标顺序、20/50 与 limit+1，复用同一 mine v1 范围检查和列表组装：先公开过滤/截断，再互动，再一次批量作者。没有新增活动用户查询、逐作者读取或额外重读，空页不读统计/作者，可选统计 nil 保留零值。按正常装配源码，非空五条/空页两条 SQL（含一条会话），本轮没有运行预算验证；响应复用原 Video DTO，认证和错误分类/文案保持。

确认生产/测试引用后，旧 mine HTTP/用例、独占组装/游标助手与 VideoItem/ListResponse 已删；旧写入 Service 只依赖写仓储，router 是构造器唯一调用点。唯一 Video ORM、完整 Repository、旧 Cursor/Author/EngagementCounts、批量上限/公开桥接仍留后续 R4-D；处理状态当前已随 R4-A3 迁移。R4-A2 实施轮 38 项源码对照、45 个内层 Go 文件依赖及 vet/build 通过，5 个测试文件/36 函数当时未改、未运行。目标库只读元数据/聚合前后相同，不证明真实列表/认证/游标/HTTP 行为；提交轮源码未改，沿用实施轮检查，13 个精确路径/暂存差异核对后提交为 `b4e145b`，未推送，范围与缺口见[开发计划 R4-A2](./DEVELOPMENT_PLAN.md#r4-a2本人视频列表已提交)。


### 3.6 视频处理状态读取

`GET /api/video/auth/:id/status` 经原 JWT/session 中间件 → [ProcessingStatusHandler](../backend/internal/interfaces/http/video/processing_status.go) → [Application ProcessingStatusService](../backend/internal/application/video/processing_status.go) → [Domain 单方法 ProcessingStatusReader](../backend/internal/domain/video/processing_status.go) → [Infrastructure 状态适配](../backend/internal/infra/persistence/video/processing_status_reader.go) → 原 Repository.GetByID。当前用户身份先于路径解析；用例依次检查视频/观看者 ID、读依赖、所属作者与允许状态，只有 processing/published/rejected 返回状态。缺失/软删、其他作者、draft/purging/未知状态仍 404。

状态读取不复用公开详情的媒体/发布时间校验：允许状态下仍可返回 nil 时间及空拒绝理由，原指针与时间精度不变。响应仍为 status/published_at/rejected_at/rejected_reason 四个无 omitempty 的顶层字段，nil 时间为 null、空理由为 ""。基础设施先将 GORM 未找到归一，其他错误复用原分类/文案，未知故障仍 500 video operation failed；认证与路径/错误助手未改。按正常装配源码，仅会话一条与视频 GetByID 一条 SQL，不读作者/互动、没有预查或重读；预算未运行验证。

确认引用后只删旧状态 HTTP/Service 方法与旧 DTO。worker 保留夹具仅增加一个 HTTP 包导入、替换三个状态 DTO 类型引用，条件/断言/解码不变，无新增/恢复测试；5 文件/36 函数继续保留，未运行。28 项源码对照、47 个内层 Go 文件依赖及 vet/build、文档/差异检查通过，原仓储/SQL/ORM 与未迁写入不变。目标库只读核对版本 10、dirty=false、videos 21 列/8 索引，元数据/聚合前后相同，无数据库写入；没有真实状态读取/认证/HTTP/数据库故障回归。提交轮源码未改，沿用实施轮检查，核对 12 个精确路径/暂存差异后提交为 `1b0acfc`，未推送，范围及缺口见[开发计划 R4-A3](./DEVELOPMENT_PLAN.md#r4-a3视频处理状态读取已提交)。


### 3.7 草稿创建与读取

`POST /api/video/auth/drafts` 与 `GET /api/video/auth/drafts/:id` 经原 JWT/session 中间件 → [DraftHandler](../backend/internal/interfaces/http/video/drafts.go) → [Application DraftService](../backend/internal/application/video/drafts.go) → [Domain DraftCreator/DraftReader](../backend/internal/domain/video/draft.go) → [Infrastructure 草稿适配](../backend/internal/infra/persistence/video/drafts.go) → 原 Repository.Create/GetByID。两个领域端口各一个方法；旧 ORM 类型只在适配中转换，原 SQL、软删除、默认事务/字段回填不变。

创建先身份/ShouldBindJSON，再用户 ID/端口可用，之后两项 TrimSpace、标题非空/rune≤255、描述 rune≤1000，最后一次 Create 后返回数据库填入的 ID/时间；HTTP 原 binding 与领域去空白校验并存，不新增解码规则。读取先身份/路径、ID/依赖、一次 GetByID，再作者 403、draft/purging 判断或 404；不要求媒体完整，也不查文件是否存在。没有新增活动账户/作者/互动查询、预读或写后重读；正常装配会话一次后各调用原 Create/GetByID 一次，读取两条 SQL，预算仅核对源码。

响应仍为 draft 包装、原十字段、201/200 和原 JSON/omitempty；只含原始展示名，不含 URL/物理名。has_video/has_cover 复用 Domain 唯一三字段非空规则，旧发布/丢弃共享 helper 仅替换两项标量调用；purging 完成标识不能证明文件仍可访问。原校验错误文本、403 only the author can modify this video、404 video not found、未知故障 500 video operation failed 与原错误优先级保持。

确认引用后删除旧创建/读取 HTTP/用例与 DraftRequest；旧 DraftItem/共享 helper 继续服务未迁发布/丢弃，完整仓储方法、唯一 ORM、媒体/写事务和后台流程保持。33 项源码检查、49 个内层 Go 文件依赖及 vet/build、文档/差异检查通过；5 测试文件/36 函数原样保留、未运行，无夹具适配。目标库只读元数据/聚合实施前后相同，无数据库写入；没有真实创建/读取、认证/HTTP 或故障回归。提交轮 Go 源码未改，沿用实施轮 vet/build，重新核对源码/文档及 12 个精确暂存路径后提交为 `2f315e8`，未推送，范围与缺口见[开发计划 R4-B1](./DEVELOPMENT_PLAN.md#r4-b1草稿创建与读取已提交)。

[R4-B2 共享媒体规则/本地存储](./DEVELOPMENT_PLAN.md#r4-b2共享媒体规则与本地存储归层已提交) 已提交为 `e56d7bf`，未推送：[Domain 媒体](../backend/internal/domain/video/media.go)持有独立媒体值/小能力端口与唯一共享规则，[LocalStorage](../backend/internal/infra/storage/media/local.go)/[已存储媒体校验](../backend/internal/infra/storage/media/validation.go)持有唯一文件实现。[Video 外层适配](../backend/internal/infra/persistence/video/media_storage.go)为删除/枚举与 Worker 转换旧错误身份和完整 cause 链；[旧媒体边界](../backend/internal/video/storage.go)只留原媒体值/接口/错误及标量规则桥接。视频/封面上传、Account 头像、Worker 和三类媒体清扫均接同一新实现；B2 实施时原上传 HTTP/用例和绑定事务、Account 保存写库补偿、Worker 拒绝/重试/ACK、Sweeper 用例/租约/SQL/调度保持原样。仅必要装配变更，一个 worker 夹具仅两项导入/一处构造器，断言不变；vet/build 与源码检查通过，未运行 Go 测试或真实上传、路径安全、头像补偿、Worker/Sweeper/HTTP 回归。B3/B4 当前实施结果见下节。

### 3.8 草稿视频/封面上传：保存后才绑定

两个原端点 `POST /api/video/auth/drafts/:id/play` 与 `POST /api/video/auth/drafts/:id/cover` 经原 JWT/session → [DraftMediaUploadHandler](../backend/internal/interfaces/http/video/draft_media_upload.go) → [Application 媒体上传](../backend/internal/application/video/draft_media_upload.go) → [Domain 绑定规则/小端口](../backend/internal/domain/video/draft_media.go) → [绑定适配](../backend/internal/infra/persistence/video/draft_media_binder.go) → 原 [Repository.UpdateDraftMedia](../backend/internal/video/video_repo.go)。两个 wrapper 只选 kind 与原 play_*/cover_* 字段；HTTP 共用认证/路径、MaxBytesReader/FormFile、视频 200 MiB/封面 10 MiB + 各 1 MiB、前 512 字节 ReadFull、原扩展名/文件头和 Seek，将打开的文件交给用例。

Application 先经 B2 唯一 LocalStorage 保存，再计算展示名；之后才校验原 ID/kind/所属 URL/存储名、检查绑定端口和原名兜底，最后单次委托原锁行事务。绑定失败只有 storage 提供 Remove 时尽力清理，忽略删除错误并返回原绑定错误；保存失败不绑定/删除。nil 接口/接口内 nil 指针和可选删除保持，不增加草稿预读、提前仓储检查、写后重读或文件重读。正常装配仍会话、锁行 First、Save 各一次，源码三次 CRUD，不含事务控制语句，未运行预算验证。

响应仍 201 四个顶层 draft_id 与原 play_* 或 cover_*，展示名响应不回填写库兜底，全部字段始终输出。原 400/401/403/404/409/413/500 分类/文案保持；multipart/大小为 413，存储返回 MediaTooLarge 仍通用 400，未知故障 500 video operation failed。B4 只将 B3 两个上传文件/类型改为共享 DraftMediaUpload 并接入 cover，完整视频用例/前缀逐段对照不变；确认全部生产/测试引用后删除旧封面 wrapper/helper、旧媒体 Service 方法/保存适配/端口与四个标量上传桥接。旧发布/丢弃/删除、完整仓储/SQL/ORM、仍用媒体值/错误/删除枚举适配及无关助手保留；Account/Worker/Sweeper/存储实际实现未改。

R4-B3 已提交为 `6bc4926`，未推送；R4-B4 已提交为 `fefc4c4`，未推送。B4 实施轮 vet/build、60 项源码对照、52 个内层文件/10 包依赖、270 个保护跟踪文件、文档/差异检查通过；全部 5 个测试文件/36 个测试函数原样，无夹具适配，未运行。目标 localhost:3306 再次拒绝连接，真实元数据核对未完成；无 SELECT/数据库写入或服务启动，没有真实上传/补偿/路径安全/Worker/Sweeper/HTTP/预算/并发/文件或数据库故障回归。边界与缺口见[开发计划 R4-B3](./DEVELOPMENT_PLAN.md#r4-b3草稿视频上传已提交)与[R4-B4](./DEVELOPMENT_PLAN.md#r4-b4草稿封面上传已提交)，随后用户要求继续 R4-C1，见下节。

取消 Sweeper 与请求内立即删除已纳入[后续行为变更计划](./DEVELOPMENT_PLAN.md#取消-sweeper-与请求内立即删除已纳入计划未实施)，尚未实施；本文的清扫/软删除/purging 链路仍描述当前源码。原清扫归层工作暂缓，发布/上传及后台处理不在本轮改变回收策略。

### 3.9 草稿发布：受理状态与事件同事务

`POST /api/video/auth/drafts/:id/publish` 经原 JWT/session → [DraftPublicationHandler](../backend/internal/interfaces/http/video/draft_publication.go) → [Application 发布](../backend/internal/application/video/draft_publication.go) → [Domain 单方法 DraftPublisher](../backend/internal/domain/video/draft_publication.go) → [发布适配](../backend/internal/infra/persistence/video/draft_publication.go) → 原 [Repository.UpdateDraftPublication](../backend/internal/video/video_repo.go)。认证后仍最多读一字节请求体，原 EOF/拒绝条件不变；再解析路径，执行 ID/端口可用校验，一次原子写后复用 DraftItemFrom/原 DTO 组装 202 draft 响应。

原事务仍锁行→作者→draft→六个媒体字段→原时间→processing CAS→同事务 pending video.process Outbox，UUID、RowsAffected、回滚/重复发布语义保持。没有前置读草稿、写后重读、文件/作者/互动读取或直接 MQ 发送。正常装配源码预算仍一条会话加三条事务内 CRUD，共四次，不计 BEGIN/COMMIT，未运行验证；202 只代表已受理，Worker 后续校验/CAS 发布不变。

响应仍十字段草稿 DTO、原 JSON/omitempty，无媒体 URL/物理名/新增时间字段；原 400/401/403/404/409/500 分类、文案和错误 cause 保持。只删除被替代的旧发布 HTTP/Service 方法和两个无用途导入，旧丢弃/删除与共享 helper、完整 Repository/唯一 ORM、上传/存储、Account、Worker/Sweeper 均保留。

C1 vet/build、57 项源码、54 个内层文件/10 包、274 个既有工作文件及文档/差异检查通过；五个测试文件/36 个测试函数原样，无夹具适配、未运行。目标 localhost:3306 拒绝连接，真实元数据未核对；无 SELECT/数据库写入或服务启动，没有真实发布/HTTP/认证/并发/事务/Outbox 故障/预算/Worker 回归。B4/C1 已分别提交为 `fefc4c4`/`9f0a393`，未推送；本轮仅继续 R4-C2，sweeper 取消实现尚未开始；完整边界见[开发计划 R4-C1](./DEVELOPMENT_PLAN.md#r4-c1草稿发布已提交)。

## 4. Timeline：一次 Feed 请求怎样完成

先沿首屏直读 MySQL 的路径理解基础链路，再阅读第 6 节的续页缓存分支；当前没有关闭缓存的配置开关。

### 4.1 从页面一路追到 SQL

```text
FeedView.vue
  → usePublishedFeed.loadFirstPage / loadMore
  → listTimelineFeed
  → GET /api/feed?scene=timeline&limit=12&cursor=...
  → Handler.GetFeed
  → Service.GetFeed
  → readTimelinePage
  → Repository.ListTimelinePage
  → video.Repository.GetPublishedVideoList
  → 截断 limit+1 条记录
  → assembleFeedItems：批量统计 + 批量作者
  → feedItemsResponseFromResult
```

源码入口：[前端 API](../frontend/src/features/video/api.ts) → [HTTP Handler](../backend/internal/interfaces/http/feed/handler.go) → [应用 Service](../backend/internal/application/feed/service.go) → [SQL 适配器](../backend/internal/infra/persistence/feed/legacy_reader.go) → [视频仓储](../backend/internal/video/video_repo.go)。

Handler 只允许 `scene`、`limit`、`cursor` 三个单值参数，未知或重复参数会失败。缺省场景是 Timeline；后端缺省 `limit=20`，上限 50；首页显式传 12。`author_id` 不属于 Feed 接口，作者主页继续使用旧 `/api/video?author_id=...`。

### 4.2 分页采用 keyset

仓储按 `published_at DESC, id DESC` 排序。后续页的定位条件可概括为：

```sql
-- 示意条件；公开过滤由 PublicVideoQuery 追加
WHERE published_at < :last_time
   OR (published_at = :last_time AND id < :last_id)
ORDER BY published_at DESC, id DESC
LIMIT :limit_plus_one;
```

例如同一时刻的 ID 为 `105、104、103`，页大小为 2：查询得到 3 条，返回 `105、104`，游标绑定 `104`；下一页才从 `103` 继续。ID 是同时间记录的稳定排序补充。

多查一条用于判断是否还有下一页，避免另外执行总数查询。**探测记录参与页缓存与可见性校验，但不参与最终页的作者和互动批量读取。**

[cursor.go](../backend/internal/application/feed/cursor.go) 将版本、场景、排序版本和位置编码为 Base64URL JSON。它严格检查长度、字段与尾随 JSON；Feed 游标不能跨用到旧视频接口。

Base64 是编码，当前游标没有签名。服务端仍须检查范围、场景和公开条件，不能把游标当权限凭证。keyset 也不是冻结快照：分页过程中后发布完成、删除或关系变化的记录，可能改变可读取集合。

### 4.3 读模型分成三个部分

| 读模型                | 内容                           | 为什么这样拆                           |
| --------------------- | ------------------------------ | -------------------------------------- |
| `FeedPageItem`        | 视频 ID、作者 ID、发布时间     | 描述排序位置与一页成员，适合轻量页缓存 |
| `FeedCard`            | 标题、描述、媒体地址和文件名等 | 展示正文相对稳定，可独立按视频 ID 缓存 |
| `Author` / `FeedStat` | 作者资料、点赞数、评论数       | 经常变化，当前保持实时批量读取         |

`assembleFeedItems` 先校验条目与卡片的 ID、作者、发布时间一致，再对截断后的 ID 去重，调用 `BatchGetStats` 与 `BatchGetAuthors`，最后保持页条目的顺序组装。

在非空、直读 MySQL 的基础 Timeline 路径，源码对应约 4 条 SELECT：视频页 1 条、点赞聚合 1 条、评论聚合 1 条、作者 1 条。它解决逐视频/逐作者查询的 N+1 问题；**固定查询次数不代表固定扫描量或已经证明性能收益。**

## 5. Following：关注关系如何变成视频流

请求入口仍是 `/api/feed`，但 `scene=following` 进入独立分支。

```mermaid
flowchart LR
    Req["Following 请求<br/>响应使用私有缓存头"] --> Auth["JWT 与活动 session<br/>观看者与游标检查"]
    Auth --> SQL["MySQL JOIN<br/>当前关注、活动作者、公开视频"]
    SQL --> Assemble["limit+1 探测与截断<br/>批量组装并生成关注流游标"]
```

源码入口：[following.go](../backend/internal/application/feed/following.go) → [following_reader.go](../backend/internal/infra/persistence/feed/following_reader.go) → [following_repo.go](../backend/internal/video/following_repo.go)。

### 5.1 关注流查的是当前关系

查询同时 JOIN：

- `user_follows`：`follower_id = viewerID`，`followee_id = videos.author_id`。
- `users`：作者仍未软删除。
- `videos`：完整公开条件和 keyset 位置。

因此新关注一个作者后可以读到该作者较早的视频；取关会影响后续请求。它不是只保留“关注以后新发的视频”的收件箱，也没有跨页关系快照保证。

### 5.2 观看者隔离贯穿后端与前端

Following 游标额外绑定 `viewer_id`。这个身份来自认证上下文，不能由查询参数指定。游标还校验规范 Base64URL，拒绝其他用户、其他场景或旧视频接口的游标。

`GetFeed` 在进入 Timeline 缓存逻辑之前分流到 `getFollowingFeed`，所以 Following **不使用 Timeline 页缓存、卡片缓存，也不占用它们的 32 个读取名额**。这不表示 Following 已有独立的全局并发保护。

认证响应使用 `Cache-Control: private, no-store` 和 `Vary: Authorization`。前端为两个场景保存独立游标，账户退出或切换后会清空状态。

非空 Following 正常路径约 6 条 SELECT：活动 session、活动观看者、视频 JOIN、两条统计聚合、作者批量查询；空页通常约 3 条。认证查询故障可能在 JWT 中间件被归为 401，应用读取失败通常映射为 503，不能假设所有 MySQL 故障都有同一个 HTTP 状态。

## 6. 多层缓存：页缓存、卡片缓存与实时数据

### 6.1 这里的“两层”分别缓存不同对象

当前没有实现进程内 LRU + Redis 的 L1/L2 数据缓存。可以把已有方案理解为**页条目缓存 + 视频基础卡片缓存**，两者都存 Redis，再搭配 MySQL 的实时事实读取。

| 层 / 数据  | 缓存内容                                                   | Key / 读取路径                                            | 默认约束                             |
| ---------- | ---------------------------------------------------------- | --------------------------------------------------------- | ------------------------------------ |
| 页缓存     | 有序 `FeedPageItem`，最多 `limit+1` 条；不存完整 HTTP 响应 | `gofeed:feed:page:v1:timeline:s1:l<limit>:<UTC时间>:<id>` | TTL 30 秒；操作 100ms；载荷 16 KiB   |
| 卡片缓存   | 单个 `FeedCard`；不包含作者详情和互动计数                  | `gofeed:feed:card:v1:<video_id>`                          | TTL 30 秒；操作 100ms；单卡片 16 KiB |
| 公开状态   | 当前可见 ID、作者 ID、发布时间                             | 每次缓存卡片读取前批量查 MySQL                            | 使用完整公开作用域                   |
| 作者与计数 | 当前资料与关系表聚合                                       | 最终页实时批量查 MySQL                                    | 不进入上述缓存                       |

源码入口：[页缓存应用编排](../backend/internal/application/feed/timeline_cache.go) · [缓存卡片读取](../backend/internal/application/feed/cached_card_reader.go) · [Redis 页适配](../backend/internal/infra/cache/feed/page_cache.go) · [Redis 卡片适配](../backend/internal/infra/cache/feed/card_cache.go)。

### 6.2 完整读取分支

```mermaid
flowchart TD
    Req["Timeline 请求"] --> Cache{"页缓存构造成功？"}
    Cache -->|否| SQL["按原游标查询完整 MySQL 页"]
    Cache -->|是| Slot{"32 个读取名额有空位？"}
    Slot -->|否| Busy["返回 503"]
    Slot -->|是| Page{"续页且页缓存有效命中？"}
    Page -->|首屏、未命中或故障| SQL
    Page -->|是| Cards["读取当前公开卡片<br/>细节见下图"]
    Cards --> Match{"整页含探测项均匹配？"}
    Match -->|否| SQL
    Match -->|是| Join["截断最终页；实时批量作者与计数"]
    SQL --> Fill["符合条件的续页短超时同步回填"]
    Fill --> Join
```

API 直接构造页/卡片缓存：页缓存构造失败时使用 MySQL 基础路径；只有卡片缓存构造失败时，页缓存仍保留，卡片按公开规则批量回源。图中的判断是组件构造结果，不对应环境变量开关。页缓存有效命中的卡片读取如下：

```mermaid
flowchart TD
    Cards{"卡片缓存构造成功？"} -->|否| Full["MySQL 批量读取完整公开卡片"]
    Cards -->|是| States["MySQL 批量读取当前公开状态"]
    States --> RedisCards["Redis 批量读仍公开的卡片"]
    RedisCards --> Missing["缺失或不匹配卡片批量回源<br/>允许时回填"]
    Full --> Match["返回外层：检查整页含探测项是否匹配"]
    Missing --> Match
```

图中的数据库步骤失败会返回错误，不会使用旧缓存掩盖。首屏和未装配缓存的基础路径不回填页缓存；当前正常装配时，首屏仍受 32 个 Timeline 读取名额限制。

### 6.3 为什么缓存命中还查 MySQL

Redis 里可能保留已删除视频或旧卡片。`CachedCardReader.BatchGetCards` 先查当前公开状态，只对仍公开的 ID 读取缓存，然后检查卡片与当前 ID、作者、发布时间一致。

页缓存命中后还会检查**整页及最后的探测记录**。只要成员缺失或排序字段不匹配，`readTimelinePage` 就按原游标整页回源，不是简单删除坏成员后凑一页。这样可以重新得到正确的页长度和下一页判断。

卡片缓存坏值或部分缺失则先只批量回源缺失卡片；如果最后仍不能匹配页条目，外层再整页回源。统计和作者继续实时读取，所以点一个赞不会因为卡片 TTL 而固定住计数。

这套检查也允许处理“删除后迟到的缓存写入”：旧值即使再次进入 Redis，后续请求仍通过 MySQL 公开状态挡住它。它不是对所有并发时刻的线性一致承诺，状态检查之后仍可能发生删除；一次请求内的多次 SQL 也没有包在统一快照事务中。

页校验只能确认缓存里的成员仍有效，不能证明缓存页已经包含所有新变得可见的成员。尤其视频的排序时间先于处理完成时间，后来完成的旧位置视频可能要等页缓存过期或重新读取后才能出现。

### 6.4 回填与容量限制的细节

- 缓存正常未命中、坏载荷或过期成员：允许同步、短超时回填。
- Redis 读取故障或缓存操作名额耗尽：直接回源，并跳过本次回填，减少故障期额外操作。
- 回填失败：记录观测，成功的 MySQL 结果仍可返回；请求已取消时遵循取消语义。
- 没有启动脱离请求的后台回填 goroutine。
- 页缓存和卡片缓存共享每个服务实例的 **16 个缓存操作名额**；满额时跳过缓存。
- 装配页缓存后，每个服务实例最多 **32 个 Timeline 读取请求**；满额快速返回 503。
- 卡片批量最多 51 个有效 ID。Lua 在 GET 返回字符串前检查长度，超大卡片跳过；页载荷在读取后的解码阶段检查大小，这两种边界不同。

这些是进程内容量约束，不是集群全局限额，也不是同一个缓存 key 的请求合并；当前没有 `singleflight` 回源合并。

### 6.5 缓存收益要怎样理解

页缓存减少重复排序取页，卡片缓存减少重复读取展示字段。它们仍保留公开状态、作者与统计 SQL；卡片未命中还可能增加一次完整卡片读取。因此不能把“缓存命中”推导成“零 SQL”，也不能只凭命中率证明整体更快。

比较时应区分首屏/续页、页缓存/卡片命中、冷缓存/热缓存，并一起看 SQL 次数、扫描量、返回字节、延迟和故障回源压力。当前缓存直接装配，收益与容量仍待测量。

## 7. 发布链路：事务、Outbox、消费与预热

### 7.1 视频状态机

```mermaid
stateDiagram-v2
    [*] --> draft: 创建草稿并上传媒体
    draft --> processing: 发布事务提交；写 video.process
    processing --> published: worker 媒体校验通过；CAS
    processing --> rejected: 媒体校验失败；记录原因和时间
    draft --> purging: 显式丢弃或保留期到期
    rejected --> purging: 显式丢弃或保留期到期
    purging --> [*]: 删除媒体、保存检查点、硬删除记录
```

已发布视频的删除采用软删除及后续清扫，是另一条路径，不是这里的草稿 `purging` 流转。

### 7.2 API 接受与实际完成分开

发布接口为 `POST /api/video/auth/drafts/:id/publish`。沿 [发布 HTTP](../backend/internal/interfaces/http/video/draft_publication.go) → [Application 发布](../backend/internal/application/video/draft_publication.go) → [Domain DraftPublisher](../backend/internal/domain/video/draft_publication.go) → [Infrastructure 适配](../backend/internal/infra/persistence/video/draft_publication.go) → 原 [UpdateDraftPublication](../backend/internal/video/video_repo.go) 阅读：

1. HTTP 按原顺序检查认证、至多一字节请求体与路径；用例检查 ID 和发布端口。
2. MySQL 事务内先锁定草稿，再复核作者、draft 状态和六个媒体字段，条件更新 `draft → processing` 并写 `published_at`。
3. 同一事务创建 UUID 标识、pending 状态的 `video.process` Outbox 事件。
4. 提交成功后复用原事务结果与草稿 DTO 返回 **202**，表示接受异步处理，不重读视频。

API 不直接投递 RabbitMQ。MQ 故障时，只要 MySQL 事务成功，请求仍可接受；视频保持处理中，等待 worker 后续恢复。MySQL 事务失败则不能承诺发布已被接受。

```mermaid
sequenceDiagram
    participant C as 发布页面
    participant A as API
    participant D as MySQL
    participant R as Relay
    participant Q as RabbitMQ
    participant W as 视频消费者
    C->>A: 发布草稿
    A->>D: 同事务：draft→processing + video.process
    D-->>A: 提交成功
    A-->>C: 202，processing
    R->>D: claim Outbox；持有租约
    R->>Q: 发布；等待 confirm
    Q-->>R: 确认发布
    R->>D: 围栏下标记 dispatched
    Q->>W: 投递 video.process
    W->>W: 校验本地视频与封面
    W->>D: CAS：processing→published
    Note over W,D: 实际 CAS 变更时，同事务写 video.published
    W->>Q: 状态处理成功后 ACK
```

视频消费者当前执行路径、大小、扩展名和文件头校验，见 [已存储媒体校验](../backend/internal/infra/storage/media/validation.go)。它还不是视频转码或内容审核流水线。

当前 worker 直接装配发布事件与预热消费，已提交的 `video.published` 继续走独立的派生链路：

```mermaid
sequenceDiagram
    participant R as Relay
    participant D as MySQL
    participant Q as RabbitMQ
    participant F as 预热消费者
    participant K as Redis
    R->>D: claim video.published
    R->>Q: 发布并等待确认
    Q-->>R: confirm；没有 Return
    R->>D: 标记 dispatched
    Q->>F: feed.card.warm 投递
    F->>D: 读取当前公开卡片
    D-->>F: 当前卡片或不可见
    F->>K: 写卡片或删除精确 key
    K-->>F: 操作结果
    F->>Q: 成功后 ACK
```

### 7.3 Relay 的派发状态不是业务完成状态

`video_outbox_events` 的状态为 `pending → publishing → dispatched`。失败后回到 `pending` 并设置下一次尝试时间；租约到期的 `publishing` 可被接管。

`ClaimPendingOutboxEvents` 用短事务与 `FOR UPDATE SKIP LOCKED` 限批 claim。每次 claim 增加 `attempt`，后续标记派发或释放重试同时校验 `attempt` 与租约时间。**不要在数据库事务中等待 MQ 网络确认。**

Relay 默认每 2 秒一轮、每轮最多 32 条、租约 30 秒。发布失败指数退避至多 5 分钟；不支持的事件类型或不一致快照固定退避，避免热循环。源码见 [outbox_repo.go](../backend/internal/video/outbox_repo.go) 与 [worker.go](../backend/internal/worker/worker.go)。

普通派发确认后才标记 `dispatched`；被接管的 `video.process` 若已观察到对应视频为 `published/rejected`，路由可以按已完成结果收口。**`dispatched` 不是视频处理成功或 Redis 预热完成的消费水位。**

### 7.4 消息与队列边界

| 事件              | 载荷核心内容                                            | 消费队列 / QoS       | 消费职责                                   |
| ----------------- | ------------------------------------------------------- | -------------------- | ------------------------------------------ |
| `video.process`   | `schema_version`、`event_id`、`video_id`、视频/封面路径 | `video.process` / 16 | 校验媒体，CAS 为 `published` 或 `rejected` |
| `video.published` | `schema_version`、`event_id`、`video_id`                | `feed.card.warm` / 4 | 重读 MySQL 当前公开卡片，预热或删除缓存键  |

交换机为 `gofeed.events`；每个消费者都有独立的 `1s/5s/30s` 重试队列及 `<queue>.dead` 死信队列。TTL 到期经死信路由回主队列，不需要延迟消息插件。QoS 是未确认投递的预取上限，不能直接当作并行 worker 数量。

源码入口：[mq.go](../backend/internal/mq/mq.go) · [feed_spec.go](../backend/internal/mq/feed_spec.go) · [relay_route.go](../backend/internal/worker/relay_route.go) · [feed_card_warm.go](../backend/internal/worker/feed_card_warm.go)。

### 7.5 为什么允许重复消息

消息已经进 MQ，但 Relay 在确认或写回 `dispatched` 前断线，后续租约接管可能再次投递。消费者已经提交状态，但 ACK 丢失，也可能再次收到。

当前以“至少一次投递 + 幂等业务处理”应对：

- 视频状态只允许从 `processing` 条件更新；重复处理不会再次完成状态变更。
- 当前 worker 中，`processing → published` 与新增 `video.published` 在同一事务；CAS 未变更不新增事件。
- 卡片预热每次读取当前事实并覆盖同一 key，不依赖旧消息里的展示内容。
- 暂态失败先发布下一档重试消息，收到 broker 确认后才 ACK 原消息。
- 坏载荷、未知版本或消费重试耗尽进入 DLQ；DLQ 需要受控处理，不会自动变成成功。

这不是端到端“恰好一次”。UUID 唯一事件键也不能消除网络中重复投递的可能性。

## 8. 抗风险亮点：降级、隔离、围栏与恢复

### 8.1 Redis 的故障恢复由 Runtime 管理

[middleware/cache/runtime.go](../backend/internal/middleware/cache/runtime.go) 保存连接健康、探测、关闭及下一次重试时间。默认冷却 1 秒；恢复由后续操作触发，而不是后台定时重连任务。

```mermaid
stateDiagram-v2
    [*] --> Unavailable: Runtime 创建，尚未连接
    Unavailable --> Probing: 首次操作或冷却结束
    Probing --> Healthy: 建连或 Ping 成功
    Probing --> Cooldown: 探测失败
    Healthy --> Cooldown: 可识别的连接故障
    Cooldown --> Probing: 冷却结束后的一个调用者
    Probing --> Probing: 其他调用者快速得到不可用结果
    Healthy --> Closed: Close
    Cooldown --> Closed: Close
    Probing --> Closed: Close
    Unavailable --> Closed: Close
```

它具备类似熔断与半开探测的作用，但源码没有通用熔断器的失败率窗口。`redis.Nil` 是未命中；Redis 服务端命令错误或当前请求取消，不应一律被当成断线并触发全局冷却。

限流与 Feed 使用不同 Runtime，隔离连接池和故障状态；页缓存与卡片共享一个 Feed Runtime。它们仍可能连接同一 Redis 实例，不能把这种进程内隔离描述成基础设施故障隔离。

### 8.2 RabbitMQ 恢复包括拓扑和信道

[mq/runtime.go](../backend/internal/mq/runtime.go) 在后续发布/取消费信道时发现连接失效，会重建连接、声明完整消费拓扑，并重新创建确认发布器；消费循环重新取得消费信道。旧发布器不能继续绑定已经失效的连接。

当前视频 MQ Runtime 首次连接及每次重连都声明视频处理与卡片预热两个规格，独立互动 Runtime 声明热度规格；两个入口装配都显式开启 mandatory/Return 检查。publisher confirm 表示 broker 确认发布，mandatory/Return 用于检测无法路由的消息，两者都不证明消费完成。Runtime 构造默认值仍是 `mandatory=false`，阅读时应以 worker 的实际装配为准。

worker 启动期连接失败有有限次数退避，耗尽会退出；运行期恢复也依赖持续调用与可用依赖。“自动恢复”不表示无限等待、无故障损失或无积压。

### 8.3 围栏处理旧持有者迟到

| 场景        | 围栏条件                                | 防止的问题                                         |
| ----------- | --------------------------------------- | -------------------------------------------------- |
| Outbox 派发 | `status + attempt + locked_until`       | 租约被新 Relay 接管后，旧 Relay 不能覆盖新派发状态 |
| 视频处理    | `status = processing` 的 CAS            | 重复消息再次发布视频或重复产生发布事件             |
| 草稿清扫    | 随机 `purge_token + lease`              | 失去租约的 sweeper 继续写检查点或删除记录          |
| 前端请求    | 场景 `generation + controller + cursor` | 迟到首屏/续页覆盖用户已经切换后的列表              |

租约主要协调当前持有者，不能阻止旧持有者已经发出的网络请求晚到。正确性仍需要状态围栏、可重复处理以及不可复用的媒体对象路径共同支持。

### 8.4 故障场景速查

| 触发条件                         | 当前处理                                | 代价 / 边界                           | 对应源码                                                                                                                                           |
| -------------------------------- | --------------------------------------- | ------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| Redis 不可用                     | Timeline 回源；注册/登录 fail-open      | MySQL 压力可能上升；限流暂时失效      | [timeline_cache.go](../backend/internal/application/feed/timeline_cache.go)、[ratelimit.go](../backend/internal/middleware/ratelimit/ratelimit.go) |
| 缓存坏值、视频删除、排序字段变更 | 校验卡片/整页，必要时整页回源           | 仍依赖 MySQL；没有全请求快照一致性    | [cached_card_reader.go](../backend/internal/application/feed/cached_card_reader.go)                                                                |
| Feed 缓存读取名额耗尽            | Timeline 快速返回 503                   | 当前保护只在装配页缓存时生效          | [service.go](../backend/internal/application/feed/service.go)                                                                                      |
| 缓存操作名额耗尽                 | 跳过缓存，读取 MySQL                    | 16 是单实例操作容量                   | [timeline_cache.go](../backend/internal/application/feed/timeline_cache.go)                                                                        |
| 发布时 MQ 不可用                 | 已提交 Outbox 保留，恢复后派发          | 视频暂时停在 processing，需要观测积压 | [worker.go](../backend/internal/worker/worker.go)                                                                                                  |
| Relay 中途退出                   | publishing 租约到期可接管               | 允许重复投递；需幂等消费者            | [outbox_repo.go](../backend/internal/video/outbox_repo.go)                                                                                         |
| 消费暂态失败                     | 确认重试副本后 ACK；最多三档重试        | 耗尽进入 DLQ，需要处置与重放          | [worker.go](../backend/internal/worker/worker.go)                                                                                                  |
| 媒体校验失败                     | CAS 为 rejected，保存原因及时间         | 已接受不等于最终发布成功              | [已存储媒体校验](../backend/internal/infra/storage/media/validation.go)                                                                               |
| 清扫一半失败                     | 保留 purging 与媒体检查点，后续接管重试 | 不把部分删除对象恢复成可发布草稿      | [draft_purge.go](../backend/internal/sweeper/draft_purge.go)                                                                                       |
| MySQL 不可用                     | 就绪检查 503；业务按各自错误映射失败    | 不靠缓存伪造当前业务事实              | [http.go](../backend/internal/observability/http.go)                                                                                               |

### 8.5 默认装配与回退边界

API 直接装配页缓存和卡片缓存，四种互动写入直接使用同事务事实存储；worker 直接装配发布事件/预热、互动 Relay、热度消费及队列观测。七项布尔配置与环境变量入口已经删除，旧配置副本中的同名项不再影响行为。热度窗口、权重、代际与容量继续使用原参数，不新增开关或配置字段。

Redis 缓存失败仍回源 MySQL。计划性回退使用对应代码版本，并保留认识已有事件类型的消费者处理存量；不能删除事实表或伪造派发/消费完成。启动前须应用迁移 `000010_interaction_outbox`。配置包只读取热度参数；[worker/main.go](../backend/cmd/worker/main.go) 的 `startWorkers` 在时间单位转换前检查整数溢出，热度索引构造时统一调用 [HeatPolicy.Validate](../backend/internal/domain/feed/heat.go) 校验窗口、去重保留、权重及容量。入口参照 GCFeed 集中装配视频处理、卡片预热、互动 Relay、热度索引/投影器/消费者及观测循环；主函数处理配置加载、连接和关闭信号，收到取消后调用返回的收尾函数，等待循环退出再关闭资源。

组件装配、队列声明与进程启动都不能证明窗口覆盖完整，Hot/Recommend 仍为 501。R1-A/R1-B1 均未执行部署或业务库迁移，也未作热度窗口完整性验收。

## 9. 账户、互动与媒体清扫

### 9.1 JWT 与数据库会话共同决定认证

[Interfaces HTTP Auth](../backend/internal/interfaces/http/auth/jwt.go) 先检查 Authorization 格式，再经 [Infra JWT](../backend/internal/infra/jwt/jwt.go) 解析 Bearer JWT，最后通过消费方 SessionValidator 的 Validate 用 `session_id + user_id` 校验数据库活动会话。保留原认证顺序、401 文案和三个 Gin 上下文键；nil 校验依赖及接口内的 nil 指针继续拒绝认证。JWT 签名有效不等于会话仍有效。

[Account 会话用例](../backend/internal/application/account/session_lifecycle.go)经小端口生成与哈希令牌，[会话仓储](../backend/internal/infra/persistence/account/session_repository.go)只持久化刷新令牌 SHA-256 hex；刷新用预期旧哈希做 CAS，避免同一个旧刷新令牌被重复轮换。七天到期保持固定，轮换后再读账户/签发；失败残留与尽力撤销保留。退出仅撤销当前会话，改密/注销的全部会话撤销仍与账户变更使用同一事务。

Following 另外检查观看者有效。匿名 Timeline 不会因携带 token 而自动返回“我是否点赞”等个性化字段；当前 DTO 返回的是公共作者与统计。

三个匿名账户 GET 从 [Account Handler](../backend/internal/interfaces/http/account/handler.go) → [Application](../backend/internal/application/account/service.go) → [Domain 读取/统计端口](../backend/internal/domain/account/repository.go) → [Infrastructure](../backend/internal/infra/persistence/account/legacy_reader.go) 读取。公开账户、资料统计和 ID 位置不携带 ORM/HTTP 标签；内层只依赖标准库及独立领域模型，User ORM 与 GORM 错误仅在 Infrastructure 转换，分页位置直接使用 Domain ListPosition。Interaction 直接返回现有 Domain Account ProfileMetrics，账户统计/视频数适配仅通过领域小接口委托，并保留原 accountError 与可选依赖 nil 语义。

列表没有 limit/cursor 时继续复用 Account Repository 全量读取；任一参数存在即分页，Gin GetQuery 保留空值存在性，`?cursor=` 使用默认 20，`?limit=`/0/超出 1–50 先报 limit 错误。Application 保留 [v1 编解码](../backend/internal/application/account/cursor.go) 的 RawURLEncoding、v/k/i、version=1、kind=users、非零 ID 及原字段检查，再将独立位置直接交给 Account 仓储。原 SQL 的 id ASC/id > ID 不变，多读一条，以实际返回末条 ID 续页；[DTO](../backend/internal/interfaces/http/account/dto.go) 保持 user/users/account、资料 omitempty、四项零值统计、空数组和末页省略游标。

资料按账户→完整公开视频数→获赞→粉丝→关注读取并立即传播失败。视频计数继续调用 video.Repository 的 PublicVideoQuery，Interaction 资料适配器实现 Domain Account 统计端口；正常装配共五条 SQL，统计部分三条，未注入统计仍返回零值。旧读取 Controller/Service、pagination.go 与无用途读取类型已删除；头像改由 Account 小 Reader 读取，原夹具 Service.GetByID 已删除，保留夹具直接调用 Account Repository.GetByID，其他共享响应保留，R3-C 已删除无用途 publicUser/旧登录响应，会话 DTO 归 Account。User ORM 已迁 Account、字段/标签不变，唯一 AuthSession ORM/仓储已归 Account；仓储 GetByIDs SQL 未变。现有保留流程涉及详情/注销 404 和资料头像，本轮未运行；账户旧 v1 为迁移前后源码兼容证据，固定旧账户 v1 续页、资料预算和逐步失败尚无持续断言，不能用关系列表 v1 代替，详见[开发计划第 6.9 节](./DEVELOPMENT_PLAN.md#69-r3-a-三个匿名账户读取已提交)。

Video 与 Feed 共用 [Account AuthorReader](../backend/internal/infra/persistence/account/legacy_author_reader.go)，旧 Video 输出转换仅在 Infrastructure。它通过独立 PublicAccountReader 读取 PublicAccount；单个不存在/注销账户返回原 ID 和“已注销用户”，其他错误传播。批量将 0 放入占位结果且不查询，非零 ID 按首次出现顺序去重，空批次不查库，非空批次只调用一次原 Account Repository.GetByIDs，保留 id/username/avatar_url 与软删除过滤。Reader 过滤原 nil 行，作者适配补缺失/注销占位，数据库故障返回失败；不逐作者查询或增加预读/重读。R3-F1 删除旧 video/user_author_reader.go 与 user 统计接口/结果，当时保留 User 仓储/ORM/夹具及仍用错误；R3-F2 已迁 User 仓储/ORM 并清理旧包。R3-F1 的 5 个测试文件未改，vet/build 与源码检查通过；没有真实作者读取、资料统计或 HTTP 回归，见[开发计划 R3-F1](./DEVELOPMENT_PLAN.md#r3-f1账户跨模块读适配已提交)。

注册走 [Account RegistrationHandler](../backend/internal/interfaces/http/account/registration.go) → [RegistrationService](../backend/internal/application/account/registration.go) → [Domain 注册规则/输入](../backend/internal/domain/account/registration.go) → [Creator](../backend/internal/infra/persistence/account/legacy_creator.go) → 原 Account Repository.Create。它与三个匿名读取复用 Account 包和公开 DTO，独立装配注册依赖。限流仍先执行；ShouldBindJSON 与原 required/min/max 标签先按 rune 校验，成功后才对用户名 TrimSpace 并按 Go len 字节长度检查 3–32，密码不 Trim、按字节检查 8–72。Application 的小 Hash 端口由 [BcryptPasswordHasher](../backend/internal/infra/persistence/account/password_hasher.go) 使用 GenerateFromPassword/DefaultCost 实现，始终先哈希再 Create，包括重名请求。

创建适配器只将独立 CreateInput 转为 Account Persistence User，复用迁入仓储的唯一键/1062 处理并直接返回现有领域错误；不预查重、不重读、不增加 SQL 或外层事务。大小写语义和软删除用户名占用不变。返回 201 + user 包装及原公开字段，avatar_url/bio 仍 omitempty；不返回密码/软删除字段，不创建会话/令牌。400/409/500 文案及原 Redis Key、5 次/小时、429/Retry-After/fail-open 均保留。确认引用后已删除旧注册 Controller/Service 方法与 CreateRequest；领域 ErrUsernameTaken/ErrInvalidInput、仓储 GetByUsername 及唯一 User ORM 保留；旧测试夹具 Service.GetByID 由 R3-F2 清理，夹具直接用 Repository 读取；改名规则已归 Domain，bcrypt 由 Account Infrastructure 复用。以上为源码兼容证据，R3-B 实施轮完成 vet/build 与差异检查；提交轮代码未变，沿用该结果，没有运行 Go 测试或真实注册回归，剩余专项见[开发计划第 6.10 节](./DEVELOPMENT_PLAN.md#610-r3-b-注册接口已提交)。

登录、刷新和退出走 [Account SessionHandler](../backend/internal/interfaces/http/account/session.go) → [SessionService 用例](../backend/internal/application/account/session.go) → [会话生命周期用例](../backend/internal/application/account/session_lifecycle.go)/[独立端口](../backend/internal/domain/account/repository.go) → [会话仓储](../backend/internal/infra/persistence/account/session_repository.go)与[凭据适配](../backend/internal/infra/persistence/account/legacy_credentials.go)。Domain 会话创建输入携带所需哈希，读取仅返回 Session 快照；唯一 AuthSession ORM 仅在 Persistence。密码比较、随机/刷新哈希和访问令牌签发分别由小端口接 bcrypt 与 [Infra JWT](../backend/internal/infra/jwt/refresh_token.go)/[签发实现](../backend/internal/infra/jwt/access_token_issuer.go)，内层仅依赖标准库和 Domain，基础设施错误在外层归类。

登录仍先限流、binding，再只 TrimSpace 用户名、读取密码哈希并比较，先保存七天会话后签发十五分钟 JWT；没有复用注册的字节长度规则，密码保持原样。不存在用户或比较失败为 401，其他凭据读取错误为 500 failed to authenticate，会话创建阶段任意错误为 500 failed to create session。刷新原样使用 refresh_token，先查活动会话并以旧刷新哈希做 CAS 轮换，保留 session ID 和 expires_at，再读当前用户、签发 JWT；轮换阶段任意错误均为 401 invalid refresh token，读取用户失败仍尝试撤销后返回同一 401，签发失败为 500 failed to create access token。退出由 [Interfaces JWT 中间件](../backend/internal/interfaces/http/auth/jwt.go) 验证后只撤销当前会话，成功为空 204，身份缺失或任意撤销错误为 401 invalid or expired token。公开 user 保留原字段和 omitempty。

登录落库后的签发失败、刷新 CAS 后的读取/签发失败仍没有外层回滚。旧会话算法/SQL、JWT 中间件、注册与匿名读取未改；仅删除确认无引用的旧入口/DTO/助手，保留改密/注销事务和头像读取。以上为源码兼容证据，vet/build 已通过，未运行 Go 测试或真实 HTTP/MySQL/Redis 会话回归；已提交为 `f20dcdf`，未推送，详见[开发计划第 6.11 节](./DEVELOPMENT_PLAN.md#611-r3-c-登录刷新与退出已提交)。

改密与注销走 [AccountSecurityHandler](../backend/internal/interfaces/http/account/security.go) → [AccountSecurityService](../backend/internal/application/account/security.go) → [原子写入端口](../backend/internal/domain/account/repository.go) → [事务适配](../backend/internal/infra/persistence/account/legacy_security.go)。改密先保留原 binding，两个密码不 Trim；[Domain 新密码规则](../backend/internal/domain/account/security.go) 仅检查新密码 8–72 字节，再通过凭据读取端口读用户、比较旧密码、生成 bcrypt 默认成本哈希。Infrastructure 使用同一个 tx 构造原用户和会话仓储，先用原密码哈希 CAS 更新，再撤销全部会话；CAS 未匹配为 403 wrong password，撤销失败会回滚更新。校验/读取/哈希仍在事务前，不增加锁、重读或重试。

注销没有用户预读，在同一事务中先调用原 Account Repository.DeleteUser 软删除，再调用 SessionRepository.UpdateUserSessionRevocations；失败仍整体回滚。改密成功为 200 + 原 message，注销成功为空 204，原 400/401/403/404/500 文案保留，不创建会话或令牌，不改变媒体、关系或视频清扫。确认引用后删除旧两个 Controller/Service 方法、密码 DTO 与无用途 ErrWrongPassword/auth/bcrypt/GORM Service 依赖，保留改名/资料/头像、Service.GetByID、仓储与 ORM。两个回滚流程和注销 HTTP 流程仅作必要装配/调用适配，断言未改。以上为源码证据，vet/build 已通过，未运行 Go 测试、真实改密/注销及故障回滚回归；已提交为 `4f4838b`，未推送，详见[开发计划第 6.12 节](./DEVELOPMENT_PLAN.md#612-r3-d-改密与注销已提交)。

改名、资料与头像走 [ProfileHandler](../backend/internal/interfaces/http/account/profile.go) → [ProfileService](../backend/internal/application/account/profile.go) → [Domain 规则/独立输入](../backend/internal/domain/account/profile.go)与[ProfileWriter](../backend/internal/domain/account/repository.go)。[写入适配](../backend/internal/infra/persistence/account/legacy_profile.go) 委托原 Account Repository.UpdateName/UpdateFields/UpdateAvatar，保留唯一键/1062、RowsAffected、软删除和大小写语义，不加预读或事务。改名仍先按原 binding 校验，再 TrimSpace 与 3–32 字节检查；资料先按原 omitempty/max 标签绑定，再分别 TrimSpace，只更新非空 bio/avatar_url，均空为 nothing to update，不支持清空或增加 URL 格式规则。

头像的 multipart、大小限制、读取前 512 字节、原扩展名/文件头和 Seek 均保留在 HTTP，[Domain 头像规则](../backend/internal/domain/account/avatar.go) 与旧规则一致。Application 依次读取当前账户、保存新对象、TrimSpace URL 后写库；写库失败尝试清理新 URL，成功后才尝试清理不同的旧 URL，两处清理错误均忽略。[存储适配](../backend/internal/infra/persistence/account/legacy_avatar_storage.go) 委托 R4-B2 迁入 Infrastructure 的唯一 LocalStorage.SaveAvatar/RemoveAvatar，仅替换具体依赖，不改物理路径、文件名、随机生成和安全删除；文件/数据库无共同事务，资料 JSON 直接改 avatar_url 仍不清理对象。

三个成功响应仍为改名/资料 200 + 原 message、头像 201 + 原 avatar_url，认证、binding、400/404/409/413/500 文案保持原规则。旧 user/controller.go、avatar.go 与三项写 Service/DTO 已删除，User 仓储/ORM 已由 R3-F2 迁入 Account，旧夹具 Service 已清理，领域错误保留；R3-E 当时保留的旧统计接口/结果已由 R3-F1 清理。R3-E 实施轮 36 项源码对照、vet/build 通过，30 个保护源码及全部 5 个测试文件未改；未运行 Go 测试、真实 HTTP/MySQL/文件上传或补偿故障回归。18 个文件已提交为 `f5c1260`，未推送，详见[开发计划第 6.13 节](./DEVELOPMENT_PLAN.md#613-r3-e-改名资料与头像已提交)。

R3-F2 的 [Repository](../backend/internal/infra/persistence/account/repository.go) 保留原全部 12 个方法和 SQL、列投影、软删除、keyset、RowsAffected、1062 与密码 CAS；[唯一 User ORM](../backend/internal/infra/persistence/account/user.go) 保留原名称/标签，GORM 仍映射 users。改密/注销事务只切换同一 tx 的仓储构造器，旧会话撤销与用户硬删除时先清会话的原事务均保持；sweeper 仅换装配，AuthSession/会话算法和清扫用例未迁。原 user 包、无用途 DTO/游标及夹具 Service 已删除，5 文件/36 测试函数保留且仅必要迁移/装配/符号适配，未运行 Go 测试。源码/静态/构建及目标库只读元数据核对通过，不代表真实仓储、事务、HTTP 或清扫回归；模块已提交为 `267463e`，未推送，见[开发计划 R3-F2](./DEVELOPMENT_PLAN.md#r3-f2用户持久化已提交)。

R3-G1 的 [JWT 实现](../backend/internal/infra/jwt/jwt.go)全文仅换包名，保留 HS256、claims、十五分钟 TTL、Secret 缓存/随机/回退及解析规则。HTTP 认证/上下文迁入共享 Interfaces Auth，旧 SessionService 与 Account 签发适配只换 JWT 调用；当时唯一 AuthSession ORM、仓储 SQL、哈希、固定到期、创建/轮换/Validate 和错误转换留 G2；现已由 G2 收口。确认全引用后删除旧 JWT/认证文件，所有消费者只改引用，两个保留测试只改导入/解析符号，断言未改。24 项源码检查、40 个内层文件依赖、236 个保护文件、vet/build 与文档/差异检查通过；未运行 Go 测试或真实 JWT/认证/HTTP 回归，未访问目标库或启动服务。模块已提交为 `e84f783`，未推送，见[开发计划 R3-G1](./DEVELOPMENT_PLAN.md#r3-g1jwt-与认证适配已提交)。

R3-G2 将会话创建/轮换/校验迁入 Account Application，唯一 AuthSession ORM 和原六个仓储方法迁入 Account Persistence。两次随机生成、SHA-256 hex、七天固定到期、入库后签发、刷新先查→生成→CAS→读账户→签发、失败残留/尽力撤销及原 401/500 文案均保留；改密/注销只切换同一 tx 的会话仓储，用户回收事务未改。随机/哈希/签发分别通过小能力端口接 Infra JWT，旧 auth 与冗余会话/issuer 转换已删除。一个保留测试只换类型/装配，断言不变；32 项源码、41 个内层依赖、242 个保护文件及 vet/build 检查通过，目标库版本 10、dirty=false，元数据和聚合实施前后相同，没有数据库写入。未运行 Go 测试或真实会话/认证/HTTP/事务回归；源码与构建不能替代运行验收。提交轮源码未改，核对 15 个精确路径/暂存差异后提交为 `fe6959d`，未推送；见[开发计划 R3-G2](./DEVELOPMENT_PLAN.md#r3-g2会话用例与持久化已提交)。

### 9.2 互动先保存关系，再读取聚合

[Relation Handler](../backend/internal/interfaces/http/relation/handler.go) → [Application](../backend/internal/application/relation/service.go) → [Domain 端口](../backend/internal/domain/relation/repository.go) → [Relation Repository](../backend/internal/infra/persistence/relation/repository.go) → MySQL。

关注状态、关注与取关使用这条链路。Domain 先校验非零 ID 与自关注限制，Application 按当前用户→目标用户校验活动账户，再查询或修改关系并独立读取粉丝数；活动用户的 GORM 不存在结果在 Infra 转换为领域错误。200 响应仍为 `following`、`follower_count`，未知存储错误仍使用原 `social operation failed` 安全文案；写入成功后计数失败不撤销已完成的关系变更。

两个匿名粉丝/关注列表也从 Relation Handler 进入。独立 [领域模型](../backend/internal/domain/relation/entity.go) 包含公开资料、关系时间/ID 和分页位置，不携带 ORM/HTTP 标签；Domain/Application 不导入旧 social 或 Gin/GORM。HTTP 先解析路径 ID 和 limit 文本，Application 再依次检查活动目标、limit 范围及 [原 v1 游标](../backend/internal/application/relation/cursor.go)，默认/显式 0 为 20，上限 50，使用 `limit+1` 探测。游标保留 RawURL Base64 的 `v/k/r/p/i` 字段及顺序，绑定列表与目标用户，取实际返回页末条的关系时间和关系 ID。

[list_reader.go](../backend/internal/infra/persistence/relation/list_reader.go) 直接使用领域位置，将原 SQL 的扫描行映射为独立领域行，旧列表/位置转换已删除。目标校验 + 一次 JOIN 固定两条 SQL；按 `(follows.created_at DESC, follows.id DESC)` 严格 keyset 并过滤注销对端，时间不截断。[HTTP DTO](../backend/internal/interfaces/http/relation/dto.go) 保持 `user`/`followed_at`、资料 omitempty、关系 ID 隐藏、空数组与末页省略游标；列表保留旧 GORM 不存在错误的 404 文案映射，未知存储错误仍为安全 500。唯一 [Follow ORM](../backend/internal/infra/persistence/relation/follow.go) 与原关注 SQL 已迁入 Relation，生产与保留测试不再引用旧 social，旧包已删除。既有流程在真实 MySQL 下继续读取迁移前固定的两个 v1 游标，验证同时间 ID 边界、毫秒精度及与新游标一致的续页。

点赞与关注通过关系表唯一键处理重复创建，删除返回是否确实发生变更；取关物理删除，评论使用软删除。Feed 的点赞和评论数来自 [statistics.go](../backend/internal/infra/persistence/interaction/statistics.go) 的当前页批量聚合：非空 ID 批次固定执行点赞、评论两条 `IN (...) GROUP BY video_id`，预先为每个 ID 补零，评论聚合遵循软删除作用域；空批次返回非 nil 空 map 且不查询。R2-C 实施边界及提交状态见开发计划第 6.8 节。

点赞状态与评论列表从 [router.go](../backend/internal/router/router.go) → [Interaction handler](../backend/internal/interfaces/http/interaction/handler.go) → [Application service](../backend/internal/application/interaction/service.go) → Domain Reader → [reader.go](../backend/internal/infra/persistence/interaction/reader.go) 直接读取 MySQL，不再调用 social.Repository。点赞状态先复用 `video.PublicVideoQuery` 校验完整公开视频，再校验活动用户、查询关系及实时计数；认证仍先由 JWT/session 中间件校验。评论由 [cursor.go](../backend/internal/application/interaction/cursor.go) 处理原 v1 字段和视频范围，仓储按 `(created_at DESC, id DESC)` 多读一条，一次 LEFT JOIN 带出全部作者资料，注销作者保留原 ID 与占位名。默认 20、显式 0 和最大 50、空数组及安全错误契约保持不变。旧 social 互动 Controller/Service、评论游标和读取 SQL 已删除。

四种写操作直接走 [HTTP handler](../backend/internal/interfaces/http/interaction/handler.go) → [application/interaction/service.go](../backend/internal/application/interaction/service.go) → [writer.go](../backend/internal/infra/persistence/interaction/writer.go)，读写统一使用 [interaction_model.go](../backend/internal/infra/persistence/interaction/interaction_model.go) 的 `VideoLike`、`Comment`，持久化包与领域包区分同名类型。实际点赞/取消、评论创建/软删除与 `interaction_outbox_events` 在同一事务提交；创建/取消先锁活动用户，再锁并复核完整公开视频。删评只锁活动用户及当前未删除评论并复核归属，不额外要求视频公开。重复点赞/取消不追加事实，重复删评仍为 404，评论 POST 仍无请求幂等键。提交后的统计/作者读取失败不撤销已提交事实。

必要互动夹具直接使用 Interaction ORM。[legacy_reader.go](../backend/internal/infra/persistence/interaction/legacy_reader.go) 的 EngagementReader 继续将独立领域统计转换为旧 `video.EngagementCounts`，router 同时注入 Video 与 Feed；资料适配器按获赞→粉丝→关注直接组合 Domain Account ProfileMetrics，并实现其独立统计端口。获赞使用完整 `video.PublicVideoQuery`，粉丝/关注通过 Relation CountReader 注入直接仓储，仍只统计活动对端账户；accountID=0 不查询，有效 ID 的统计部分仍为三条 SQL，出错即停止并返回零值和原错误。旧 Video 转换留在外层，Interaction 不再依赖旧 user 类型，旧 Video 不反向导入该持久化包。[FollowingReader](../backend/internal/infra/persistence/feed/following_reader.go) 通过窄接口调用 Relation RequireActiveUser，将不存在映射 Feed 401、其他依赖失败映射安全 503；会话校验、私有头与观看者游标保持原契约，非空六条/空页三条 SQL。完整视频与当前关系 JOIN 仍在 [video/following_repo.go](../backend/internal/video/following_repo.go)，Account/Video 剩余边界留给 R3/R4。迁移摘要见[开发计划第 6.4 节](./DEVELOPMENT_PLAN.md#64-已提交迁移摘要)，查询预算与未覆盖范围见第 5 节。

[domain/interaction/event.go](../backend/internal/domain/interaction/event.go) 固定 `event_id`、版本、类型、互动 ID、正负 delta、变更时间及原创建时间；[event_model.go](../backend/internal/infra/persistence/interaction/event_model.go) 的 `OutboxEvent` 映射 [000010](../backend/db/migrations/000010_interaction_outbox.up.sql) 的独立事实表，原视频/互动删除不会级联清理该表。2026-10-05 本机 feedsystem 已完成版本 9→10、dirty=false 的增量迁移及结构核对，摘要见开发计划第 5.1 节；其他部署目标仍须单独确认。

[worker/main.go](../backend/cmd/worker/main.go) 直接装配 [InteractionRelay](../backend/internal/worker/interaction_relay.go) → [Dispatcher](../backend/internal/application/interaction/dispatcher.go) → [Outbox 适配器](../backend/internal/infra/persistence/interaction/outbox.go)。每轮最多逐条派发 32 个已提交事实，领取使用数据库时钟、30 秒租约与递增 attempt；确认发布后仅在有效租约内标记 dispatched，失败退避，同事件可能重复投递。MQ 编码使用持久事实，不重新读取当前业务行拼装历史。

[interaction_spec.go](../backend/internal/mq/interaction_spec.go) 声明 `interaction.changed` → `feed.heat`、QoS 4、`1s/5s/30s` 重试和 DLQ；独立 Runtime 开启 mandatory/Return 检查。[HeatConsumer](../backend/internal/worker/feed_heat.go) 负责严格解码、有限重试/死信和成功后的 ACK，[HeatProjector](../backend/internal/application/feed/heat_projector.go) 将不可变事实映射到 [领域热度规则](../backend/internal/domain/feed/heat.go)，再调用 [Redis 热度适配器](../backend/internal/infra/cache/feed/heat_index.go)。

新增与撤销归属同一原创建分钟。Lua 状态 Hash 同时保存收据、绝对分数及容量计数，再把绝对分数写入 ZSET；重复投递读取当前分数补写 ZSET，不重复加分，撤销先到保留负贡献。桶过期由原创建分钟确定，不因重投延长，过期或已离开窗口且不存在的桶不重建。同代际规则指纹不一致、已有状态损坏或分钟容量超限均不伪造成功。

热度消费直接装配，处理上下文 5 秒、Redis 单次操作 100ms。Lua 的长度主要来自规则/类型/容量检查、event_id 去重、绝对分数补写及原分钟到期处理；这些逻辑用于处理重复与乱序，不在 API 请求中计算整张热榜。Hash 收据仍在时，重复投递可补写 ZSET；去重状态也丢失时不能据此保证恢复，属于后续 B2。

`coverage=unverified` 的含义是“窗口完整性尚未证明”。[heat_index.go](../backend/internal/infra/cache/feed/heat_index.go) 只在创建代际 meta 时写这个固定值，当前没有验证后更新它的代码；worker 日志也固定输出这个值。这是源码行为说明，本轮没有读取实时 Redis。

| 当前可观察的状态 | 能证明什么 | 仍不能证明什么 |
| --- | --- | --- |
| Redis 可连接、分钟 ZSET 有数据 | 某些操作可访问 Redis | 历史采集无缺口、整个窗口已消费 |
| Outbox 为 dispatched | broker 发布已确认且派发标记成功 | 事件已计分，或没有重试/DLQ 积压 |
| 单次消费成功、重复或过期跳过 | 此投递已按当前规则处理 | 窗口内其他事实齐全、索引可用于完整热榜 |

目前只有 `HeatIndex.ApplyHeat` 写入端口及处理结果、单事件 lag、队列深度，没有窗口榜读取、持久覆盖水位、事实重建或 MySQL 快照。`GetFeed` 仍将 Hot/Recommend 映射为 501；七项能力直接装配只覆盖已实现链路。下一步 B2 的扫描、重建和快照边界见 [开发计划第 3.8 节](./DEVELOPMENT_PLAN.md#38-f4-b2事实重建与-mysql-快照的下一步边界未实现)，真实故障与覆盖验收仍待补。

### 9.3 文件系统与数据库无法共用一个事务

[LocalStorage.Save](../backend/internal/infra/storage/media/local.go) 将上传媒体放入受控目录，清洗文件名并追加随机对象标识，使用独占创建避免覆盖已有文件。原始展示名与存储对象名分别保存。

文件落盘后，数据库绑定可能失败；文件删除成功后，检查点写入也可能失败。项目分别用以下机制处理：

- **草稿清扫**：不可逆进入 `purging`；视频、封面各自保存删除检查点。重试只处理未完成槽位；不存在的文件按删除成功处理。
- **租约接管**：先校验/续约 token，失去租约就停止继续推进；不让旧持有者写回新任务。
- **孤儿文件回收**：只枚举当前本地存储规则生成的规范对象，默认宽限 24 小时、每批最多 100 个候选；查询用户头像及视频/封面引用，**包含软删除保留期内的记录**，失败对象留待下轮。
- **已发布视频与用户清扫**：软删除先改变可见性，到期后再回收媒体和记录，默认保留期均为 7 天。

源码入口：[draft_purge.go](../backend/internal/sweeper/draft_purge.go) · [media_orphan_purge.go](../backend/internal/sweeper/media_orphan_purge.go) · [media_reference_repo.go](../backend/internal/sweeper/media_reference_repo.go) · [video_purge.go](../backend/internal/sweeper/video_purge.go) · [user_purge.go](../backend/internal/sweeper/user_purge.go)。

孤儿回收的宽限期是在覆盖落盘与绑定之间的短暂窗口，并不是文件系统与数据库的原子事务。现有视频 Outbox 外键会在视频硬删除时级联删除事件，也不应被理解成永久事件审计库。

## 10. 前端：分页并发与播放生命周期

不要只看 [FeedView.vue](../frontend/src/views/FeedView.vue) 的模板。列表正确性主要在 [usePublishedFeed.ts](../frontend/src/features/video/usePublishedFeed.ts)，HTTP 契约在 [api.ts](../frontend/src/features/video/api.ts)。

### 10.1 每个场景都有独立状态

Timeline 和 Following 各自保存视频、游标、首屏/续页加载状态、错误和 `loaded` 标志；另有各自的请求 `generation` 与 AbortController。

首屏重新加载、场景切换、退出/更换账户都会使旧请求失效。接收响应时不仅看请求是否成功，还检查 controller 归属、generation 和续页游标是否仍是当前值。因此浏览器取消之后晚到的响应也不能覆盖新状态。

同一场景续页正在加载时拒绝重复发起；合并按视频 ID 去重，重复 ID 更新已有位置。有限自动重试使用 300ms、900ms 两档；到达上限后显示可重试错误，不无限循环。续页遇到 400 会作废旧游标，后续重新读首屏。

### 10.2 身份变化不等于 token 变化

前端以 user ID 判断观看者切换。正常刷新 token 不清空分页；用户退出或换号会清空两个场景。Following 每次重新进入时刷新服务端列表，以反映关注变化。

页面场景以 URL 查询参数为来源：`/?scene=following` 可直达关注流，未登录时展示登录入口并保留回跳位置。关注请求通过 `withAuthenticatedSession` 获取访问令牌和处理会话恢复。

### 10.3 播放与发布恢复也是生命周期问题

`FeedView.vue` 用 IntersectionObserver 管理可见视频，离开路由、页面隐藏或组件卸载时暂停播放，并释放观察器与请求。

发布页面的恢复入口是 [PublishVideoView.vue](../frontend/src/views/PublishVideoView.vue) 与 [publishResume.ts](../frontend/src/features/video/publishResume.ts)：上传响应丢失不等于服务端上传失败，客户端会读取草稿事实确认媒体状态；202 后再查询处理结果。客户端记住的草稿信息不能替代服务端当前状态。

## 11. 观测、运行与验证边界

### 11.1 已有观测回答哪些问题

| 入口 / 事件                           | 能回答什么                                  | 不能据此推导什么                                |
| ------------------------------------- | ------------------------------------------- | ----------------------------------------------- |
| `/health`                             | API 进程是否存活                            | MySQL、MQ、Redis 或发布闭环是否正常             |
| `/ready`                              | 2 秒内 MySQL 探测是否成功                   | Redis/MQ 健康、表结构齐全、消费积压或媒体完整性 |
| `X-Request-ID`、`http_request`        | 路由、HTTP 状态、耗时与本次数据库查询数     | 完整业务追踪或指标告警已经建立                  |
| `feed_page_cache` / `feed_card_cache` | 命中、回源、坏值、超大跳过、失败与容量结果  | 命中一定带来性能收益                            |
| worker Outbox / 队列快照              | pending/publishing 数量、最老事件与队列深度 | dispatched 即消费完成，或队列深度含全部在途工作 |
| `interaction_relay`                    | 互动单事件派发、重试、租约丢失与标记失败     | 已有热度消费、覆盖水位或热榜积压告警             |
| `feed_heat` / `feed_heat_queue`         | 热度处理、重复、跳过、当前事件 lag 与队列深度 | 最老积压、完整覆盖、重建完成或自动告警           |
| sweeper 事件                          | 每项清扫结果、耗时、删除与失败数            | 所有文件已永久正确回收                          |

源码入口：[observability/http.go](../backend/internal/observability/http.go) · [db/query_counter.go](../backend/internal/db/query_counter.go) · [worker/observability.go](../backend/internal/worker/observability.go) · [sweeper/runner.go](../backend/internal/sweeper/runner.go)。

### 11.2 当前指标回调与容量工作的边界

现有 [Observe 配置](../backend/internal/config/config.go) 与 [Feed Handler](../backend/internal/interfaces/http/feed/handler.go) 的 `RequestObserver` 回调已作为两个生产文件提交为 `2ff0364`。Handler 可在请求完成时报告场景、首屏/续页、状态、条数及耗时；相关测试由其他任务提交为 `d056fe2`，本轮未触碰或运行。

当前 `router.New` 未注入该请求回调，API 入口没有指标采集器或独立监听装配，仓库也没有 `observability/metrics.go` 或 `internal/baseline`。Observe 字段和环境覆盖存在，不表示配置已对外提供 `/metrics`，也不表示 Prometheus、容量工具或 pprof 已实现。页/卡片缓存继续使用现有 stdout 观测。

后续指标模块需补齐有限标签采集、默认关闭的独立回环监听及资源关闭；容量测量继续暂缓。恢复测量时应记录源码、硬件、数据分布、关注规模、并发、p50/p95/p99、错误率、SQL、EXPLAIN 和资源消耗，并在同一环境下比较。当前不据此决定 Following 混合推拉阈值或缓存开启范围，实际计划见 [开发计划](./DEVELOPMENT_PLAN.md)。

### 11.3 阅读运行配置时的顺序

从 [config.go](../backend/internal/config/config.go) 看字段与环境覆盖，再看 [config.example.yaml](../backend/configs/config.example.yaml) 和 [.env.example](../backend/.env.example)，最后看进程入口是否确实装配。YAML 出现字段不代表已接入运行链路。

本地 API 从 `backend` 目录运行 `go run ./cmd`，worker 与 sweeper 分别用 `go run ./cmd/worker`、`go run ./cmd/sweeper`；前端从 `frontend` 运行 `pnpm.cmd dev`。先准备私有配置、创建数据库并显式迁移。应用不自动建库建表，Compose 的建库/迁移属于独立启动服务。

阅读异步发布必须同时考虑 API、worker 与 sweeper 的媒体根目录/共享挂载。独立启动三个进程但访问不同媒体目录，会破坏校验与回收链路。

### 11.4 用测试理解契约，不把测试文件当通过证据

| 要理解的行为                                  | 建议读的测试                                                                                                                                                        |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 缓存命中、载荷损坏与 MySQL 回源               | [e2e_test.go](../backend/internal/router/e2e_test.go)                                                                                                                |
| 当前关注关系、认证与跨用户游标                | [e2e_test.go](../backend/internal/router/e2e_test.go)                                                                                                                |
| 旧关系 v1 兼容、列表/用户绑定与 Following 关系/认证/预算 | [e2e_test.go](../backend/internal/router/e2e_test.go) |
| MySQL/Redis 下实际卡片读取                    | [e2e_test.go](../backend/internal/router/e2e_test.go)                                                                                                                |
| MySQL → Relay → RabbitMQ → 预热消费者 → Redis | [integration_test.go](../backend/internal/worker/integration_test.go)                                                                                               |
| 账户会话、改密/注销与撤销失败回滚 | [user_repo_test.go](../backend/internal/infra/persistence/account/user_repo_test.go) |
| 草稿部分回收、失去租约与断点继续              | [draft_purge_test.go](../backend/internal/sweeper/draft_purge_test.go)                                                                                              |
| 前端迟到响应、场景与分页                      | [usePublishedFeed.spec.ts](../frontend/src/features/video/__tests__/usePublishedFeed.spec.ts)、[FeedView.spec.ts](../frontend/src/views/__tests__/FeedView.spec.ts) |

当前保留 5 个 Go 测试文件、36 个函数；Feed service_test.go 的 4 个缓存专项与 Video video_repo_test.go 的 11 个专项由独立提交 `a483843` 删除。原 repo_test.go 的六个互动/关注流程与预算函数也不再保留，不将历史通过当作持续覆盖。后续按用户指令不运行 Go 单元测试或包含它们的全量/race 命令；R3-F2/G2 实施轮有目标库只读元数据核对，G1 只有源码、vet/build 与差异检查；G2 本轮同样未运行 Go 测试，不能称为真实依赖回归。保留文件可用于源码阅读，历史证据与未覆盖项见开发计划第 5 节；未运行不能算 PASS 或 SKIP。

## 12. 向量与推荐：当前边界及未来接入位置

### 12.1 当前没有 GoFeed 向量实现可供追踪

在当前 GoFeed 的业务源码、迁移、配置及依赖中，没有 embedding 生成、向量字段/索引、相似度检索或兴趣向量更新链路。`SceneRecommend` 只是预留场景值，`GetFeed` 当前返回 `ErrSceneNotEnabled`，HTTP 为 501。

开发计划的顺序是：先收集请求归因、曝光、有效观看和完播事实，再做规则推荐，数据与效果基线成立后评估向量召回。以下内容是**帮助理解扩展位置的概念说明，不是当前已实施方案或接口契约**。

### 12.2 向量解决的是候选召回

可以把内容或用户兴趣表示为若干维度的数值向量。相似度用于从大量视频中找出一批可能相关的候选，例如采用余弦相似度：

```text
similarity(user, video) = dot(user, video) / (norm(user) × norm(video))
```

这里的内容表示来自什么输入、使用什么模型、兴趣怎样由观看行为形成，都需要另外设计。向量召回只提供候选及相关性信号；它不能单独决定内容是否公开、用户是否有权限，也不能替代去重、作者打散、新鲜度与最终排序。

Timeline 当前按时间取候选，Following 当前按关注关系取候选。未来向量的接入位置应主要在**候选来源与推荐策略**，可复用现有卡片、作者和统计组装边界；当前 `Service` 仍直接分派场景，并没有已经建好的通用召回插件体系。

```mermaid
flowchart TD
    Facts["MySQL：视频与行为事实"] -.-> Build["未来：生成内容向量"]
    Facts -.-> Interest["未来：形成兴趣向量"]
    Build -.-> Index["未来：可重建的向量索引"]
    Index -.-> Recall["未来：候选 ID 与分数"]
    Interest -.-> Recall
    Recall -.-> Filter["复用：MySQL 公开状态检查"]
    Filter -.-> Rank["未来：规则排序、去重、作者打散"]
    Rank -.-> Assemble["复用：卡片、作者、统计组装"]
    Assemble -.-> Response["未来：推荐响应与专属分页"]
```

整张图的虚线表示尚未接入的推荐链路；“可复用”表示源码已有基础能力，不表示已经存在推荐调用。

### 12.3 后续设计需要解决的几个问题

| 问题               | 为什么影响正确性                                     | 与 GoFeed 现有机制的关系                                |
| ------------------ | ---------------------------------------------------- | ------------------------------------------------------- |
| 事实与索引的归属   | 索引丢失后应能重建，不能只在向量库保存业务可见性     | 延续 MySQL 事实源与派生系统边界                         |
| 内容/模型版本      | 旧生成任务晚到时可能覆盖新内容的向量                 | 类似 Outbox attempt 围栏，但需要独立内容及模型版本      |
| 删除与失效         | 相似搜索可能召回已删除的视频                         | 复用 MySQL 当前公开状态检查，不仅依靠索引删除事件       |
| 曝光归因与幂等     | 没有可靠行为事实，兴趣更新与效果评估会失真           | 需要新增行为事实契约，当前未实现                        |
| 候选不足、检索超时 | 需要对规则候选或 Timeline 定义显式降级               | 复用非权威依赖故障的思路，冻结推荐专属响应契约          |
| 分页稳定性         | 推荐分数及用户兴趣变化后，时间游标不足以保持分页语义 | 需要绑定策略/候选快照等版本，不能直接沿用 Timeline 游标 |

同样，Hot 需要分钟桶热度、MySQL 快照与独立分页；当前只实现了分钟桶消费，快照和 Hot 读取仍在计划中。Following 混合推拉是未来的派生 Inbox/作者索引。它们与向量召回分别解决热度、关注分发和相关候选问题，不能把三者混为一个功能。

## 13. 建议阅读顺序与自检题

### 13.1 用八站串起完整系统

| 顺序 | 阅读文件 / 方法                                                                                                                                                                                                                                     | 读完应能回答                                              |
| ---- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------- |
| 1    | [router.go](../backend/internal/router/router.go)、[cmd/main.go](../backend/cmd/main.go)                                                                                                                                                            | 哪些接口匿名，哪些认证？缓存到底在什么条件下被装配？      |
| 2    | [domain/feed/entity.go](../backend/internal/domain/feed/entity.go)、[repository.go](../backend/internal/domain/feed/repository.go)                                                                                                                  | 页条目、卡片、作者、统计各自承载什么？                    |
| 3    | [Service.GetFeed / assembleFeedItems](../backend/internal/application/feed/service.go)、[cursor.go](../backend/internal/application/feed/cursor.go)                                                                                                 | 场景如何分派？为什么多查一条？在哪一步截断？              |
| 4    | [legacy_reader.go](../backend/internal/infra/persistence/feed/legacy_reader.go)、[video_repo.go](../backend/internal/video/video_repo.go)、[video_scope.go](../backend/internal/video/video_scope.go)                                               | SQL 如何实现排序、公开过滤与批量读取？                    |
| 5    | [following.go](../backend/internal/application/feed/following.go)、[following_repo.go](../backend/internal/video/following_repo.go)                                                                                                                 | 关注流怎样绑定身份，怎样反映当前关系？                    |
| 6    | [timeline_cache.go](../backend/internal/application/feed/timeline_cache.go)、[cached_card_reader.go](../backend/internal/application/feed/cached_card_reader.go)、[cache/runtime.go](../backend/internal/middleware/cache/runtime.go)               | 缓存命中为什么仍查 DB？哪里回源、哪里 503、哪里快速放行？ |
| 7    | [UpdateDraftPublication](../backend/internal/video/video_repo.go)、[outbox_repo.go](../backend/internal/video/outbox_repo.go)、[worker.go](../backend/internal/worker/worker.go)、[feed_card_warm.go](../backend/internal/worker/feed_card_warm.go) | 事务、确认、ACK、CAS 与预热分别提供什么保证？             |
| 8    | [draft_purge.go](../backend/internal/sweeper/draft_purge.go)、[usePublishedFeed.ts](../frontend/src/features/video/usePublishedFeed.ts)、[FeedView.vue](../frontend/src/views/FeedView.vue)                                                         | 后台删除与前端请求怎样跨失败恢复并避免旧状态覆盖？        |

第一遍顺着无缓存 Timeline 和正常发布链路读；第二遍再逐个追失败分支；第三遍对照测试检查自己的理解。每个复杂方法优先看输入、事实读取、状态条件、外部副作用和错误返回。

### 13.2 带着具体场景复述源码

1. **页大小 12，MySQL 返回 13 条：** 哪条生成下一页游标？哪条只用于探测？作者与统计是否读取第 13 条？看 `GetFeed` 和 `TestGetFeedProbeRecordExcludedFromBatches`。
2. **缓存页最后的探测视频被删除：** 是否只丢弃这条？沿 `pageCardsMatch` 找到整页按原游标回源。
3. **两层缓存全命中：** 还会查哪些表？沿公开状态、两条统计聚合和作者读取核对。
4. **用户 A 的 Following 游标交给 B：** 哪一步拒绝？身份从哪里来？看 `decodeFollowingCursor` 和 Handler 的 JWT 装配。
5. **MQ 在发布请求时宕机：** API 是否已经接受？视频在哪个状态？谁负责重放？看发布事务和 Relay。
6. **MQ 已确认但 dispatched 写回失败：** 后续为什么可能重复？旧 Relay 为什么不能覆盖新租约？看 `attempt` 围栏。
7. **视频已 published，但消费者 ACK 丢失：** 重投会不会新增第二个发布事件？看 `CompleteVideoProcessing` 的 CAS 与事务。
8. **预热消息晚到，视频已经软删除：** 消费者会信消息还是重新查事实？晚写缓存又由哪里挡住？看 `CardWarmer.WarmCard` 与 `CachedCardReader`。
9. **封面删除失败，但视频文件已经删除：** 下一轮从哪里继续？是否可以把对象恢复成 draft？看 purging 和两个媒体检查点。
10. **快速切换 Timeline/Following，旧首屏最后返回：** 为什么不会覆盖当前页？看 `generation`、AbortController 与场景独立状态。

### 13.3 最容易读错的边界

| 容易混淆的判断                   | 应采用的判断依据                                  |
| -------------------------------- | ------------------------------------------------- |
| 有 SceneRecommend 就有推荐系统   | 看 `GetFeed` 实际分支、配置装配和请求结果         |
| 有两层缓存就几乎不查数据库       | 看公开状态、作者、计数与冷缓存额外读取            |
| HTTP 202 就说明发布完成          | 202 是接受；`published` 才是公开可见状态          |
| Outbox dispatched 就说明预热完成 | 它记录派发；消费及 Redis 写入需要另外的证据       |
| UUID、CAS、ACK 就是恰好一次      | 网络确认有不确定窗口；设计允许重复并保证处理幂等  |
| keyset 就是分页快照              | 它定位排序位置；当前事实与可见集合仍会变化        |
| fail-open 就是防滥用能力更强     | 它优先业务可用性，故障期 Redis 限流能力会失效     |
| 缓存单探针就是每个 key 单次回源  | 单探针协调 Redis 恢复；当前没有 key 级请求合并    |
| `/ready` 成功就是整个系统健康    | 当前只证明 MySQL 可探测，不证明异步链路与媒体状态 |
| 工作树已有指标代码就有容量结论   | 需要匹配提交、环境、数据和实际测量报告            |
