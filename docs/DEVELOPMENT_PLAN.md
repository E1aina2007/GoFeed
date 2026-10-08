# GoFeed 开发计划

> 更新日期：2026-10-08。本文维护未完成任务、必要设计和验收缺口；已实现能力见 [README](../README.md)，接口见 [API](../API.md)，源码链路见 [源码导读](./SOURCE_CODE_GUIDE.md)，工作规则见 [AGENTS](../AGENTS.md)。

## 阅读导航

- [1. 当前基线与优先顺序](#1-当前基线与优先顺序)：先确认已实现和未实现的边界
- [2. Feed 缓存后续工作](#2-feed-缓存后续工作)：一致性、容量和收益缺口
- [3. F2–F6：Feed 派生能力](#3-f2f6feed-派生能力)：指标、Following、热度重建与推荐
- [4. 其他待开发与评估模块](#4-其他待开发与评估模块)：按需求独立立项
- [5. 待补验证与交付门槛](#5-待补验证与交付门槛)：历史证据、剩余专项和检查要求
- [6. 全后端四层架构演进](#6-全后端四层架构演进)：分层规则、迁移路线和当前模块

R2-B 已提交为 `f9481b2`，[R2-C 关系持久化与旧 social 收口](#68-r2-c-关系持久化与旧-social-收口已提交) 后端为 `ea36d40`；[R3-A 三个匿名账户读取](#69-r3-a-三个匿名账户读取已提交) 后端/API 为 `35a6fe0`，[R3-B 注册接口](#610-r3-b-注册接口已提交) 后端/API 为 `a834d46`，均未推送。[R3-C 登录、刷新与退出](#611-r3-c-登录刷新与退出已提交) 已提交为 `f20dcdf`，[R3-D 改密与注销](#612-r3-d-改密与注销已提交) 为 `4f4838b`，[R3-E 改名、资料与头像](#613-r3-e-改名资料与头像已提交) 为 `f5c1260`，均未推送。账户 HTTP 已全部迁入 Account；[R3-F1](#r3-f1账户跨模块读适配已提交) 已提交为 `c335902`，未推送；[R3-F2](#r3-f2用户持久化已提交) 已提交为 `267463e`，未推送；[R3-G1](#r3-g1jwt-与认证适配已提交) 已提交为 `e84f783`，未推送；[R3-G2](#r3-g2会话用例与持久化已提交) 已提交为 `fe6959d`，未推送；[R4-A1 已发布详情与公开列表](#r4-a1已发布详情与公开列表已实现待-review) 已实现，未暂存/提交，等待 review。[第 6.14 节](#614-r3-后续收口与-r4r6-重构路线)本人列表、处理状态及其余模块尚未实施。后续不运行 Go 单元测试，默认仅静态检查、构建与差异检查。Feed 功能路线下一步为 [F4-B2 事实重建与 MySQL 快照](#38-f4-b2事实重建与-mysql-快照的下一步边界未实现)，两条路线分别 review。

## 1. 当前基线与优先顺序

MySQL 是唯一业务事实源；Redis 是可重建缓存/索引及限流存储；RabbitMQ 承担至少一次投递。保留事务 Outbox、publisher confirm、租约/attempt 围栏、消费 CAS 幂等、有限重试与 DLQ。

| 路线 | 当前状态 | 下一动作 |
| --- | --- | --- |
| 架构 R1 | Interaction 六个 HTTP、ORM/直接读取、事务、批量统计与获赞读取已迁移；资料统计已接 Account 领域类型 | 外层 video 转换随 R4 收口 |
| 架构 R2 | R2-A/B/C 已提交，关系 ORM/SQL、资料计数及 Following 活动观看者已收口，social 已删除 | 保留专项验收缺口 |
| 架构 R3 | R3-A/B/C/D/E/F1/F2/G1/G2 已提交；会话用例/ORM/仓储已归 Account，旧 user/auth 已删除 | 真实兼容/故障专项继续保留 |
| 架构 R4 | R4-A1 已发布详情与公开列表已归 Video 四层，未暂存/提交；仓储/媒体及其他用例仍在旧 Video | 先 review R4-A1，再独立迁本人列表、处理状态 |
| Feed F0/F1 | 匿名 Timeline、批量卡片、页/卡片缓存及首页接入已实现 | 补容量、收益与一致性专项 |
| Feed F2 | 视频事件类型路由、发布事件及卡片预热已实现 | 保留未覆盖的双规格重连等可靠性专项 |
| Feed F3 | MySQL Following 与页面已实现 | 指标出口待接通；容量工具暂缓，混合推拉须收益证据 |
| Feed F4 | 互动事实、可靠派发和分钟桶消费已实现，coverage=unverified | 补真实专项，再按 F4-B2 完成重建/快照，随后开放 Hot |
| Feed F5/F6 | 曝光、规则/向量推荐及完整恢复运维未实现 | 按数据、效果和恢复需求拆分 |

API 直接装配页/卡片缓存，worker 直接装配发布预热、互动 Relay 与热度消费，七项能力的布尔开关已删除。worker 在 `startWorkers` 中统一启动、取消、等待，再关闭资源；热度规则由 Domain 校验。Hot/Recommend 仍返回 501，未知场景为 400。

Timeline 保持匿名；Following 使用 JWT/session 与活动观看者校验、私有响应和独立游标，绕过 Timeline 缓存。作者页继续使用旧 `/api/video?author_id=...`。架构迁移不自动增加新 Feed 功能，每次完成一个模块后停止等待 review。

## 2. Feed 缓存后续工作

现有读取与恢复路径见 [源码导读第 6 节](./SOURCE_CODE_GUIDE.md#6-多层缓存页缓存卡片缓存与实时数据)。后续保留以下工作：

- 比较直读与缓存的查询成本、p95、回源耗时和资源使用。ID 页缓存命中仍查 MySQL，不能仅凭命中率或 SQL 次数宣称收益。
- 补页缓存默认 TTL 自然过期回源、多实例同 Key 竞争、真实 Redis 断连/服务端超时、容量饱和与槽释放。当前每实例上限为 32 个缓存 Feed 请求、16 次缓存操作；请求容量不足返回安全 503，缓存容量不足跳过缓存。
- 页载荷限制目前在编码及 Get 后解码；卡片批量 Lua 已先检查 STRLEN，页缓存尚无同等 Redis 端有界读取。
- 作者资料与点赞/评论数继续实时批量读取，作者/统计缓存未实现。新增缓存须定义失效和旧请求回填围栏。
- 基础卡片命中仍用 MySQL 完整公开规则核对 ID、作者和发布时间。未来支持已发布内容编辑时须增加内容版本，不能沿用发布时间作为编辑一致性保障。

默认装配已改变，旧“关闭缓存开关”的回退说明失效。代码回退须明确事件生产、存量消费和客户端游标边界；只处理自有精确 Key 或等待 TTL，不全库清理 Redis。

## 3. F2–F6：Feed 派生能力

### 3.1 F2-A：Outbox 事件类型路由（已实现）

现有 Relay 按 event_type 装配 `video.process`、`video.published` 的目标及快照准备；未知类型保持有界退避。视频处理终态收口只适用于该路由，不能据此推断互动消费完成。新增事件须同时定义持久事实、schema、幂等、拓扑、ACK、重试/DLQ 和恢复边界。

### 3.2 F2-B：发布事件与单视频卡片预热（已实现）

`processing → published` CAS 与 `video.published` Outbox 同事务；独立 `feed.card.warm` 消费当前 MySQL 卡片。预热只写基础卡片，不包含作者资料、互动统计或任意游标页。重复投递重新读取当前公开事实并覆盖相同 Key；不可见视频清理精确 Key，超大值跳过，依赖故障有限重试。

SET/清理成功后 ACK；重试发布确认前不 ACK 原消息。生产 Runtime 显式使用 mandatory/Return，重连恢复拓扑。dispatched 仅为派发确认，预热日志不是持久消费水位。历史可靠性范围及当前缺口见第 5 节。

### 3.3 下一模块顺序

| 顺序 | 最小交付 | 进入条件 |
| --- | --- | --- |
| F3-C1 | 接通有限标签指标与独立监听；容量工具继续暂缓 | 测量恢复后取得同环境、同数据的收益基线 |
| F3-C2/C3 | Following 派生写入、重建，再影子比较与读取切换 | 确有收益，覆盖与回退已证明 |
| F4-A/B1 验收 | 互动事务、可靠派发及幂等热度真实专项 | 故障、重放与覆盖边界可观察 |
| F4-B2 | 有界事实扫描、Redis 代际重建、MySQL 快照 | 固定事实时点与在线增量衔接明确 |
| F4-C | Hot 策略、快照绑定游标，随后独立接入页面 | 覆盖、同分排序、到期及降级契约成立 |
| F5/F6 | 曝光/观看事实、规则推荐、恢复/告警 | 归因、隐私、效果和容量依据成立 |

向量召回另行评估；GCFeed 的本地 128 维 hash-ngram 特征和有界候选相似度不能当作 GoFeed 已具备大规模向量召回的证据。

### 3.4 Following 剩余专项与容量评估

已实现 MySQL 当前关系查询和前端场景切换，仍需：

- Timeline 真实卡片缓存浏览器、移动端真实 UI 登出、专用 Redis 进程重启等专项。历史 Following 联调以登出 API、清会话及重载验证退出状态。
- 真实浏览器与 mock 页面证据分别报告；正式浏览器证据持久化，Authorization、认证请求/响应、参数及 SHA1 资源均须脱敏。
- 扩大数据分布与关注规模，记录首屏/续页扫描量、p95、连接池和资源成本。历史样本只有 128 名作者、1152 条视频，非空 6/空页 3 条 SQL 只证明查询次数。
- Inbox/Author Outbox 与阈值尚未实施；确有索引缺口再新增迁移，不修改已应用版本。切换/回退场景后清空游标，不跨场景或跨接口续页。

### 3.5 指标与 Following 混合推拉的实施条件

指标配置与 Feed 请求回调已提交为 `2ff0364`，采集器/监听尚未装配。出口先采用默认关闭、独立回环监听及完整关闭生命周期；场景、结果、缓存类别使用有限标签，用户/视频 ID、游标、request_id 和凭据不进入标签。采集不能改变业务返回、缓存装配或 /ready；不附带 Grafana、pprof、迁移或新队列。

容量测量恢复后，同环境比较 Timeline 首屏/续页、缓存装配与 MySQL Following，记录 p50/p95/p99、错误率、SQL、EXPLAIN 实际扫描量、CPU/内存；覆盖稀疏/多关注、热门作者、多种公开视频比例及同发布时间，逐级提高并发并定义停止条件。

只有收益成立才推进混合推拉：

1. 先派生写入：配置化大小作者阈值、Inbox/Author Outbox 上限和关注补偿窗口；MySQL 提交后按粉丝 keyset 限批处理，保留续跑位置，API 继续读 MySQL。
2. 证明重复、失败、DLQ、关注/取关、删除及跨阈值的写入/重建语义，再接独立读端口与影子比较。
3. 读取仍校验 MySQL 当前关系、活动作者及公开状态；缺失、部分覆盖、过期、损坏或 Redis 故障整页回源。保留精确 `(published_at, id)` 顺序、截断后历史回源和限批补足，不用合成 ZSET 分数替代 keyset。

### 3.6 F4-A：互动事实事件（已实现，专项待补）

`interaction_outbox_events`（迁移 000010）保存不可变事实及投递状态，无级联外键。事实 v1 包含稳定 UUID event_id、类型/版本、video_id、kind、interaction_id、正负 delta、变更时刻和原创建时刻；时刻按 UTC 毫秒保存，MQ 载荷只由已提交行构造，不包含正文、资料或凭据。

仅真实点赞/取消/评论创建/删除与事实同事务写入，仓储内复核权限、锁定或 CAS。重复点赞/取消不再生事件；评论 POST 没有请求幂等键，重试可创建新评论。事件插入错误回滚整体；提交结果未知须读回，提交后统计/作者读取失败不撤销事实。

独立互动 Relay 持有有界租约/attempt 围栏，以同一载荷重试；confirm 后标记失败允许接管重发。默认每轮最多 32 条、租约 30 秒、单操作 5 秒、退避上限 5 分钟；独立 Runtime 声明 `feed.heat`、QoS 4、1s/5s/30s 重试及 DLQ，开启 mandatory/Return 并恢复拓扑。API 不连接 RabbitMQ。

事实保留时长、清理水位与恢复策略未设计完成；不能按 dispatched 删除。物理级联回收、注销及后台清扫不会逐条合成负事件，热榜可见性/重建须单独处理。真实事务、并发、非 UTC 往返、提交不确定与投递故障见第 5 节待补项。

### 3.7 F4-B1 幂等分钟桶消费（已实现，覆盖未证明）

源码入口：[heat.go](../backend/internal/domain/feed/heat.go)、[heat_projector.go](../backend/internal/application/feed/heat_projector.go)、[heat_index.go](../backend/internal/infra/cache/feed/heat_index.go)、[feed_heat.go](../backend/internal/worker/feed_heat.go)。

- 只计点赞/评论，默认权重 3/5、窗口 60 分钟、宽限 10 分钟、去重 24 小时，每分钟最多 10000 视频/100000 事件；这些是初始参数，不是效果或容量证明。
- 新增/撤销均归原互动创建分钟；撤销先到保留负贡献，不在写入时截断。Lua 先在状态 Hash 记录收据/绝对分数，再写 ZSET，重投修复 ZSET 而不重复加分；状态丢失时不猜累计值。
- 分钟桶按创建分钟 + 1 分钟 + 窗口 + 宽限绝对到期，重投不延长；过期桶不重建。Redis TIME 判定窗口，未来超过 30 秒的事件重试后可进 DLQ。
- 同代际使用共同 hash tag 和规则指纹；修改窗口、权重、去重或容量须换代际。载荷有界且严格校验，成功后 ACK，依赖失败有限重试，确认重试发布后才 ACK 原消息。

目前只有 `HeatIndex.ApplyHeat` 写入端口、处理结果/单事件 lag/队列深度。coverage 始终为 unverified，没有窗口榜读取、完整消费水位、事实扫描、代际切换或快照；历史采集缺口不能因收到首条事件而变为完整。

### 3.8 F4-B2：事实重建与 MySQL 快照的下一步边界（未实现）

从 MySQL 不可变事实恢复丢失/不完整的 Redis 索引，并为 Hot 提供稳定分页结果；快照是可重算派生数据，不替代事实。分模块交付：

| 模块 | 范围与完成条件 |
| --- | --- |
| 覆盖契约与有界扫描 | 冻结窗口、规则指纹、事实读取时点、恢复位置；明确并发提交、迟到撤销、采集缺口和取消续跑 |
| Redis 代际重建 | 限批、限总量、限时重放原 event_id 与正负事实；失败保留旧代际，在线增量衔接及覆盖验证完成后才能切换 |
| MySQL 有界快照 | 保存窗口、代际/规则版本、事实边界、覆盖状态、生成/到期时刻及固定排名；快照头/条目同事务，固定同分次序和条目上限 |
| 随后的 F4-C Hot | 独立策略及快照绑定游标，复用批量卡片；校验当前 MySQL 可见性，Redis 故障先读有效快照，缺失/到期的错误或显式 Timeline 降级先冻结契约 |

恢复必须证明：

- 自增 ID 不保证提交顺序；最大 ID、dispatched、空队列或低 lag 都不等于完整覆盖，验收包含“小 ID 事务后提交”。
- 000010 没有回填历史事实；选择并验证完整采集起点，或另行设计业务基线与增量衔接，未证明时保持 unverified。
- 固定时点快照不等于实时追平。事实保留/清理须覆盖迟到、恢复及快照有效期，不能以 Redis 去重键或派发状态推断安全删除。

复用 Feed/interaction 应用层与 worker 的 startWorkers，不为装配新建独立包或恢复七项开关。新表按迁移目录和目标库分配版本。Hot 不在请求中实时全表聚合，不静默混用 Timeline 游标。

## 4. 其他待开发与评估模块

以下为未实施方案，按产品/容量需求单独 review；共享媒体存储、跨进程时区与 gorm.io/gen 仍是未冻结评估项。

| 模块 | 保留设计边界 |
| --- | --- |
| pprof | 默认关闭；API/worker 使用独立 ServeMux 与字面量 loopback 监听，sweeper 暂不接；配置/监听失败不终止业务，设置请求头超时及独立关闭上下文 |
| 会话缓存 C2 | 先证明 auth_sessions 查询与 p95 收益；只缓存已验证身份/到期/版本，TTL=min(5m, token 剩余, session 剩余)，坏值/故障回 MySQL。登出/改密/注销提交后精确失效；冻结撤销并发、删除失败和迟到回填窗口，不用 SCAN |
| 分片上传 | 保留直传及 200 MB 上限，首版 5 MB 视频分片；MySQL 保存 owner/draft/哈希/分片/租约与预留对象，唯一(upload_id, chunk_index)，complete 用 CAS/租约，绑定草稿与会话同事务。staging 在静态根目录外；失败进入不可逆 purging，复用存储/sweeper，文件与 DB 无共同事务 |
| 话题标签 | Unicode/NFC/大小写规范化、最多 64 rune/每视频 10 个去重标签；关联与发布/Outbox 同事务。tag 与 author_id 初版互斥，游标绑定规范名，SQL 内过滤，硬删除级联关联；不顺带做热门标签/搜索建议 |
| 累计点赞榜 | 区别于近期热度，基于现有点赞关系与公开规则聚合，独立(likes_count, video_id)游标，不恢复冗余计数；动态排名不承诺跨页快照，需要稳定结果再设计物化 |
| 通知 | 按迁移后的业务归属，将真实非自互动与通知同事务，唯一(event_type, source_id)，失败整体回滚；JWT 保护本人列表/未读/已读，游标绑定收件人，单收件人不新增 MQ |
| SSE | 通知持久化后再做：60 秒用途隔离 HMAC 票据绑定 user/session，签名常量时间比较，新连接复核 session；票据可重放，不把 access token 放 query。每用户最多 5 连接、有界缓冲/丢帧、30 秒心跳，提交后推送，多实例漏帧靠列表收敛 |
| 私信 | 独立 message，同步写 MySQL；规范化会话对、保留方向，最多 2000 rune，独立 keyset；发送要求活动非本人收件人，历史读取保留注销对端，已读只改本人收到的消息。附件、撤回、收件箱摘要和实时推送另立项 |
| 公开 Feed 登录态 | 产品/查询压力成立才做；无凭据匿名，有凭据失败为 401；批量最终页点赞态，Vary: Authorization，认证结果 private/no-store，不进入公共缓存；新缓存预算重新核对 |

分片上传须抽取接受事务句柄的共同草稿绑定校验，不能嵌套调用当前自行开事务的方法并宣称原子；补缺片、并发 complete、崩溃、补偿与过期清扫。通知跳转不阻止视频/评论清扫；SSE 不实现 Last-Event-ID 重放，设置 no-store/no-referrer/禁代理缓冲并脱敏 ticket，已建立连接撤销及跨实例广播另立项。私信 DDL 的 CHECK 支持须核对实际 MySQL 版本。

## 5. 待补验证与交付门槛

### 5.1 历史验证摘要

以下均为相应日期的历史证据；删除重复日志和过时测试入口，不将旧工具/开关描述为当前可运行能力。

| 日期/模块 | 已记录证据 | 结论边界 |
| --- | --- | --- |
| 2026-10-02 Timeline/卡片 | 真实 Go/MySQL/Redis 与桌面/移动浏览器；不同缓存装配响应一致并独立观测命中 | 媒体为本地夹具；未证明 MQ 发布、缓存收益或生产容量；相关专项部分已随测试精简删除 |
| 2026-10-02/03 F2 路由/预热 | vet、普通/race、真实 MySQL/RabbitMQ/Redis；发布 CAS/回滚、派发/预热及 HTTP 卡片读取 | 部分 ACK/拓扑断言用 fake；双规格真实重连等仍缺；6 个未启用专项不计通过 |
| 2026-10-04 Following | 真实浏览器覆盖页缓存关/开 × 桌面/移动及登录/刷新衔接；认证 trace 漏检修复并复扫 | 移动 UI 登出、专用 Redis 重启、容量及索引收益仍缺；普通与 race 按原记录区分 |
| 2026-10-05 本机迁移 | feedsystem 从 9→10、dirty=false；互动事实表 18 列/7 索引/无外键，原七表行数未变 | 只证明迁移/结构；其他目标重新核对；保留迁移 receipt，不等于业务消费验收 |
| 2026-10-05 默认装配/worker | `8e059e2` 完成直接装配、统一编排与领域规则校验；编译/静态检查通过 | 当轮没有业务、前端/浏览器及热度故障验收 |
| 2026-10-06 R1/R2-A | vet、全量普通及无缓存 race 通过；保留 8 文件/57 测试，0 失败/用例跳过，真实 MySQL/Redis/RabbitMQ 参与 | 只代表保留用例；已删除参数、并发和故障专项不算持续覆盖 |
| 2026-10-07 提交前 | vet、普通全量通过；5 测试包缓存，router/social/worker 重跑 | 未重跑 race；本轮文档精简只检查文档，不重新运行业务测试 |
| 2026-10-07 R2-B | 从 backend 直接运行 vet、普通全量及无缓存 race 全量，均退出 0；race 重跑 8 个测试包。补充普通 JSON 输出为 57 PASS/0 FAIL/0 SKIP，5 包缓存，router/social/worker 重跑；真实 MySQL/Redis/RabbitMQ 参与 | 固定迁移前两个 v1 游标可续页；两个匿名列表各两条 SQL。只代表保留流程和迁移必要适配，全部非法组合、故障/并发与容量专项未覆盖 |
| 2026-10-07 R2-C 实施轮 | Go 1.27.1 windows/amd64、CGO=1，从 backend 直接执行 vet、普通全量及 `go test -race -count=1 ./...`，均退出 0，普通/race 各重跑 8 个测试包；补充 `go test -json ./...` 为 57 PASS/0 FAIL/0 SKIP，8 包重跑、0 包缓存。真实 MySQL/Redis/RabbitMQ 参与；文档 219 个本地链接/锚点及围栏检查通过 | 当时为 8 文件/57 函数，两个固定旧 v1 续页与新游标一致，双向列表各 2、Following 非空 6/空页 3 SQL；后续 R2-C 提交轮 repo_test.go 已不存在，当时 7 文件/51 函数，不将已删除断言称为持续覆盖 |
| 2026-10-07 R2-C 提交轮 | 保留 repo_test.go 删除状态后，从 backend 直接执行 vet、普通全量及无缓存 race 全量，均退出 0；普通 7 包缓存，race 重跑 7 包。补充普通 JSON 为 51 PASS/0 FAIL/0 SKIP，7 包缓存、0 包重跑；保留的真实 MySQL/Redis/RabbitMQ 流程参与 race 验证 | 当时 7 文件/51 函数；固定旧 v1 仍在 router 流程成功续页，与新游标一致。未恢复测试或增加断言，已删除列表预算/完整互动流程不算当前覆盖；没有前端、容量与部署验收 |
| 2026-10-07 R3-A 实施轮 | Go 1.27.1 windows/amd64、CGO=1，从 backend 直接执行 `go vet ./...`、`go test ./...`、`go test -race -count=1 ./...` 均退出 0；普通 2 包缓存/5 包重跑，race 7 包重跑。补充 `go test -json ./...`：51 PASS/0 FAIL/0 SKIP，2 包缓存/5 包重跑；真实 MySQL/Redis/RabbitMQ 参与，默认缓存可用 | 当时 7 文件/51 函数，仅必要测试装配适配；账户旧 v1 为源码兼容证据，没有固定旧账户 v1 真实续页断言。资料五条/统计三条 SQL 和逐步失败仅核对复用实现；不能把关系列表或该历史结果称为当前账户专项验收 |
| 2026-10-07 R3-A 提交轮 | 按最新指令没有执行 Go 测试；从 backend 直接执行 `go vet ./...`、`go build ./...` 均退出 0，提交前检查精确暂存范围与空白 | 后端/API 为 `35a6fe0`，没有推送；Feed/Video 两个测试文件的删除未纳入 R3-A，随后已有独立提交 `a483843`。当前工作区与提交树均为 5 文件/36 函数；历史普通/race 结果不替代当前运行验证 |
| 2026-10-07 R3-B 实施/提交轮 | 实施轮使用 Go 1.27.1 windows/amd64、CGO=1，从 backend 直接执行 `go vet ./...`、`go build ./...` 均退出 0；默认缓存可用，仅适配保留测试的注册装配/夹具。提交轮代码未变，沿用该结果，检查精确暂存范围与空白 | 后端/API 已提交为 `a834d46`，未推送；未运行任何 Go 测试及真实 HTTP/MySQL/Redis 注册回归。执行顺序、双重长度校验、响应/错误、限流和唯一键仅有源码兼容证据 |
| 2026-10-07 R3-C 实施/提交轮 | Go 1.27.1 windows/amd64、CGO=1，从 backend 直接执行 `go vet ./...`、`go build ./...` 均退出 0；默认缓存可用。Account 内层依赖检查及 48 项源码对照检查通过；保留测试只适配三个入口的装配；提交轮 Go 源码未变，沿用实施轮结果并检查精确暂存范围/空白 | 已提交为 `f20dcdf`、未推送；未运行任何 Go 测试或真实 HTTP/MySQL/Redis 会话回归。执行顺序、阶段错误、响应、原限流及旧会话/CAS/JWT 复用仅有源码证据，构建不等于会话回归 |
| 2026-10-07 R3-D 实施/提交轮 | Go 1.27.1 windows/amd64、CGO=1，从 backend 直接执行 `go vet ./...`、`go build ./...` 均退出 0；Account 内层依赖检查及 35 项源码对照检查通过，两个事务体在类型/参数转换后与旧实现一致；提交轮 Go 源码未变，沿用实施轮结果并检查精确暂存范围/空白 | 已提交为 `4f4838b`、未推送；保留测试只适配装配/调用，断言未改。未运行任何 Go 测试、真实 HTTP/MySQL 改密/注销或事务故障回归；构建不等于运行验收 |
| 2026-10-07 R3-E 实施轮/提交轮 | 实施轮 Go 1.27.1 windows/amd64、CGO=1，从 backend 直接执行 `go vet ./...`、`go build ./...` 均退出 0；内层依赖与 36 项源码对照检查通过，30 个保护源码及全部 5 个测试文件未变。提交轮精确范围/暂存差异检查通过，18 文件为 `f5c1260` | 未推送；提交轮未改 Go 源码、未重跑 vet/build。未运行任何 Go 测试或真实改名/资料/头像上传及补偿故障回归；校验、执行顺序、错误和文件清理仅有源码证据 |
| 2026-10-07 四层路线分析 | 对照 GCFeed/GoFeed 源码、迁移及调用引用；40 个 Domain/Application 生产文件导入检查、4 文档 323 个本地链接/锚点、围栏/空白、git diff --check 与 3 文档精确范围检查通过；原 Mermaid 图未改 | 仅计划分析；新文档未暂存/提交，R3-F1 未实施。未重跑 Go 检查、运行 Go 测试/服务/真实业务或访问目标库；迁移源码不等于实际元数据证据 |
| 2026-10-08 R3-F1 实施/提交轮 | 实施轮从 backend 执行 vet/build，均退出 0；22 项源码检查、40 个内层文件依赖、327 个本地链接/锚点、围栏与差异检查通过。提交轮 Go 源码未改，11 个精确路径检查后提交为 `c335902` | 未推送；5 个测试文件未改，未运行 Go 测试、真实作者读取、资料统计或 HTTP 回归，未访问目标库 |
| 2026-10-08 R3-F2 实施/提交轮 | 从 backend 执行 vet/build，均退出 0；32 项源码检查、40 个内层文件依赖、原仓储全文/ORM/SQL及全部引用核对通过；只读核对目标 feedsystem 版本 10、dirty=false，用户/会话列与索引符合迁移；文档链接/围栏和差异检查通过 | 5 文件/36 测试函数保留，仅必要迁移/装配和符号适配；未运行 Go 测试、真实账户仓储/事务/HTTP/清扫回归，无数据库写入；提交轮代码未改，沿用实施轮结果并检查精确暂存范围/空白，提交为 `267463e`，未推送 |
| 2026-10-08 R3-G1 实施/提交轮 | 从 backend 执行 vet/build，均退出 0；24 项源码检查、40 个内层文件依赖、236 个保护文件及全部引用核对通过；JWT 全文仅换包名、HTTP 认证体仅换类型/导入，构造时保留 nil 指针语义；文档链接/围栏和差异检查通过 | 两个保留测试只改导入/解析符号，5 文件/36 函数和断言不变；未运行 Go 测试或真实 JWT/认证/HTTP 回归，未访问目标库或启动服务；提交轮代码未改，核对 17 个精确路径/暂存差异后提交为 `e84f783`，未推送 |
| 2026-10-08 R3-G2 实施/提交轮 | 从 backend 执行 vet/build，均退出 0；32 项源码检查、41 个内层生产文件依赖和 242 个保护文件核对通过；原唯一 ORM、六个仓储方法/SQL、创建/轮换/Validate 顺序、两项安全事务及 HTTP/错误转换核对；只读核对目标版本 10、dirty=false，列/索引/聚合实施前后相同；文档/差异检查通过 | 一个保留测试仅适配会话类型/装配，全部 5 文件/36 函数与断言保留；未运行 Go 测试或真实会话/认证/HTTP/事务回归，没有数据库写入或服务启动；提交轮源码未变，15 个精确路径/暂存差异检查后提交为 `fe6959d`，未推送 |
| 2026-10-08 R4-A1 | 从 backend 执行 vet/build，均退出 0；45 项源码对照、44 个内层 Go 文件依赖与 248 个保护文件检查通过；v1 游标、DTO、公开规则、读取/错误顺序和原 SQL 均核对；目标库 SELECT-only 核对版本 10、dirty=false、videos 21 列/8 索引与原迁移一致，实施前后元数据/聚合相同；文档/差异检查通过 | 全部 5 测试文件/36 函数未改，未运行 Go 测试、真实视频/作者/统计读取或 HTTP 回归；无数据库写入或服务启动；未暂存/提交/推送 |

原查询预算保留：评论/粉丝/关注列表 2，点赞状态含 session 5，评论创建 8，Following 非空 6/空页 3；卡片冷读 5→命中 4。R2-B 与 R2-C 实施轮曾直接断言默认/显式 0/limit=50 在粉丝、关注两个方向各两条 SQL；对应 repo_test.go 已从当前工作区删除，这些断言不再持续运行。Following 六条/三条及卡片预算仍有保留流程；次数证据不代表扫描量或性能。当前实际测试入口见 [源码导读第 11.4 节](./SOURCE_CODE_GUIDE.md#114-用测试理解契约不把测试文件当通过证据)。

### 5.2 必须保留的验收缺口

| 范围 | 未完成事项 |
| --- | --- |
| 缓存/容量 | 页 TTL 回源、多实例同键、服务端断连/超时、真实饱和/槽释放、页 Redis 端有界读取；收益/p95/生产容量 |
| F2 发布预热 | 双消费规格真实重连；video.published 专用派发标记失败恢复；默认装配后的整链故障证据 |
| F3 页面/派生 | Timeline 真实卡片缓存浏览器、移动 UI 登出、专用 Redis 重启；混合推拉的收益、覆盖和整页回退 |
| F4-A/B1 | 四种互动事实失败/并发、提交不确定读回、非 UTC 往返；未路由/confirm 丢失/租约接管/标记失败；重复/乱序/迟到/跨窗口、Redis 丢失、ACK 丢失、DLQ 回放及关闭 |
| F4-B2/Hot | 事实扫描、增量衔接、代际切换、快照存取、到期/覆盖/降级；热度完整性当前 unverified |
| 账户/HTTP | 全部参数/存储错误分类、关注写入后计数失败、并发关注/注销、413 大上传、真实 429、refresh 并发轮换、broker 主动 nack |
| 关系列表 | 全部非法游标字段/版本/时间/ID 组合、存储错误逐项注入、并发关注/注销及分页快照、超过 50 条关系的分页截断与大规模扫描；R2-B 的两个固定旧 v1、同时间 ID/毫秒、默认/显式 0、limit=50、两方向两条 SQL、空页/注销对端与校验顺序已运行 |
| R2-C 持续覆盖 | 资料统计三条 SQL/账户 0、获赞→粉丝→关注顺序及立即失败、Relation/Following 活动观看者 404/401/500/503 文案仅核对当前实现和原 SQL；本轮对应新增断言已删除，原有流程不含这些独立预算/故障断言，不能将一次普通/race 通过视为专项验收 |
| R3-A 账户读取 | 现有流程没有账户列表双模式、空 query/limit 校验顺序、固定旧账户 v1 真实续页及全部非法字段组合的独立断言；资料五条/统计三条 SQL、可选统计依赖和逐步失败仅核对源码。保留的详情/注销 404、资料头像流程不替代这些专项，关系列表 v1 证据不代替账户 v1 |
| R3-B 注册 | 本轮未运行 Go 测试或真实注册回归；空白/多字节用户名、密码字节边界与原 binding 规则并存、重名先哈希、软删除占名/大小写、未知哈希/存储故障分类、真实 Redis 429/Retry-After/fail-open 仅核对源码，尚无本轮运行证据 |
| R3-C 会话 | 本轮未运行 Go 测试或真实会话回归；登录空白/多字节与 bcrypt 失败分类、随机/存储/签发故障、刷新 CAS 并发/旧令牌复用/到期、轮换后用户读取和签发失败状态、撤销失败/重复退出、真实登录 429/Retry-After/fail-open 仅核对源码，尚无本轮运行证据；未核对目标库元数据 |
| R3-D 改密/注销 | 本轮未运行 Go 测试或真实回归；双重长度规则、多字节/空白密码、比较/哈希/读取/事务失败、密码 CAS 竞争、全部会话撤销与故障回滚、注销并发/软删除可见性和重复请求仅核对源码，未核对目标库元数据 |
| R3-E 改名/资料/头像 | 本轮未运行 Go 测试或真实回归；双重用户名长度/空白/多字节、重名/软删除占名/大小写/RowsAffected、资料空白与外部 URL、multipart/大小/读/Seek/存储错误、写库失败清理新对象和旧对象清理失败、并发替换及孤儿回收仅核对源码，未核对目标库元数据或磁盘状态 |
| R3-F1 跨模块读取 | 未运行 Go 测试或真实作者/资料/HTTP 回归；0/重复/空批次、nil 行、缺失/注销占位、数据库故障、统计顺序/遇错即停、三条统计/五条资料 SQL、可选依赖及 404/500 文案仅有源码证据；未核对目标库元数据 |
| R3-F2 用户持久化 | 目标库只读元数据与聚合核对不证明真实 ORM CRUD、1062/大小写/软删除占名、RowsAffected、keyset/批量投影、CAS/事务故障回滚或用户清扫；本轮只做源码/静态/构建，未运行任何 Go 测试或 HTTP/清扫服务 |
| R3-G1 JWT/认证 | HS256/claims/TTL、Secret 缓存/随机/回退、算法/期限解析、Authorization 格式/执行顺序、nil 校验依赖、上下文/401 文案及活动会话查询预算仅有源码兼容证据；未运行 Go 测试、真实 JWT/认证/HTTP 或密钥/随机失败回归，未访问目标库 |
| R3-G2 会话收口 | 目标库元数据与聚合只读核对不证明创建/签发失败残留、随机失败、刷新 CAS 竞争/重复使用/固定到期、活动会话校验/库故障、刷新后读取/签发失败、尽力撤销或改密/注销事务故障回滚；仅源码/静态/构建，未运行任何 Go 测试或真实 HTTP/会话回归 |
| R4-A1 公开读取 | 全局/作者 v1 实际续页、同时间 ID/毫秒/时区、全部非法游标/重复 query、残缺/软删/非 published 行、作者占位/批量、空页和统计依赖缺失、逐阶段错误及错误链优先级/文案、公开列表四条/空页一条 SQL 仅有源码证据；元数据核对不证明真实公开读取，未运行 Go 测试或 HTTP/数据库业务回归 |
| 删除测试后的边界 | 当前工作区与提交树均为 5 文件/36 函数；application/feed/service_test.go 的 4 个缓存并发/容量/取消函数与 video/video_repo_test.go 的 11 个 Video 专项由独立提交 `a483843` 删除。原 repo_test.go 的六个互动/关注流程与预算函数也不再保留；router 的旧关系 v1、Following 和缓存 HTTP 流程仍在。历史通过不代表已删除覆盖仍存在，不恢复删除的测试 |
| 迁移/工程 | 真实 down/dirty 中断、EXPLAIN 索引选择；历史残留库/MQ 资源按归属确认，不能仅凭年龄删除；换行符统一另行决策；页缓存非法参数错误包装 |
| 运维 | 采集器/监听/告警、恢复水位、事实/Outbox 保留清理、异常事件处置；processing/pending 不一致、最老积压和 DLQ 自动告警 |

### 5.3 验证与交付规则

2026-10-07 用户要求后续不运行任何 Go 单元测试，也不执行包含它们的全量 go test 或 race 命令。现有测试仅作迁移必要的编译/装配适配，不新增测试、扩展断言或恢复已删除专项；默认不执行 Go 测试。实现模块从 backend 直接执行：

```powershell
go vet ./...
go build ./...
```

最后运行 `git diff --check`；只有用户明确重新授权才执行测试，不因文档中的待办自行运行。文档修改只核对链接/锚点、围栏与差异；不新增临时脚本或日志文件，结果直接输出终端。报告区分源码、静态/构建与运行证据；未运行测试应写“未运行”，不能算通过或 SKIP，历史结果不替代当前验收。

启动新版 API/worker 前，核对目标 schema_migrations 与实际结构并应用必要迁移，再启动认识全部事件的 worker，最后 API。回退先停止新事实生产，处理/记录存量和重放清单，保留事实及消费者；不在运行期间 down 事实表。隔离测试只清理确认自有的 MySQL/Redis/MQ 资源。

完成一个独立模块后等待 review；明确指令后精确暂存/提交，默认不推送。正文仅保留当前计划和必要验收摘要，完成结果简述入 README，完整实现历史由 Git 追溯。

## 6. 全后端四层架构演进

### 6.1 源码基线与剩余边界

| 模块 | 已建立边界 | 剩余迁移 |
| --- | --- | --- |
| Feed | 独立四层、缓存/读取端口，Following 活动观看者接 Relation，作者经 Account Infrastructure 读取 | Infra 仍复用旧 video，Account 已持有唯一 User ORM/仓储；Following 视频 SQL 仍在 Video |
| Interaction | 六个 HTTP、ORM/SQL、事务、批量统计与获赞，资料关注计数接 Relation，资料结果实现 Account 领域端口 | 完整公开规则依赖 video；外层仍适配 video |
| Relation | 五个 HTTP/用例、v1 游标、独立读取/计数端口及唯一 Follow ORM/SQL；R2-C 已提交 | 专项验收缺口继续见第 5.2 节 |
| Account | R3-A/B/C/D/E/F1/F2/G1/G2 已提交；会话用例和唯一 AuthSession ORM/仓储已归 Account，旧 user/auth 已删除 | 旧 Video 输出转换/头像媒体实现留 R4，真实专项验收继续见第 5.2 节 |
| Auth | JWT/随机/刷新哈希及令牌签发实现归 infra/jwt；HTTP 认证/上下文消费 Account 会话校验小端口，旧 auth 已删除，G2 已提交 | 账户清扫用例留 R5，真实专项见第 5.2 节 |
| Video | R4-A1 公开列表/详情四层、小读端口与原游标；Domain 公开规则，HTTP DTO 独立，待 review | 本人列表/状态、草稿/媒体、写入与唯一 ORM/仓储分模块迁移；Feed 等旧消费者留 R4-D |
| Worker/Sweeper | 入口集中编排、小能力接口 | 用例、调度、消息 ACK/重试及媒体引用仓储分别归层 |

### 6.2 目标结构与依赖规则

参考 [GCFeed 工程规范](../../GCFeed/docs/engineering.md) 的职责，保持 GoFeed 模块化单体与现有工作区：

```text
internal/domain/{account,video,interaction,relation,feed}
internal/application/{account,video,interaction,relation,feed}
internal/infra/{persistence,cache,mq,storage,config,database,jwt}
internal/interfaces/{http,worker,jobs}
cmd/ 负责进程装配与生命周期；db/migrations/ 保留显式迁移
```

依赖方向为 `Interfaces → Application → Domain`；Infrastructure 实现内层端口。Domain 只依赖标准库，Application 使用领域模型及小能力接口，均不导入 Gin/GORM/Redis/AMQP 或旧业务包类型。领域实体、唯一一份表 ORM、HTTP DTO 分开维护，ORM 不加 Model 后缀；基础设施错误在外层转换。

过渡旧类型只在 Infrastructure 转换；不复制公开规则、增加逐条查询、通用事件/事务框架或微服务。GCFeed 的直接发事件、部分内层 infra/bcrypt 依赖不是 GoFeed 的可靠性/依赖规范。

### 6.3 R0–R6 执行顺序

| 阶段 | 独立交付与兼容重点 |
| --- | --- |
| R0 基线冻结 | Git/路由/DTO、游标、SQL 预算、事务及依赖图，保留用户改动 |
| R1 Interaction | 已完成本阶段 HTTP、持久化与统计迁移，外层账户/视频适配随其归属模块收口 |
| R2 Relation | R2-A/B/C 已提交；ORM/SQL、计数与 Following 活动观看者适配已收口，social 已删除 |
| R3 Account | R3-A/B/C/D/E/F1/F2/G1/G2 已提交，会话及持久化已迁入 Account；保留 CAS、哈希、事务与补偿 |
| R4 Video | R4-A1 公开读取已实现待 review；后续本人列表/状态、草稿上传、发布/删除与 Outbox 分别迁移；保留公开规则、旧游标、202、CAS/锁及文件补偿 |
| R5 Worker/Sweeper | 用例归 Application，消息/ACK/调度归 Interfaces，连接/confirm/存储归 Infra；保留租约接管与关闭顺序 |
| R6 收口 | 整理技术实现/HTTP 组合根、Feed 适配与文档，删除旧包和无用途过渡层 |

R 编号是架构路线，F 编号是功能路线。每个阶段可拆成多个独立 review 模块，四层化不以 Hot/推荐上线为前提。

### 6.4 已提交迁移摘要

| 模块 | 提交 | 实际完成边界 |
| --- | --- | --- |
| 测试精简 | `01fc0bb` | 保留 8 文件/57 个复杂及业务流程测试 |
| R1-A | `56c691a` | 六个互动 HTTP 接入四层，保留 v1 评论游标与响应 |
| R1-B1 | `7b01685` | 点赞/评论 ORM、直接读取及事务归 Interaction |
| R1-B2 | `a7bce52` | 非空批量统计固定两条 SQL、完整公开视频获赞读取、旧消费者外层转换 |
| R2-A | `0b08d92` | GET/PUT/DELETE 关注状态/写入归 Relation，持久化仍委托 social |
| R2-B | `f9481b2` | 两个匿名关系列表、独立读模型/位置/端口、分页与原 v1 游标归 Relation，复用旧仓储 SQL |
| R2-C | `ea36d40` | 唯一 Follow ORM、原 SQL 与计数归 Relation，资料/Following 接独立端口，删除旧 social；当时保留测试删除状态，7 文件/51 函数 |
| R3-A | `35a6fe0` | 三个匿名账户 GET、独立模型/端口、全量/分页双模式及原账户 v1 游标归 Account；复用旧仓储、视频计数和资料统计 |
| 后续测试精简 | `a483843` | 独立删除 Feed/Video 两个测试文件，当前保留 5 文件/36 函数；不将早先普通/race 结果称为当前运行验证 |
| R3-B | `a834d46` | 注册 POST 接入 Account 四层；独立注册规则、创建/哈希端口、原 binding/限流/响应，复用 User 仓储，删除旧注册方法 |
| R3-C | `f20dcdf` | 登录/刷新/退出接入 Account，独立凭据/会话/令牌与小端口，复用原会话/CAS/JWT，删除旧入口/DTO/助手；后端/API/必要文档共 19 文件 |
| R3-D | `4f4838b` | 改密/注销接入 Account，独立密码规则/输入、小密码端口与原子写入端口，复用原 CAS、软删除及全部会话撤销的同一事务；后端/API/必要文档共 17 文件 |
| R3-E | `f5c1260` | 改名/资料/头像接入 Account，独立资料/头像规则与小读写/存储端口，保留原 HTTP 解析、写入及文件补偿；后端/API/必要文档共 18 文件 |
| R3-F1 | `c335902` | 作者读取接独立 PublicAccountReader 并迁入现有 Account Infrastructure，统计改用领域小接口/结果，保留原批量、占位、nil 与错误转换；后端及既有文档改动共 11 文件 |
| R3-F2 | `267463e` | 唯一 User ORM 与全部 12 个仓储方法归 Account，装配接新仓储并删除旧 user；SQL/事务/错误保持，保留测试仅必要迁移适配；共 16 文件 |
| R3-G1 | `e84f783` | JWT 签发/解析/随机归 Infra，共享认证/上下文归 Interfaces 并消费独立 Validate 端口；保留 nil、原文案/顺序/claims；共 17 文件 |
| R3-G2 | `fe6959d` | 会话创建/轮换/校验归 Account Application，唯一 AuthSession ORM/六个仓储方法归 Persistence，随机/哈希/签发接 Infra JWT；原 SQL/CAS/错误/同一 tx 撤销保留，删除旧 auth/冗余转换；共 15 文件 |

R1-B2 资料统计按获赞→粉丝→关注读取，有效账户统计部分三条 SQL；空视频批次/账户 0 不查库，失败立即返回。R2-A 保留认证、自关注/活动用户校验、幂等关注/取关及写后独立计数；计数失败不回滚已完成关系变更。两项均未引入新缓存/事务/关注事件，配套文档提交为 `b0e8dd0`。实际验证与未覆盖范围统一见第 5 节。

R2-C、R3-A/B/C/D/E 均按用户指令提交，没有推送；范围与验证边界见第 6.8–6.13 节。R3-F1 已提交为 `c335902`，未推送；R3-F2 已提交为 `267463e`，未推送；R3-G1 已提交为 `e84f783`，未推送；R3-G2 已提交为 `fe6959d`，未推送；R4-A1 已实现，未暂存/提交，等待 review。范围及其余 R4–R6 计划见第 6.14 节，未实施模块仍独立推进。

### 6.5 必须保留的原子性与恢复语义

- 改密/注销与撤销全部会话保持同事务，应用服务不访问具体仓储 .db。
- `draft → processing + video.process`、`processing → published + video.published`、真实互动与事实保持同事务；领域前置检查不能代替仓储锁/权限/状态复核及 CAS。
- confirm、租约/attempt 围栏、确认重试发布后 ACK、有限重试/DLQ 和消费幂等不随目录迁移改变；派发完成不是消费完成。
- 保留完整公开视频、Following 当前关系/活动作者、缓存故障回源及迟到回填防护；数据库不可用按安全错误契约返回。
- HTTP/JSON、游标版本/范围、表名、状态/软删除、缓存 Key、消息 schema 与已应用迁移保持兼容。

### 6.6 最终完成条件

1. 五个业务模块 HTTP 与 worker/sweeper 的用例、输入适配各有四层归属。
2. Domain/Application 无实现依赖或旧类型；领域、ORM、DTO 分离。
3. 生产不再导入旧 user/auth/video/social/worker/sweeper；技术实现与 HTTP 输入/组合根各有归属，过渡适配按清单删除。
4. 账户/会话、视频/清扫、互动、Feed 分页/可见性、缓存与 Outbox/ACK 保留流程回归通过。
5. 源码、自动化、真实依赖和必要页面证据分别报告，未完成功能/专项继续保留。

### 6.7 R2-B 粉丝/关注列表与游标（已提交）

只迁 `GET /api/user/:id/followers` 和 `GET /api/user/:id/following`。R2-B 提交时链路为 `Relation Handler → Application → Domain ListReader → Infra 适配 → social.Repository`；R2-C 已将仓储直接归 Relation。独立公开资料/列表行/时间-ID 位置归 Domain，分页和原 v1 编解码归 Application，DTO 归 HTTP；内层没有旧 social 或 Gin/GORM 依赖。R2-B 删除旧 Controller/Service 及无用途响应/游标助手，当时关系 ORM/SQL、资料计数与 Following 适配留后续迁移。结果摘要见 README，源码入口见[导读第 9.2 节](./SOURCE_CODE_GUIDE.md#92-互动先保存关系再读取聚合)。

复用三个既有 HTTP 流程，仅作迁移必要适配，测试仍为 8 文件/57 个函数，没有新增分层单测或恢复已删除专项。迁移前先在真实 MySQL 运行旧 HTTP 并固定两个 v1 输出，迁移后直接用它们续页，且与新游标续页一致；覆盖毫秒精度、同时间 ID 倒序、列表/用户绑定和无版本旧格式拒绝。默认/显式 0 返回 20、limit=50、末页/空数组、注销对端及校验优先级已运行；粉丝与关注两个方向均直接断言两条 SQL。完整检查结果与跳过范围见第 5.1 节。

全部非法字段/版本/时间/ID 组合、存储失败分类与错误文案逐项注入、并发关注/注销及分页快照、超过 50 条关系的分页截断与大规模扫描尚未专项覆盖，继续列入第 5.2 节；不恢复已删除专项。实施轮未运行前端/浏览器、容量及部署验证。2026-10-07 按用户指令将后端、必要测试适配和 API 文档提交为 `f9481b2`，没有推送；提交轮未改 Go 源码，复用实施轮全量验证结果，检查暂存范围/空白及配套文档链接。后续 R2-C 的当前状态见第 6.8 节。

### 6.8 R2-C 关系持久化与旧 social 收口（已提交）

本模块已将关注持久化直接归 `infra/persistence/relation`，切换资料关注计数与 Following 活动观看者装配，确认生产和保留测试无引用后删除旧 social。五个关系 HTTP、Application 分页/游标及 JSON 契约保持原实现；没有开始 Account 或 Video 的整体迁移。2026-10-07 按用户指令将后端提交为 `ea36d40`，没有推送。

| 当前实现 | 迁移与保留边界 |
| --- | --- |
| [Follow ORM](../backend/internal/infra/persistence/relation/follow.go)、[Repository](../backend/internal/infra/persistence/relation/repository.go)、[列表读取](../backend/internal/infra/persistence/relation/list_reader.go) | 唯一 ORM 与活动账户、创建/物理删除/状态、两个计数、两个列表的原 SQL 迁到 Relation；TableName/唯一键/索引及 DDL 未改。直接实现 Domain Repository/ListReader/CountReader，删除 LegacyRepository、旧列表/位置转换与 social 包 |
| [Domain 端口](../backend/internal/domain/relation/repository.go) | 增加只含两个计数方法的 CountReader，资料适配器依赖独立能力；Domain/Application 仍无旧业务类型或 Gin/GORM/驱动依赖 |
| [资料统计适配器](../backend/internal/infra/persistence/interaction/legacy_reader.go)、[router](../backend/internal/router/router.go) | 注入 Relation 仓储；保持获赞→粉丝→关注、accountID=0 不查库、有效账户统计部分三条 SQL、失败立即返回及原字段。Account 用例未迁 |
| [FollowingReader](../backend/internal/infra/persistence/feed/following_reader.go) | 窄接口调用 Relation RequireActiveUser，将不存在映射 Feed 401、其他依赖失败映射安全 503；保持 JWT/session、私有头与独立游标。移除旧 social 和 GORM 判断依赖 |
| [保留 HTTP 流程](../backend/internal/router/e2e_test.go) | 只切换 Follow 夹具的导入与 ORM 类型；提交前原 social/repo_test.go 和迁移后的 repo_test.go 均已不存在，保留该删除状态。当时为 7 文件/51 个函数，没有新增测试或扩展断言；未经用户明确指令不新增单元测试，也不恢复已删除测试 |

Following 的完整视频查询仍在 [video/following_repo.go](../backend/internal/video/following_repo.go)，包括当前关系、活动作者、完整公开视频和发布时间-ID keyset。R2-C 只切换活动观看者依赖；视频 SQL 与 PublicVideoQuery 留到 Video/Feed 对应模块，没有复制 JOIN、调整读取策略或引入混合推拉/缓存。

保留唯一键下重复关注、物理取关、写后独立计数、活动对端过滤、原 v1 五字段与列表/用户绑定、关系时间-ID 严格倒序和 limit+1。Relation 400/404/500 文案及校验顺序未改，GORM/MySQL 错误由外层处理，列表原 GORM 不存在错误映射仍保留。既有 router 流程继续直接消费迁移前固定的两个 v1 游标，验证同刻 ID 边界、毫秒精度及与新游标一致续页；Following 非空 6/空页 3 的预算仍在该流程。双向列表各 2 的断言已随 repo_test.go 删除，资料统计部分 3 只核对源码，不将它们视为本轮运行覆盖。

实施前工作树干净；只读核对真实 feedsystem 的 schema_migrations 为 version=10、dirty=false，user_follows 为原四列、DATETIME(3)、原唯一键及双向分页索引，关系行数为 2，没有执行迁移或修改业务库数据。测试使用原有隔离数据库及自动清理流程。

用户切换 Go 1.27.1 windows/amd64 后，实施轮曾完成 57 函数版本的验证。提交轮发现 repo_test.go 已不存在，保留该删除状态，并对当前 51 函数版本从 backend 直接执行 `go vet ./...`、`go test ./...`、`go test -race -count=1 ./...`，均退出 0；普通使用 7 包缓存，race 重跑 7 包。补充普通 JSON 为 51 PASS/0 FAIL/0 SKIP，7 包缓存、0 包重跑；真实 MySQL/Redis/RabbitMQ 的保留流程参与 race 验证。此前 386/race 环境拒绝与中断不计最终通过证据，没有恢复已删除测试或新增断言；全部结果直接输出终端。验证证据与剩余缺口见第 5.1、5.2 节。前端、配置、DDL、缓存/MQ/热度、视频用例和部署未改；没有新增临时脚本或日志文件。后端已提交，本轮不推送、不开始 R3。

### 6.9 R3-A 三个匿名账户读取（已提交）

仅迁 `GET /api/user`、`GET /api/user/:id`、`GET /api/user/:id/profile`。当前链路为 [Account Handler](../backend/internal/interfaces/http/account/handler.go) → [Application](../backend/internal/application/account/service.go) → [Domain 端口](../backend/internal/domain/account/repository.go) → [Infrastructure 适配](../backend/internal/infra/persistence/account/legacy_reader.go) → 原仓储/统计能力，router 只切换这三个匿名 GET。2026-10-07 后端/API 已提交为 `35a6fe0`，没有推送。

| 已迁移边界 | 保留的契约 |
| --- | --- |
| Domain 独立公开账户/资料模型、ID 分页位置与读取/统计端口 | 内层只用独立模型；不导入旧 user/auth/video、Gin/GORM、bcrypt 或数据库驱动 |
| Application 三个读取用例、列表双模式、分页与原 v1 游标 | 不带 limit/cursor 保留历史全量；任一 query 存在进入分页。仅省略 limit 使用 20，显式空/0/超出 1–50 均为 400；先校验 limit 再游标。`id ASC`、`id > i`、limit+1、最后返回 ID 续页，保持 RawURLEncoding、v/k/i、version=1、kind=users 与原字段检查，合法旧 v1 继续可用 |
| Infrastructure 复用 [user.Repository](../backend/internal/infra/persistence/account/repository.go)、视频计数与现有资料统计 | 只转换旧 ORM/游标/指标，不复制 SQL，不新增 User ORM。资料保持账户→完整公开视频数→获赞→粉丝→关注及失败立即返回；视频数继续复用 PublicVideoQuery。正常装配资料共 5 条 SQL、统计部分 3 条；这是当前实现预算，不表示已有独立持续断言 |
| HTTP Account Handler/DTO 与 router 只切换三个匿名 GET | 保留 user/users/account 包装、avatar_url/bio 的 omitempty、四项零值统计、空数组、末页省略 next_cursor、状态码/错误文案与校验顺序；不暴露密码/软删除字段。当前 Gin 对 query 存在性的处理也须保留：`?cursor=` 进入分页，`?limit=` 拒绝 |

确认引用后已删除旧 Controller 的三个读取方法、Service 列表/资料用例、旧 pagination.go、读取响应和无用途助手。R3-A 当时保留旧 Service.GetByID、publicUser、登录/刷新/头像共享响应、仓储 UserCursor 及现有统计适配所需类型；旧 Service 构造器只保留仍用的仓储依赖。user.Repository 包括作者 GetByIDs 批量读取完全未改，未新增或迁移 User/AuthSession ORM。用户/会话写入、关系、Interaction、Following、Video/Feed、缓存/MQ/热度、前端、配置、DDL 与部署没有改动；后续会话入口清理见第 6.11 节。

账户旧 v1 兼容证据：对照迁移前 user/pagination.go 与当前 [account/cursor.go](../backend/internal/application/account/cursor.go)，编码、解码和 validUserCursor 三个函数在类型/错误名及领域位置返回转换归一后相同；v/k/i 的字段类型、标签与顺序、20/50、version=1、kind=users、非零 ID、RawURLEncoding 和原 map 字段检查保持不变，没有增加解码限制。user.Repository 分页 SQL 未改，仍为 id ASC、id > 游标 ID，Application 继续 limit+1 探测和返回末条 ID 续页。这是账户源码证据；当前保留流程没有固定旧账户 v1 真实续页用例，不将关系列表旧 v1 的通过当作账户兼容验收。

实施轮历史验证见第 5.1 节：当时 7 文件/51 函数的 vet、普通全量及无缓存 race 全量通过，JSON 为 51 PASS/0 FAIL/0 SKIP。提交轮遵循停止执行 Go 单元测试的新指令，只运行 vet/build 和差异检查，不执行 Go 测试；不能以历史真实依赖结果代替当前验收。

仅 user_repo_test.go 作构造器与新详情 Handler 装配适配；改用外部测试包以避免导入旧 user 的 Infrastructure 形成循环依赖，删除不再使用的视频计数 fake，原测试和断言范围不变。没有新增测试、扩展断言、恢复已删除专项或新增临时脚本/日志文件。Feed/Video 两个测试文件的删除未纳入 R3-A，随后已有独立提交 `a483843`；当前保留 5 文件/36 函数。资料预算/按步失败/可选依赖及账户旧 v1 实际续页缺口继续见第 5.2 节。R3-A 提交轮只提交该模块并分析下一步，没有实施 R3-B。

### 6.10 R3-B 注册接口（已提交）

仅将 `POST /api/user/register` 迁入现有 Account 四层，独立装配 RegistrationService/RegistrationHandler，复用公开账户模型及响应 DTO。三个匿名 GET 的用例与装配保持原状，注册不创建会话或令牌。2026-10-07 按用户指令将后端、必要测试适配与 API 提交为 `a834d46`，没有推送。

| 实现位置 | 源码兼容证据 |
| --- | --- |
| [Domain 注册规则/输入](../backend/internal/domain/account/registration.go)、[Creator 端口](../backend/internal/domain/account/repository.go) | 仅依赖标准库；公开模型不含密码/软删除。用户名先 strings.TrimSpace，再按 Go len 字节长度检查 3–32；密码不 Trim，按原字节长度检查 8–72。独立 CreateInput 只携带用户名与密码哈希 |
| [Application 注册用例/Hash 端口](../backend/internal/application/account/registration.go) | 规范化/业务校验→哈希→Create；只使用 context 与独立 Domain，不导入旧 user/auth/video、Gin/GORM、bcrypt 或驱动。没有预查重、重读、会话创建或外层事务 |
| [BcryptPasswordHasher](../backend/internal/infra/persistence/account/password_hasher.go)、[Creator 适配](../backend/internal/infra/persistence/account/legacy_creator.go) | 使用原 GenerateFromPassword/DefaultCost，复用 user.Repository.Create 与唯一 User ORM，不复制 SQL。旧 User、ErrUsernameTaken/ErrInvalidInput 只在 Infra 转换；保留原唯一键与 MySQL 1062 重名处理，包括软删除占名及原大小写语义 |
| [HTTP 注册 Handler](../backend/internal/interfaces/http/account/registration.go)、[DTO](../backend/internal/interfaces/http/account/dto.go)、[router](../backend/internal/router/router.go) | URL/匿名不变；ShouldBindJSON 与原 required/min/max 标签不变，先 binding 后 TrimSpace，不新增未知字段/尾部 JSON 限制。保留 201 + user 包装、公开字段及 avatar_url/bio omitempty，不暴露密码/软删除，不创建令牌或会话 |
| 注册错误规则与原限流中间件 | binding 失败为 400 invalid registration payload；业务输入错误为 400 invalid user input；重名为 409 username already exists；未知哈希/存储错误为 500 user operation failed。router 保留限流在 Handler 之前，原 `rl:v1:register:<IP>`、5 次/小时、429、Retry-After 与 fail-open 不变，中间件未改 |

HTTP 的 validator 字符串长度检查按 rune，Domain 注册规则的 Go len 按字节；这两步仍分别保留，尤其是含空格和多字节用户名。哈希继续先于 Create，包括重名请求；不新增提前查用户名优化，不把普通创建改成幂等写入。

确认生产与保留测试引用后，仅删除旧 user.Controller.CreateUser、user.Service.CreateUser 和无用途 CreateRequest；[保留用户流程](../backend/internal/infra/persistence/account/user_repo_test.go) 仅切换注册 Handler/夹具及所需公开模型，没有新增测试、扩展断言或恢复已删除专项。R3-B 当时保留用户名/密码修改、登录仍用的 ErrUsernameTaken、ErrInvalidInput、bcrypt、GetByID/GetByUsername、publicUser、共享响应、仓储及适配类型；旧写入消费者不反向导入 Infrastructure。后续会话入口清理见第 6.11 节。

静态/构建：使用 Go 1.27.1 windows/amd64、CGO=1，从 backend 直接执行 go vet ./...、go build ./...，均退出 0；默认缓存可用。go list 依赖闭包仅含标准库及 Account Domain/Application；24 项源码对照检查通过，确认原绑定标签、字节规则、公开字段、路由顺序及范围外源码不变。git diff --check、四份文档的 247 个本地链接/锚点与围栏检查通过；结果直接输出终端，没有临时脚本或日志文件。必要文档为 README、API、源码导读及本计划。

运行边界：未运行任何 Go 测试或全量/race/JSON 测试命令，也未运行真实 HTTP/MySQL/Redis 注册回归；未核对本轮目标库元数据。双重长度校验、含空白/多字节输入、重名先哈希、软删除占名/大小写、哈希/存储故障及真实限流故障行为仅有源码证据，详见第 5.2 节，不能把 vet/build 称为真实注册回归。

R3-B 实施与提交轮没有改动 R3-A 三个匿名读取及其游标/资料统计、登录/刷新/撤销、改密/注销、资料/头像与补偿、User/AuthSession ORM、关系、Interaction、Following、Video/Feed、缓存/MQ/热度、前端、配置、DDL 和部署。R3-B 提交轮没有修改 Go 源码或执行 Go 测试，沿用实施轮 vet/build 结果，检查精确暂存范围、空白和配套文档；当时只分析下一步，后续 R3-C 实施见第 6.11 节。

### 6.11 R3-C 登录、刷新与退出（已提交）

已仅迁 `POST /api/user/login`、`POST /api/user/refresh`、`POST /api/user/auth/logout` 到现有 Account 四层。独立凭据、会话快照/令牌结果和小端口供 Application 编排；R3-C 当时 Infrastructure 复用旧 User 仓储及 auth.SessionService/Repository，不迁移 ORM、SQL、JWT 中间件或全用户会话撤销事务；这些后续归属现见第 6.14 节 R3-F2/G1/G2。后端/API 与必要文档已提交为 `f20dcdf`，未推送。

| 当前入口/源码 | 必须保留的顺序和契约 |
| --- | --- |
| [Account SessionHandler.Login](../backend/internal/interfaces/http/account/session.go)、[SessionService.Login](../backend/internal/application/account/session.go) | 原登录限流→ShouldBindJSON→用户名 TrimSpace→GetByUsername→bcrypt.CompareHashAndPassword→旧 SessionService.Create→200。用户名/密码仍用原 required/min/max 标签；用户名 binding 后仅 TrimSpace，没有注册的业务字节长度检查；密码不 Trim。不复用 NormalizeRegistration，不新增空白/字节规则 |
| [会话创建用例](../backend/internal/application/account/session_lifecycle.go)、[JWT 签发](../backend/internal/infra/jwt/jwt.go) | 先生成 session ID，再生成刷新令牌，按原 SHA-256 哈希存储并创建七天会话，再签发十五分钟 HS256 JWT。保留 user_id/username/sid、原随机生成和密钥策略；JWT 签发失败时已写入会话不额外回滚或补偿 |
| Account SessionHandler/SessionService.UpdateRefreshToken、旧会话实现 | binding 后原样使用 refresh_token，不 Trim、不增加格式规则；先哈希并查活动会话→生成新刷新令牌→以原 id/user_id/expectedHash/未撤销/未到期条件做 CAS→读取当前用户→签发访问令牌。保持 session ID、原 expires_at，不延长七天期限；RowsAffected != 1 仍拒绝旧令牌复用/竞争 |
| 刷新在 CAS 后的失败分支 | 读取当前用户的任何错误均先尝试撤销该会话，忽略撤销错误后返回 401 invalid refresh token；访问令牌签发失败为 500 failed to create access token。轮换已提交，不增加外层事务、回滚旧哈希或重试，不能把用户读取提前到 CAS 之前 |
| Account SessionHandler.UpdateSessionRevocation、[Interfaces JWT 中间件](../backend/internal/interfaces/http/auth/jwt.go) | JWT/session 验证后检查当前 user/session 身份，只撤销当前会话，成功 204 空响应。仓储保留 id/user_id/未撤销条件及单行更新；所有撤销错误仍为 401 invalid or expired token，不改成幂等 204，不撤销其他会话 |
| HTTP DTO 与限流 | 登录/刷新仍 200 + access_token/refresh_token/expires_at/user；expires_at 是会话/刷新期限，公开 user 复用 Account DTO，avatar_url/bio 保留 omitempty。登录限流保留 `rl:v1:login:<IP>`、10 次/分钟、429/Retry-After/fail-open，并先于 binding；刷新和退出不新增限流 |

错误须按阶段分别映射，不能统一为注册的 500 回退：登录 binding 为 400 invalid login payload；用户不存在或任意 bcrypt 比较失败为 401 invalid username or password，其他用户读取错误为 500 failed to authenticate；会话创建及其中随机生成/落库/签发错误均为 500 failed to create session。刷新 binding 为 400 invalid refresh payload，轮换阶段任意错误（含数据库和随机生成失败）仍为 401 invalid refresh token；CAS 后用户读取/访问令牌签发分支按上表保留。退出继续保留原 JWT 缺失/格式/过期错误及撤销错误的 401 文案。

| 四层实现 | 实际边界 |
| --- | --- |
| [Domain 模型](../backend/internal/domain/account/session.go)与[端口](../backend/internal/domain/account/repository.go) | 独立凭据、会话快照和令牌结果；公开模型不携带密码哈希，仅依赖标准库，不添加 ORM/HTTP 标签。凭据只供内层密码比较，不写入公开 DTO |
| [Application 会话用例](../backend/internal/application/account/session.go) | 登录读取/比较/创建、刷新轮换/用户读取/失败撤销/签发和当前会话撤销；使用小端口并按阶段包装独立错误，仅依赖标准库及 Domain |
| [凭据适配](../backend/internal/infra/persistence/account/legacy_credentials.go)、[会话用例](../backend/internal/application/account/session_lifecycle.go)、[密码比较](../backend/internal/infra/persistence/account/password_verifier.go)、[令牌签发](../backend/internal/infra/jwt/access_token_issuer.go) | R3-C 当时委托旧 User 仓储、auth.SessionService/JWT 并在外层转换；F2/G1/G2 已迁入对应四层，仍保留 SHA-256、随机生成、TTL、原 SQL/CAS 和失败后的状态，旧转换已清理 |
| [HTTP 会话 Handler](../backend/internal/interfaces/http/account/session.go)、[DTO](../backend/internal/interfaces/http/account/dto.go)与[router](../backend/internal/router/router.go) | 登录/刷新保留原绑定及按阶段固定文案，退出读取原 JWT 上下文；只切换三个最终 Handler 和依赖装配。原 sessionService 继续服务全部 JWT 中间件、Following 和旧消费者 |

确认全部生产与保留测试引用后，仅删除旧 Controller.Login/UpdateRefreshToken/UpdateSessionRevocation、Service.Authenticate、无用途的 Service.GetByUsername 包装和 ErrInvalidCredentials、publicUser/loginResponse/handleLoginError、LoginRequest/RefreshRequest/LoginResponse/FindByIDResponse，以及 Controller 的 Sessions 字段/构造器参数。R3-C 当时保留仓储 GetByUsername、头像仍用的 Service.GetByID、改名/改密的 ErrUsernameTaken/ErrInvalidInput/bcrypt、改密/注销所需 SessionRepository.UpdateUserSessionRevocations、旧 SessionService/TokenPair/Claims/User/AuthSession 及其他共享响应；后续改密/注销入口迁移见第 6.12 节。保留测试只切换 newUserHTTPEngine 装配；测试函数与断言未改，没有新增或恢复测试。

R3-A/B、改名、改密/注销与全部会话撤销事务、资料/头像及补偿、用户/会话 ORM 和 SQL 收口、JWT 中间件/密钥配置、会话缓存、Relation/Interaction、Following、Video/Feed、缓存/MQ/热度、前端、配置、迁移和部署未改。

静态/构建：使用 Go 1.27.1 windows/amd64、CGO=1，从 backend 直接执行 go vet ./...、go build ./...，均退出 0；默认缓存可用。go list 的非标准库依赖闭包仅包含 Account Domain/Application。48 项源码对照检查通过，包括旧 binding/公开字段、执行顺序、阶段错误和响应、完整 router 差异，以及旧 auth/session、auth/jwt、JWT 中间件、限流、user.Repository 与 R3-A/B 实现不变。git diff --check、15 个 Go 文件的 gofmt 检查、19 个改动文件的范围/空白检查及四份文档的 273 个本地链接/锚点与围栏检查通过，暂存为 0。必要文档为 README、API、源码导读及本计划；结果直接输出终端，没有临时脚本或日志文件。

运行边界：未运行任何 Go 测试或全量/race/JSON 命令，未运行真实 HTTP/MySQL/Redis 会话回归，也未核对目标库元数据。随机/签发/存储故障、CAS 并发、到期边界、刷新后的失败状态、重复退出和真实限流仅有源码证据，详见第 5.2 节；不能把 vet/build 称为真实会话回归。当前仍为 5 个测试文件、36 个测试函数。实施轮完成后停止等待 review。2026-10-07 用户明确要求提交并继续下一步，提交轮 Go 源码未变，沿用实施轮 vet/build 结果，仅检查精确暂存范围与空白；19 个文件提交为 `f20dcdf`，未推送。后续 R3-D 见第 6.12 节。

### 6.12 R3-D 改密与注销（已提交）

已仅迁 `PATCH /api/user/auth/password` 与 `DELETE /api/user/auth` 到现有 Account 四层。原 JWT/session 中间件和上下文检查保留，两个业务事务移入 Infrastructure，使用明确的“更新并撤销全部会话”原子端口，复用原仓储方法与同一个事务句柄。后端/API 与必要文档共 17 文件已提交为 `4f4838b`，未推送。

| 入口/分支 | 保留的契约与顺序 |
| --- | --- |
| 改密校验 | JWT/session→当前用户身份→原 ShouldBindJSON。old_password/new_password 均保留 required,min=8,max=72 标签和原 JSON 规则，标签长度按 rune；binding 后仅检查新密码 Go len 8–72 字节。两个密码都不 Trim，不增加旧密码业务长度、ID 或 JSON 格式规则 |
| 改密编排 | 新密码字节校验→原 GetByID→bcrypt.CompareHashAndPassword→GenerateFromPassword/DefaultCost→事务；读取、比较、哈希仍在事务前。任意比较失败为 403 wrong password，哈希失败不进入事务，不创建会话/令牌 |
| 改密原子写入 | 同一 tx 的 user.Repository.UpdatePassword(id, expectedHash, newHash)→auth.SessionRepository.UpdateUserSessionRevocations(id)。保留原密码 CAS/单行语义和全部会话撤销；CAS 未匹配为 403 wrong password，撤销失败整体回滚。没有外层嵌套事务、锁、重读或重试 |
| 注销原子写入 | 当前用户身份后直接进入事务，不预读用户。同一 tx 先调用原 user.Repository.DeleteUser 软删除，再撤销该用户全部会话；任一失败整体回滚，不增加媒体删除、关系/视频级联或新的补偿 |
| 响应/错误 | 改密仍 200 + message: password updated; sign in again；注销仍空 204。binding 为 400 invalid password payload，新密码字节错误为 400 invalid user input，旧密码比较/CAS 为 403 wrong password，用户/原仓储未找到错误为 404 user not found，未知错误仍 500 user operation failed。身份缺失仍 401 invalid or expired token，原 JWT 缺失/格式/过期文案不改，不新增限流或幂等 204 |

| 四层实现 | 实际边界 |
| --- | --- |
| [Domain 新密码规则/输入](../backend/internal/domain/account/security.go)与[原子端口](../backend/internal/domain/account/repository.go) | 独立 PasswordChange、ErrWrongPassword 和新密码字节校验；原子端口只提供改密并撤销/软删除并撤销两项能力，Domain 仅依赖标准库，没有 ORM/HTTP 标签 |
| [Application](../backend/internal/application/account/security.go) | 新密码校验、凭据读取、密码比较/哈希、原子写入及直接注销编排；复用已有小 verifier/hasher，凭据按 ID 读取另用小端口；仅依赖标准库与 Domain，不接触事务、bcrypt、旧 user/auth/video 或驱动 |
| [凭据适配](../backend/internal/infra/persistence/account/legacy_credentials.go)与[事务适配](../backend/internal/infra/persistence/account/legacy_security.go) | 旧 User/密码哈希和 GORM 错误仅在外层转换；原两个事务体仅作类型/参数转换，同一个 tx 构造原用户与会话仓储，算法、仓储/SQL 和 ORM 不变 |
| [Handler](../backend/internal/interfaces/http/account/security.go)、[DTO](../backend/internal/interfaces/http/account/dto.go)与[router](../backend/internal/router/router.go) | 原身份/binding/响应/错误与安全固定文案，仅切换两个最终 Handler 和依赖装配；原中间件、注册、会话和三个匿名读取保持原契约 |

确认生产与保留测试引用后，删除旧 user.Controller.UpdatePassword/DeleteUser、user.Service.UpdatePassword/DeleteUser、UpdatePasswordRequest 和无用途旧 ErrWrongPassword/Controller 规则，以及旧 Service 不再使用的 auth/bcrypt/GORM 导入。R3-D 当时保留改名所需 ErrUsernameTaken/ErrInvalidInput、资料/头像用例、Service.GetByID、共享响应、user.Repository.GetByID/GetByUsername/UpdatePassword/DeleteUser 和 User/AuthSession ORM；后续三个入口迁移见第 6.13 节。两个原回滚测试仅装配新用例/切换调用，newUserHTTPEngine 仅改注销入口装配；全部断言和其他流程未改，没有新增或恢复测试，仍为 5 文件/36 函数。

静态/构建：Go 1.27.1 windows/amd64、CGO=1，从 backend 直接执行 go vet ./...、go build ./...，均退出 0，默认缓存可用；go list 非标准库依赖闭包仅含 Account Domain/Application。35 项源码对照检查通过，包括原 binding/业务字节规则、两个事务体转换后相同、HTTP 身份/绑定/成功顺序相同、原错误分类、完整 router 差异、24 个保护源码文件与保留断言不变。git diff --check、13 个 Go 文件的 gofmt、17 文件精确范围/空白及四份文档的 289 个本地链接/锚点与围栏检查通过，暂存为 0。同步 README、API、源码导读与本计划；结果直接输出终端，没有临时脚本或日志文件。

运行边界：未运行任何 Go 测试或全量/race/JSON 命令；未运行真实 HTTP/MySQL/Redis 改密/注销、多会话失效或故障回滚回归，未核对目标库元数据。原 CAS 与事务、软删除及错误转换只提供源码兼容证据，不能把 vet/build 或已有回滚测试文件称为本轮运行回归。多字节/空白、故障/并发、事务提交和全部会话撤销等验收缺口保留于第 5.2 节。

改名、资料/头像及文件补偿、User/AuthSession ORM/SQL 收口、用户清扫、JWT/session 算法/配置、Relation/Interaction、Following、Video/Feed、缓存/MQ/热度、前端、配置、迁移和部署未改。实施轮完成后停止等待 review。2026-10-07 用户明确要求提交并继续，提交轮代码未变，沿用实施轮 vet/build 结果并检查精确暂存范围/空白；17 文件提交为 `4f4838b`，未推送。后续 R3-E 见第 6.13 节。

### 6.13 R3-E 改名、资料与头像（已提交）

已仅迁 `PATCH /api/user/auth/name`、`PATCH /api/user/auth/profile`、`POST /api/user/auth/avatar` 到现有 Account 四层。所有账户 HTTP 已归 Account；User 仓储/ORM 与本地媒体实现继续复用，ORM/SQL 和其余消费者收口留待后续独立 review。后端/API 与必要文档共 18 文件已提交为 `f5c1260`，未推送。

| 入口/分支 | 保留的契约与顺序 |
| --- | --- |
| 改名 | 原 JWT/session→当前用户→ShouldBindJSON，new_username 保留 required,min=3,max=32 标签和原绑定规则。binding 后 TrimSpace，空值为 new username is required，其余按 Go len 3–32 字节检查。直接调用原仓储 UpdateName，保留唯一键/1062、软删除占名/大小写与 RowsAffected=0 的 404；不预查重、重读、改为幂等成功或撤销会话 |
| 资料 JSON | 原 JWT/session→当前用户→ShouldBindJSON，avatar_url/bio 保留 omitempty,max=512 与 omitempty,max=255。标签仍按 rune，binding 后分别 TrimSpace，仅将非空 bio/avatar_url 放入原更新 map；均空为 nothing to update，不能清空字段，不添加 URL/业务长度规则，不读用户/清理对象/撤销会话 |
| 头像 HTTP | JWT/session→当前用户→检查存储可用→MaxBytesReader→FormFile(file)→文件大小→前 512 字节→原扩展名/文件头→Seek。保留 10 MiB 文件、1 MiB multipart 开销、11 MiB 总请求限制、defer Close 和原读取/Seek/格式/413 分支，不增加解码规则 |
| 头像用例/补偿 | 校验后 GetByID→SaveAvatar(TrimSpace(filename))→对保存的 URL TrimSpace/非空校验→原 UpdateAvatar。写库/URL 校验失败时尝试删除新原始 URL，仍返回原错误；成功后旧 URL 非空且不同才尝试清理旧对象。两处删除错误均忽略，响应返回原保存 URL。失败不增加重试、事务、锁、CAS、持久补偿记录或新清扫机制 |
| 成功响应/鉴权 | 改名/资料仍 200 + 原 message，头像仍 201 + avatar_url。原 JWT 文案及身份缺失的 401 invalid or expired token 保留；不创建会话/令牌，不新增限流 |
| 错误映射 | 原绑定文案 invalid username payload/invalid profile payload、400 业务文案 new username is required/invalid user input/nothing to update/invalid avatar file、409 username already exists、404 user not found、413 avatar file too large 保留。头像缺存储/读取/Seek 的原 500 avatar storage unavailable/failed to read avatar upload 保留，其余未知错误仍 500 user operation failed；不暴露底层存储错误 |

| 四层实现 | 实际边界 |
| --- | --- |
| [Domain 改名/资料规则](../backend/internal/domain/account/profile.go)、[头像规则](../backend/internal/domain/account/avatar.go)与[ProfileWriter](../backend/internal/domain/account/repository.go) | 独立 ProfileChanges、改名/URL/非空字段规则及原头像大小/文件头；仅依赖标准库，不含 ORM/HTTP 标签 |
| [Application](../backend/internal/application/account/profile.go) | 使用小 AccountReader/ProfileWriter/AvatarStorage，编排三个用例及原头像补偿；存储可用状态供 HTTP 在解析前检查，不依赖旧 user/auth/video、Gin/GORM 或驱动 |
| [写入适配](../backend/internal/infra/persistence/account/legacy_profile.go)与[存储适配](../backend/internal/infra/persistence/account/legacy_avatar_storage.go) | 复用原 user.Repository.UpdateName/UpdateFields/UpdateAvatar 和 video.LocalStorage.SaveAvatar/RemoveAvatar；旧错误与类型只在外层转换，原 SQL/路径/文件名/随机与安全删除不变 |
| [Handler](../backend/internal/interfaces/http/account/profile.go)、[DTO](../backend/internal/interfaces/http/account/dto.go)与[router](../backend/internal/router/router.go) | 保留原 JSON/multipart 参数、认证、状态码与文案，只切换三个最终 Handler 和依赖装配。文件解析/Close/Seek 归 HTTP，保存/写库/清理归 Application |

确认生产与保留测试引用后，删除旧 user/controller.go、avatar.go、Service.UpdateName/UpdateProfile/UpdateAvatar、UpdateNameRequest/UpdateProfileRequest 和无用途旧头像/改名专用错误。保留原 User、user.Repository 全部方法/作者批量读取、旧创建适配仍用的 ErrUsernameTaken/ErrInvalidInput、PublishedVideoCounter/ProfileMetricsReader 及统计结果；旧 Service/GetByID/构造器仅供现有回滚测试夹具。全部 5 个测试文件与 36 个测试函数原样保留，没有新增、修改断言或恢复测试。

实施轮静态/构建：Go 1.27.1 windows/amd64、CGO=1，从 backend 直接执行 go vet ./...、go build ./...，均退出 0；默认缓存可用，go list 非标准库依赖闭包仅含 Account Domain/Application。36 项源码对照检查通过：DTO 标签、改名/资料/URL 规则、原头像校验/大小/HTTP 解析、读取/保存/写库/补偿、原错误映射和完整 router 差异一致；30 个保护源码及全部测试文件不变。git diff --check、12 个现有 Go 文件的 gofmt、18 路径精确范围/空白（14 个 Go 路径含 2 删除，4 文档）和四份文档的 308 个本地链接/锚点与围栏检查通过，实施轮结束时暂存为 0。必要文档为 README、API、源码导读与本计划；结果直接输出终端，没有临时脚本或日志文件。

运行边界：未运行任何 Go 测试或全量/race/JSON 命令；未运行真实 HTTP/MySQL/Redis/文件上传、改名/资料/头像与清理故障回归，未核对目标库元数据或磁盘状态。编译与源码对照不证明真实重名/软删除/大小写、RowsAffected、multipart 边界、存储故障、补偿或并发替换，待补项保留于第 5.2 节。

匿名账户读取、注册、登录/刷新/退出、改密/注销事务、User/AuthSession ORM/SQL 收口、本地媒体实现与孤儿回收/清扫、Relation/Interaction、Following、Video/Feed、缓存/MQ/热度、前端、配置、迁移和部署未改。实施轮完成后停止等待 review。2026-10-07 用户明确要求提交并分析后续重构，提交轮 Go 源码未变，沿用实施轮 vet/build 与源码兼容证据；检查 18 个精确暂存路径及 git diff --cached --check 后提交为 `f5c1260`，未推送，提交后工作树干净。后续只分析并更新文档，未开始 R3-F1 开发。

### 6.14 R3 后续收口与 R4–R6 重构路线

目标是将 GoFeed 全后端演进为 GCFeed 所采用的 Domain/Application/Infrastructure/Interfaces 模块化单体。保留现有五个业务模块、三个进程、Vue 客户端和数据契约；目录与职责统一后，再按独立 F 路线建设 Hot、推荐等新能力。

#### 源码依据与当前缺口

2026-10-07 路线分析读取 GCFeed 的[工程规范](../../GCFeed/docs/engineering.md)、[账户用例](../../GCFeed/apps/api/internal/application/account/service.go)、[账户仓储](../../GCFeed/apps/api/internal/infra/persistence/account/gorm.go)、[HTTP 组合根](../../GCFeed/apps/api/internal/interfaces/http/router/router.go)及[Worker 入口](../../GCFeed/apps/api/cmd/worker/main.go)，核对 GoFeed 的调用与迁移文件。借鉴四层职责及业务模块组织；GCFeed 的 Domain bcrypt、Application metrics 实现依赖、AutoMigrate、Model 后缀及保存后忽略发布失败的做法不作为 GoFeed 迁移规范。GoFeed 继续遵守第 6.2、6.5 节的依赖、显式迁移、唯一 ORM 和事务 Outbox 约束。

R3-E 提交后的生产源码盘点：Domain 20 文件、Application 20 文件，导入检查未发现框架、驱动或旧业务包依赖；这只是源码检查。旧 user/auth/video 分别仍被 9/5/20 个生产文件直接导入，保留测试另有 3/2/3 个导入文件。账户 HTTP 迁移完成不能视为账户持久化或全后端四层化完成。

| 耦合位置 | 当前源码事实 | 收口归属 |
| --- | --- | --- |
| 作者读取 | [Account 作者适配](../backend/internal/infra/persistence/account/legacy_author_reader.go) 通过独立 PublicAccountReader 读取公开模型，供 Video/Feed 使用；批量仍委托原 GetByIDs | R3-F1 已提交；R4 再消除旧 Video 输出转换 |
| 资料统计 | [Interaction 外层适配](../backend/internal/infra/persistence/interaction/legacy_reader.go) 直接返回 Domain Account ProfileMetrics；[Account 读取适配](../backend/internal/infra/persistence/account/legacy_reader.go) 保留错误转换 | R3-F1 已解除旧统计类型依赖，SQL 与统计顺序未变 |
| 用户持久化 | [唯一 User ORM](../backend/internal/infra/persistence/account/user.go)、[仓储](../backend/internal/infra/persistence/account/repository.go)已归 Account Persistence，账户适配、router、sweeper 和夹具装配已切换 | R3-F2 已提交；用户清扫用例/调度留 R5；会话 ORM 已随 G2 归 Account |
| 会话/JWT | [会话编排](../backend/internal/application/account/session_lifecycle.go)、[唯一 ORM](../backend/internal/infra/persistence/account/auth_session.go)与[会话仓储](../backend/internal/infra/persistence/account/session_repository.go)已归 Account；[JWT](../backend/internal/infra/jwt/jwt.go)/随机/哈希归 Infra，[HTTP 认证](../backend/internal/interfaces/http/auth/jwt.go)归 Interfaces | R3-G1/G2 已提交；旧 auth 与冗余转换已删除 |
| 视频及后台 | Video 混合规则/ORM/DTO/SQL/文件实现；Worker 混合用例/投递，Sweeper 混合用例/调度/引用 SQL | R4、R5 分别按读、写、异步处理和清扫拆分 |

#### R3-F1：账户跨模块读适配（已提交）

本模块已解除作者读取和资料统计对旧 user 类型的耦合，当时复用原 user.Repository，User/AuthSession ORM 与持久化迁移留后续模块。User 仓储/ORM 现已由下述 R3-F2 迁入 Account，SQL 未变。

| 改动位置 | 最小交付与兼容边界 |
| --- | --- |
| Domain Account | 增加独立 PublicAccountReader（GetByID/GetByIDs），复用 PublicAccount 和原统计端口/结果；不返回旧 User、凭据或软删除字段，不扩展列表 Reader，不引入实现依赖 |
| Account Infrastructure | Reader.GetByIDs 只调用一次原仓储的 id/username/avatar_url 投影，过滤 nil 行并保留原批量错误；空批次不查询。AuthorReader 保留返回 map、0 占位、非零 ID 首次出现去重、一次查询和缺失/注销占位；单个未找到仍返回原 ID，其他错误传播 |
| Interaction/Account 统计适配 | Interaction 实现 Domain Account ProfileMetricsReader 并直接返回 ProfileMetrics，账户统计/视频数适配参数和字段改用领域小接口；只删冗余结果转换，保留 nil 与 accountError。账户 0 不查统计库、获赞→粉丝→关注、遇错即停，视频数仍独立读取；原三条统计/五条资料 SQL 未改 |
| router 与引用清理 | 仅切换作者装配。全生产/测试引用确认后删除旧 video/user_author_reader.go、user.PublishedVideoCounter、user.ProfileMetricsReader 与 user.ProfileMetrics；旧 Video 输出转换留 Infrastructure，R4 再收口。User ORM、全部仓储方法、Service.GetByID/构造器、ErrUsernameTaken/ErrInvalidInput 与其他 DTO 保留；5 个测试文件原样保留，无必要夹具调整 |

保留 `/api/video`、Timeline、Following、公开资料及评论作者响应、JSON/omitempty、查询预算和公开/软删除过滤。不得增加逐作者 GetByID、预读、重读、缓存、事务、SQL 或新 JSON 规则；不改变账户 Handler/校验、会话、资料/头像补偿、Interaction/Relation 写事务、视频状态机、MQ、清扫调度、前端、配置、迁移和部署。

2026-10-08 仅格式化本模块 7 个 Go 文件，从 backend 直接执行 go vet ./...、go build ./...，均退出 0；内层依赖、完整引用、原仓储/SQL、作者批量逻辑、统计顺序/错误转换和保护范围核对通过，git diff --check 与文档链接/锚点、围栏检查通过。作者转换迁入现有包，没有新增单适配器包或循环依赖；Interaction EngagementReader、Relation 计数及写事务未改。

R3-F1 未运行任何 Go 测试、真实作者读取、资料统计或 HTTP 回归，未访问目标库或启动服务；构建与源码对照不能替代运行验收。原三份计划文档改动保留并同步；2026-10-08 用户要求提交并继续，提交轮 Go 源码未改，沿用实施轮结果，检查 11 个精确暂存路径与 git diff --cached --check 后提交为 `c335902`，未推送，提交后工作树干净。待补验收保留于第 5.2 节。

#### R3-F2：用户持久化（已提交）

唯一 [User ORM](../backend/internal/infra/persistence/account/user.go) 和原 [Repository](../backend/internal/infra/persistence/account/repository.go) 全部 12 个仓储方法已迁入现有 Account Persistence。User 名称、全部 GORM/JSON 标签及默认 users 表映射保留；不复制 ORM、添加别名或引入 AutoMigrate/DDL。GetUserListPage 改为接现有 Domain ListPosition，删除仅携带 ID 的旧 UserCursor 转换；原 keyset 条件、列投影、排序和 limit 不变。

Create/UpdateName 的 MySQL 1062 直接返回现有 Domain ErrUsernameTaken，保留原文案，删除重复旧错误转换；ErrInvalidInput 继续使用现有领域错误。原 username 唯一键、软删除占名、大小写、RowsAffected 和密码 CAS 实现未改。Creator/Reader/CredentialReader/ProfileWriter/SecurityWriter 复用同包新仓储；改密/注销仍在同一 tx 上更新账户并调用旧 auth.SessionRepository 撤销全部会话。RemoveExpiredUsers 的原事务、先清会话再硬删用户及返回计数保持原样；cmd/sweeper 仅切换仓储构造器，清扫用例/调度未迁。

确认生产及全部保留测试引用后删除旧 user 包、Service/GetByID/构造器、UserCursor 和无用途 FindByUsernameRequest/Response。账户流程测试移至 Account Infrastructure 外部测试包，两个回滚夹具直接用新 Repository.GetByID；注册/会话 HTTP 夹具仅换仓储装配，旧 ErrUsernameTaken 的单处断言符号改为同文案领域错误。router/sweeper 两个测试文件只替换 User ORM 命名空间；全部测试条件、预期值、流程及 5 文件/36 函数保持，无新增/恢复测试。

静态/构建：只格式化本模块 12 个 Go 文件，从 backend 执行 go vet ./...、go build ./...，均退出 0。32 项源码检查通过：原仓储全文仅包名/错误命名空间/位置签名变化，全部 SQL/投影/条件与 12 个方法、User ORM、密码 CAS、硬删除事务和两项安全事务均核对；40 个 Domain/Application 生产文件依赖符合约束，无旧 user 引用或循环依赖。AuthSession/会话/CAS/JWT、HTTP/校验、作者/统计算法、Interaction/Relation 写事务、视频状态机、媒体补偿、MQ/缓存/热度、清扫实现、前端/配置/部署/迁移未改。文档链接/锚点、围栏与 git diff --check 通过。

目标库仅只读核对 localhost:3306/feedsystem：schema_migrations=10、dirty=false，用户表 6 列/3 索引、会话表 7 列/4 索引与 000001 及当前迁移匹配；列/索引及聚合数据在实施前后保持一致，没有数据库写入。元数据读取不证明真实 ORM CRUD 或事务故障回滚；未运行任何 Go 测试、真实账户仓储/HTTP/清扫回归或服务。待补项保留于第 5.2 节。2026-10-08 用户要求提交并继续，提交轮 Go 源码未变，沿用实施轮检查结果，核对精确暂存范围及 git diff --cached --check 后提交为 `267463e`，共 16 文件，未推送；提交后工作树干净。

#### R3-G1：JWT 与认证适配（已提交）

JWT 的 Secret/Claims/GenerateToken/GenerateRefreshToken/ParseToken 已迁入 [infra/jwt](../backend/internal/infra/jwt/jwt.go)，全文仅改包名：保留 HS256、user_id/username/sid、十五分钟 access TTL、IssuedAt/NotBefore、JWT_SECRET 获取、原缓存/随机生成/回退与日志、算法检查及原解析规则。原 SessionService 三处随机调用与一处签发、Account AccessTokenIssuer 只换新实现引用；两次 32 字节随机生成顺序、SHA-256 hex、七天固定会话到期、入库后签发、刷新 CAS 和原失败残留均未改。

Authorization 与 Gin 上下文适配迁入共享 [Interfaces HTTP Auth](../backend/internal/interfaces/http/auth/jwt.go)，以仅含 Validate(ctx, sessionID, userID) error 的消费方 SessionValidator 接旧服务。构造时将接口内的 nil 指针归一为 nil，保留原具体服务缺失时拒绝认证；请求仍按原顺序检查 header/格式、解析令牌、检查校验依赖与 sid、查询活动会话，成功后写原三个上下文键并 Next。Claims/UserID/SessionID 获取规则、401 状态和三种文案、未知会话/库错误统一拒绝认证保持原样，无会话缓存或额外账户读取。

router 三处认证装配、Account/Relation/Interaction/Feed HTTP 及旧 Video Controller 仅改包/符号引用，URL、Timeline 匿名/Following 认证、JSON/omitempty、公开过滤与查询预算保持。确认全部生产/测试引用后删除旧 auth/jwt.go 与 middleware/jwt/jwt.go；没有旧别名或转发层。R3-G1 当时保留 auth/session.go 的唯一 AuthSession ORM、全部 SQL/仓储/轮换/Validate 和哈希，legacy_sessions/security 与账户/互动/关注用例、事务不变；这些后续归属见下述 G2。

两个保留测试文件仅作必要导入/ParseToken 符号适配，条件、预期值、断言与 5 文件/36 函数不变；无新增/恢复测试。仅格式化本模块 14 个 Go 文件，从 backend 执行 go vet ./...、go build ./... 均退出 0。24 项源码检查通过，40 个 Domain/Application 生产文件仅依赖标准库/Domain，236 个保护文件保持；JWT 全文、认证体/上下文、全部消费者、旧会话及 SQL 均按允许变换对照。文档链接/锚点、围栏和 git diff --check 通过。

未运行任何 Go 测试、真实 JWT/认证/HTTP 或密钥/随机失败回归，未访问目标库、写数据库或启动服务；源码与构建不能称为真实认证回归，待补项见第 5.2 节。2026-10-08 用户要求提交并继续；提交轮代码未变，沿用实施轮结果，核对 17 个精确路径与 git diff --cached --check 后提交为 `e84f783`，未推送，提交后工作树干净。

#### R3-G2：会话用例与持久化（已提交）

[Domain 会话输入/结果](../backend/internal/domain/account/session.go)增加 SessionCreateInput，读取和写入分别通过 [SessionReader/SessionWriter](../backend/internal/domain/account/repository.go)，复用现有 Session/TokenPair/SessionLifecycle；不返回 ORM、完整会话行或公开刷新哈希。创建/轮换/校验编排归 [Application SessionLifecycleService](../backend/internal/application/account/session_lifecycle.go)，随机生成、刷新哈希及访问令牌签发分别使用小能力端口，内层只依赖标准库与 Domain。

创建仍先生成 session ID，再生成刷新令牌，两次均委托原 32 字节随机实现；SHA-256 hex 后保存七天固定到期的会话，再签十五分钟 access token。签发失败时已写入会话不增加回滚/补偿。轮换仍原样使用 refresh_token，空值不查库；先查活动会话→生成新令牌→以旧哈希 CAS，返回原 ID/到期，再由原账户用例读当前账户→签 token。原阶段错误包装、读取失败尽力撤销、CAS 后读取/签发失败的残留及退出非幂等行为保留。Validate 的 sid/userID 空值检查与单次活动查询保持，无账户预读/重读、缓存或外层事务。

[唯一 AuthSession ORM](../backend/internal/infra/persistence/account/auth_session.go)保留完整字段/标签及默认 auth_sessions 映射；[SessionRepository](../backend/internal/infra/persistence/account/session_repository.go)保留原六个方法、SQL/条件/更新列、时间获取、CAS/RowsAffected 与未知错误传播。输入/读取只转换现有领域值；未找到/零行结果直接返回旧外层转换所用 Domain ErrInvalidSession，原 HTTP 401/500 分类和文案保持。改密/注销仍用同一 tx 的新会话仓储撤销全部会话，两项事务体仅换构造器；User 仓储的清会话→硬删用户事务原样保留。

随机与 SHA-256 hex 实现经 [Infra JWT 刷新能力](../backend/internal/infra/jwt/refresh_token.go)接小端口，[AccessTokenIssuer](../backend/internal/infra/jwt/access_token_issuer.go)直接实现原签发端口；G1 的 JWT/Secret/解析/随机函数及 HTTP 认证/上下文代码未改。router 仅切换会话/签发装配。全生产及保留测试引用确认后删除旧 auth、legacy_sessions 及持久化 issuer 转换，无复制 ORM、旧别名或转发层；其他 legacy_* 文件仅保留已有名称，Video 输出/媒体边界留 R4。

仅账户仓储一个保留测试文件适配会话类型、构造器和端口装配，条件、预期值、断言与全部 5 文件/36 函数不变，无新增/恢复测试。仅格式化本模块 10 个 Go 文件，从 backend 执行 go vet ./...、go build ./... 均退出 0；32 项源码检查通过，41 个内层生产文件依赖与 242 个保护文件核对，完整引用、ORM/六个方法/SQL、用例顺序/错误、两项事务及 HTTP/DTO/公开过滤/查询预算均对照。文档链接/锚点、围栏及 git diff --check 通过。

实施前后仅 SELECT 核对 localhost:3306/feedsystem：schema_migrations=10、dirty=false，auth_sessions 七列/四索引、users 六列/三索引符合原迁移；元数据及 users 总数/软删 19/0、会话总数/撤销 16/3 两次相同，无数据库写入。未运行任何 Go 测试、真实会话/认证/HTTP/事务或随机/签发失败回归，未启动服务；元数据、构建与源码对照不能称为真实业务回归，待补项见第 5.2 节。实施轮必要文档同步后停止等待 review；2026-10-08 用户要求提交并继续，提交轮源码未改，核对 15 个精确路径及 git diff --cached --check 后提交为 `fe6959d`，未推送，提交后工作树干净。随后仅实施下述 R4-A1。

#### R3 收口后的进入条件

R3-G2 已按用户指令提交；当前只实施 R4-A1，之后仍须逐模块 review/明确提交。结构迁移完成不代表第 5 节真实兼容与故障专项已验收。

F2/G2 仅改变代码归属，ORM 名称保持 User/AuthSession、每张表只保留一个定义；不能通过复制 ORM、长期兼容别名或跨层仓储嵌套解决编译问题。R3-F2/G2 已分别按 AGENTS 只读核对目标 schema_migrations、实际列/索引与迁移文件；后续涉及数据库的模块实施前仍须重新核对目标库。若发现需要改表或改变行为，另立迁移/修复模块，不夹带于本路线。

#### R4–R6：从账户收口推进到全后端四层

下表是后续拆分路线，各子模块实施前重新冻结精确接口/调用范围；不一次迁完 Video 或全部后台任务。

| 模块 | 独立迁移单元 | 必须保留的边界 |
| --- | --- | --- |
| R4-A Video 读取 | R4-A1 已发布详情/公开列表已实现待 review；R4-A2 本人列表、R4-A3 处理状态未实施，分别 review。Domain 建状态/公开读模型，Application 建读取与原视频游标，HTTP 分离 DTO，Infra 先委托原仓储 | 原 `/api/video` 全局/作者两种分页范围、原游标版本/范围、完整公开规则、作者占位、批量作者/互动查询与错误顺序；不与 Feed 游标混用 |
| R4-B 草稿与媒体 | 创建/读取草稿；视频/封面上传与替换分别 review；LocalStorage 实现及媒体安全规则归 Infra/Domain，由小保存/删除端口编排 | 原 multipart 限制、文件头/扩展名、所属用户/草稿绑定、存储相对路径、original_name、保存→绑定→清理与失败补偿；不增加持久补偿、分片上传或共享存储新功能。Account 头像改接同一迁入的存储实现 |
| R4-C 发布与删除 | 草稿发布；丢弃草稿；已发布删除分别 review。应用使用明确原子写端口，仓储复核权限/状态 | draft→processing 与 video.process Outbox 同事务、202、processing/purging 不可逆性、锁/CAS、重复调用与软删除；不直接发 MQ 替代 Outbox |
| R4-D Video 持久化及消费方 | 唯一 Video/OutboxEvent ORM、CRUD/公开/Following 查询、Outbox/租约仓储按方法族分模块迁入 Infra；Feed/Interaction/作者适配接领域小端口 | PublicVideoQuery/IsPublicVideo 的 SQL 与内存规则保持唯一对应；Following 活动观看者/关系/作者、缓存命中 MySQL 复核、投影/扫描与 SQL 预算不变；迁库方法族后原实现删除，不长期双份维护 |
| R5-A 视频 Relay/处理 | Relay 调度与消息转换归 Interfaces；领取/路由/处理用例归 Application，claim/confirm 状态持久化归 Infra。视频处理与卡片预热各自 review | published CAS 与 video.published Outbox 同事务、attempt/租约围栏、至少一次、Return/mandatory/confirm、确认重试发布后 ACK、有限退避/DLQ/重连；不将 dispatched 当消费完成 |
| R5-B 已分层异步能力 | 互动 Relay、卡片预热、热度消费者逐一收口外层投递/ACK，Application 已有用例继续复用 | interaction_outbox 事务、去重/正负贡献/绝对到期及 coverage=unverified 不变；不附带 F4-B2 重建或开放 Hot |
| R5-C Sweeper | 用户回收、已删视频、草稿租约回收、孤儿媒体分别 review；用例归 Application，RunEvery/调度归 Interfaces，媒体引用 SQL/安全文件操作归 Infra | 保留宽限期、软删引用、claim/续租/围栏、先删媒体后确认、purging 不可逆、24 小时孤儿宽限/100 候选上限、失败重试与 shutdown；不改变数据回收策略 |
| R6-A 技术包与装配 | 分别收口 config/db、Redis Runtime/缓存中间件、MQ Runtime/拓扑、日志/就绪与 HTTP 错误适配；HTTP 组合根最终归 interfaces/http/router，cmd 保留进程资源装配与生命周期 | 每次只迁一种技术实现，配置键、启动目录、健康/就绪契约、限流 Key/429/Retry-After/fail-open、资源关闭顺序与部署入口保持兼容；不引入新容器或恢复已删开关 |
| R6-B 最终审计 | 按 import/引用清单删除旧 video/worker/sweeper 及无用途 legacy_*；对照五模块/三个进程的完整依赖图和文档 | 内层无旧类型/框架/驱动，唯一 ORM/公开规则/事件实现，无循环或逐条查询，所有 HTTP/后台输入经对应 Application；保留尚未完成的功能与真实验收缺口 |

#### R4-A1：已发布详情与公开列表（已实现，待 review）

仅迁匿名 `GET /api/video` 与 `GET /api/video/:id`。全局列表和按 author_id 的作者页使用同一分页用例；author_id 缺失/空/0 仍为全局，不新增无参数全量模式。本人列表、处理状态、所有草稿/媒体/发布/删除入口及 Video/Outbox ORM 与持久化 SQL 保留后续独立模块。

| 归属 | 当前实现 |
| --- | --- |
| [Domain Video](../backend/internal/domain/video/video.go) | 独立 PublicVideo/VideoItem/Author/EngagementCounts/ListPosition、状态和小读取端口；唯一内存公开规则，不含 ORM/JSON 标签，只依赖标准库 |
| [Application 读取](../backend/internal/application/video/service.go)、[原 v1 游标](../backend/internal/application/video/cursor.go) | 详情/公开列表编排，默认/显式 0 为 20、最大 50、limit+1，外部游标只在 Application；仅依赖标准库/Domain |
| [Infrastructure 读取](../backend/internal/infra/persistence/video/reader.go)、[作者/互动](../backend/internal/infra/persistence/video/enrichment.go)、[错误转换](../backend/internal/infra/persistence/video/errors.go) | 各委托一次原 Video Repository、Account AuthorReader 与 Interaction EngagementReader，仅转换旧类型/错误；无新 SQL、事务、缓存、预查询或重读 |
| [HTTP Handler](../backend/internal/interfaces/http/video/handler.go)、[DTO](../backend/internal/interfaces/http/video/dto.go) | 保留原 Gin Query/路径/limit/author_id 解析、公开匿名访问、状态码/文案与字段顺序；内层不承载 HTTP DTO |
| [router](../backend/internal/router/router.go)与旧 Video | 只切换两条公开 GET 装配；旧状态常量引用 Domain 原值、IsPublicVideo 经标量桥接复用 Domain 规则。原 SQL、Feed/Following、账户资料和评论作者装配不变 |

源码兼容证据：原 RawURL Base64 v1 的 v/k/a/p/i 字段顺序、public 禁止 a、author/mine 要求 a、版本/时间/ID/范围检查原样保留；不增加严格解码、长度或重复 query 规则，不与 Feed 游标混用。完整公开规则仍要求 published、未软删、非 nil/非零发布时间及六项媒体值非空；SQL 作用域全文未改，旧 Video/Feed 的内存检查只做字段桥接，规则未复制。

列表仍先过滤/limit 截断→互动→批量作者，最终页作者首次出现去重，空页不读作者/统计；详情先校验完整公开→作者→互动。Account 的 0/注销占位、单次 GetByIDs 与三字段投影保持；统计可选 nil 仍零值，故障以双重 %w 返回，禁止零值掩盖。按源码，正常非空公开列表四条 SQL（视频一条、互动两条、作者一条），空页一条；详情作者非零时同样四条，无新增读取。HTTP 仍先 limit 文本后 author_id，再由用例检查 limit 范围/游标；空 items 是数组，next_cursor 仅非空时输出，author.avatar_url 保持无 omitempty。

错误仍按无效输入 400→未找到 404→禁止 403→冲突 409→统计不可用 503→未知 500 判定；详情仓储未找到先归一，跨端口转换保留原文案与 cause。统计错误包裹未找到仍优先 404 video not found，普通统计故障仍 503 engagement stats temporarily unavailable，未知仍 500 video operation failed。

确认全部生产/测试引用后仅删除旧 Controller 两个公开 GET、Service 两个公开用例、独占 publicCursorScope/toVideoItem/parseAuthorID 和旧 VideoReader；完整 Repository 方法与唯一 ORM 未删。旧 mine 专用列表组装/游标、共用 DTO/错误/作者统计接口、写入/状态方法和 Feed 输出适配继续保留；后续迁移对应消费者后再清理。全部 5 个保留测试文件/36 个函数原样保留，本轮没有签名装配适配、新增/恢复测试或断言变化。

静态/构建：仅格式化本模块 12 个 Go 文件，从 backend 执行 go vet ./...、go build ./...，均退出 0；45 项源码对照、44 个 Domain/Application Go 文件依赖与 248 个保护文件检查通过，25 个旧 Service/14 个旧 HTTP 方法体原样保留。文档链接/锚点/围栏及 git diff --check 通过。实施前后仅 SELECT 核对 feedsystem 版本 10、dirty=false，videos 21 列/8 索引符合原迁移，元数据与 published 总数/软删 3/0 两次相同，无数据库写入。

未运行任何 Go 测试、race、JSON 测试、真实视频/作者/互动读取、HTTP、游标续页或数据库故障回归，未启动服务；源码与构建、元数据核对不能称为真实读取回归，待补项见第 5.2 节。本模块未暂存/提交/推送，停止等待 review；不开始 R4-A2/A3、R4-B 或其他模块。

R4-D 的各仓储方法族在对应读写/后台用例切换后逐个归位，具体前后次序以依赖闭包重新冻结；允许未迁消费者经外层小适配复用唯一新实现，不复制 SQL。R5 迁完各用例后，cmd/worker.startWorkers 仍统一启动、取消、等待并关闭资源，不能为目录整齐拆散生命周期。

最终目录以第 6.2 节为准，业务规则/用例/ORM/输入适配各有唯一归属。结构完成需要源码依赖与引用检查；兼容验收仍需账户/视频/关注/互动/Feed/异步处理及清扫的真实流程与故障证据。当前禁跑 Go 测试的约束继续有效，未重新授权前只做静态/构建，不能宣布真实行为已回归或第 6.6 节全项完成。本轮按用户要求提交 R3-G2，并仅实施 R4-A1；未开始其余模块、运行服务/测试或修改数据库/配置/前端，R4-A1 未暂存/提交/推送，留待 review。
