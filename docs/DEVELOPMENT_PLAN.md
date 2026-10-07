# GoFeed 开发计划

> 更新日期：2026-10-07。本文维护未完成任务、必要设计和验收缺口；已实现能力见 [README](../README.md)，接口见 [API](../API.md)，源码链路见 [源码导读](./SOURCE_CODE_GUIDE.md)，工作规则见 [AGENTS](../AGENTS.md)。

## 阅读导航

- [1. 当前基线与优先顺序](#1-当前基线与优先顺序)：先确认已实现和未实现的边界
- [2. Feed 缓存后续工作](#2-feed-缓存后续工作)：一致性、容量和收益缺口
- [3. F2–F6：Feed 派生能力](#3-f2f6feed-派生能力)：指标、Following、热度重建与推荐
- [4. 其他待开发与评估模块](#4-其他待开发与评估模块)：按需求独立立项
- [5. 待补验证与交付门槛](#5-待补验证与交付门槛)：历史证据、剩余专项和检查要求
- [6. 全后端四层架构演进](#6-全后端四层架构演进)：分层规则、迁移路线和当前模块

R2-B 已提交为 `f9481b2`，[R2-C 关系持久化与旧 social 收口](#68-r2-c-关系持久化与旧-social-收口已提交) 后端已提交为 `ea36d40`。架构下一模块为 [R3-A 三个匿名账户读取](#69-r3-a-三个匿名账户读取未实施)，尚未实施。Feed 功能路线的下一步：[F4-B2 事实重建与 MySQL 快照](#38-f4-b2事实重建与-mysql-快照的下一步边界未实现)。两条路线分别 review，不同时实施。

## 1. 当前基线与优先顺序

MySQL 是唯一业务事实源；Redis 是可重建缓存/索引及限流存储；RabbitMQ 承担至少一次投递。保留事务 Outbox、publisher confirm、租约/attempt 围栏、消费 CAS 幂等、有限重试与 DLQ。

| 路线 | 当前状态 | 下一动作 |
| --- | --- | --- |
| 架构 R1 | Interaction 六个 HTTP、ORM/直接读取、事务、批量统计与获赞读取已迁移 | 外层 user/video 转换随 R3/R4 收口 |
| 架构 R2 | R2-A/B/C 已提交，关系 ORM/SQL、资料计数及 Following 活动观看者已收口，social 已删除 | 下一步 R3-A 三个匿名账户读取，尚未实施 |
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
| 2026-10-07 R2-C 实施轮 | Go 1.27.1 windows/amd64、CGO=1，从 backend 直接执行 vet、普通全量及 `go test -race -count=1 ./...`，均退出 0，普通/race 各重跑 8 个测试包；补充 `go test -json ./...` 为 57 PASS/0 FAIL/0 SKIP，8 包重跑、0 包缓存。真实 MySQL/Redis/RabbitMQ 参与；文档 219 个本地链接/锚点及围栏检查通过 | 当时为 8 文件/57 函数，两个固定旧 v1 续页与新游标一致，双向列表各 2、Following 非空 6/空页 3 SQL；后续提交轮 repo_test.go 已不存在，当前 7 文件/51 函数，不将已删除断言称为持续覆盖 |
| 2026-10-07 R2-C 提交轮 | 保留 repo_test.go 删除状态后，从 backend 直接执行 vet、普通全量及无缓存 race 全量，均退出 0；普通 7 包缓存，race 重跑 7 包。补充普通 JSON 为 51 PASS/0 FAIL/0 SKIP，7 包缓存、0 包重跑；保留的真实 MySQL/Redis/RabbitMQ 流程参与 race 验证 | 当前 7 文件/51 函数；固定旧 v1 仍在 router 流程成功续页，与新游标一致。未恢复测试或增加断言，已删除列表预算/完整互动流程不算当前覆盖；没有前端、容量与部署验收 |

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
| 删除测试后的边界 | 当前 7 文件/51 函数；原 repo_test.go 的完整公开视频互动边界、视频硬删除级联、点赞/评论/关注 HTTP 完整流程与列表查询预算六个函数不再保留。旧关系 v1 游标和 Following 动态关系/可见性/认证/预算仍在 router 流程，历史通过记录不代表已删除覆盖仍存在；不恢复删除的测试 |
| 迁移/工程 | 真实 down/dirty 中断、EXPLAIN 索引选择；历史残留库/MQ 资源按归属确认，不能仅凭年龄删除；换行符统一另行决策；页缓存非法参数错误包装 |
| 运维 | 采集器/监听/告警、恢复水位、事实/Outbox 保留清理、异常事件处置；processing/pending 不一致、最老积压和 DLQ 自动告警 |

### 5.3 验证与交付规则

常规迁移复用保留的 HTTP/业务/真实依赖测试，只做必要适配；不按层、方法、字段或简单参数新增重复单测，不恢复已删除专项。实现模块从 backend 直接执行：

```powershell
go vet ./...
go test ./...
go test -race -count=1 ./...
```

race 用于共享行为变更；测试范围服从用户当轮指令。文档修改只核对链接/锚点、围栏与 `git diff --check`；不新增临时脚本或日志文件，结果直接输出终端。报告区分源码实现、缓存/重跑、真实依赖、fake/mock、跳过及未覆盖项，历史结果不替代当前验收。

启动新版 API/worker 前，核对目标 schema_migrations 与实际结构并应用必要迁移，再启动认识全部事件的 worker，最后 API。回退先停止新事实生产，处理/记录存量和重放清单，保留事实及消费者；不在运行期间 down 事实表。隔离测试只清理确认自有的 MySQL/Redis/MQ 资源。

完成一个独立模块后等待 review；明确指令后精确暂存/提交，默认不推送。正文仅保留当前计划和必要验收摘要，完成结果简述入 README，完整实现历史由 Git 追溯。

## 6. 全后端四层架构演进

### 6.1 源码基线与剩余边界

| 模块 | 已建立边界 | 剩余迁移 |
| --- | --- | --- |
| Feed | 独立四层、缓存/读取端口，Following 活动观看者接 Relation | Infra 仍复用旧 video/user；Following 视频 SQL 仍在 Video |
| Interaction | 六个 HTTP、ORM/SQL、事务、批量统计与获赞，资料关注计数接 Relation | 完整公开规则依赖 video；外层仍适配 user/video |
| Relation | 五个 HTTP/用例、v1 游标、独立读取/计数端口及唯一 Follow ORM/SQL；R2-C 已提交 | 专项验收缺口继续见第 5.2 节 |
| User/Auth | 资料统计端口、DB 会话/刷新轮换 | Service 具体仓储与事务、Controller 登录/刷新编排、会话模型/持久化拆分 |
| Video | 仓储、作者/统计小接口 | Domain/ORM/DTO 混合、GORM 错误、上传/绑定/补偿编排与媒体实现 |
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
| R3 Account | 先 R3-A 三个匿名账户读取，再分模块迁注册、登录、刷新、撤销、改密/注销；保留会话轮换 CAS、哈希、事务与头像补偿 |
| R4 Video | 先模型/读取，再草稿上传、发布/删除与 Outbox；保留公开规则、旧游标、202、CAS/锁及文件补偿 |
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
| R2-C | `ea36d40` | 唯一 Follow ORM、原 SQL 与计数归 Relation，资料/Following 接独立端口，删除旧 social；保留测试删除状态，当前 7 文件/51 函数 |

R1-B2 资料统计按获赞→粉丝→关注读取，有效账户统计部分三条 SQL；空视频批次/账户 0 不查库，失败立即返回。R2-A 保留认证、自关注/活动用户校验、幂等关注/取关及写后独立计数；计数失败不回滚已完成关系变更。两项均未引入新缓存/事务/关注事件，配套文档提交为 `b0e8dd0`。实际验证与未覆盖范围统一见第 5 节。

R2-C 按用户指令提交，没有推送；实现与验证边界见第 6.8 节。下一模块 R3-A 的最小范围见第 6.9 节，本轮只准备实施 prompt，不开始新模块。

### 6.5 必须保留的原子性与恢复语义

- 改密/注销与撤销全部会话保持同事务，应用服务不访问具体仓储 .db。
- `draft → processing + video.process`、`processing → published + video.published`、真实互动与事实保持同事务；领域前置检查不能代替仓储锁/权限/状态复核及 CAS。
- confirm、租约/attempt 围栏、确认重试发布后 ACK、有限重试/DLQ 和消费幂等不随目录迁移改变；派发完成不是消费完成。
- 保留完整公开视频、Following 当前关系/活动作者、缓存故障回源及迟到回填防护；数据库不可用按安全错误契约返回。
- HTTP/JSON、游标版本/范围、表名、状态/软删除、缓存 Key、消息 schema 与已应用迁移保持兼容。

### 6.6 最终完成条件

1. 五个业务模块 HTTP 与 worker/sweeper 的用例、输入适配各有四层归属。
2. Domain/Application 无实现依赖或旧类型；领域、ORM、DTO 分离。
3. 生产不再导入旧 user/auth/video/social，过渡适配按清单删除。
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
| [保留 HTTP 流程](../backend/internal/router/e2e_test.go) | 只切换 Follow 夹具的导入与 ORM 类型；提交前原 social/repo_test.go 和迁移后的 repo_test.go 均已不存在，保留当前删除状态。当前为 7 文件/51 个函数，没有新增测试或扩展断言；未经用户明确指令不新增单元测试，也不恢复已删除测试 |

Following 的完整视频查询仍在 [video/following_repo.go](../backend/internal/video/following_repo.go)，包括当前关系、活动作者、完整公开视频和发布时间-ID keyset。R2-C 只切换活动观看者依赖；视频 SQL 与 PublicVideoQuery 留到 Video/Feed 对应模块，没有复制 JOIN、调整读取策略或引入混合推拉/缓存。

保留唯一键下重复关注、物理取关、写后独立计数、活动对端过滤、原 v1 五字段与列表/用户绑定、关系时间-ID 严格倒序和 limit+1。Relation 400/404/500 文案及校验顺序未改，GORM/MySQL 错误由外层处理，列表原 GORM 不存在错误映射仍保留。既有 router 流程继续直接消费迁移前固定的两个 v1 游标，验证同刻 ID 边界、毫秒精度及与新游标一致续页；Following 非空 6/空页 3 的预算仍在该流程。双向列表各 2 的断言已随 repo_test.go 删除，资料统计部分 3 只核对源码，不将它们视为本轮运行覆盖。

实施前工作树干净；只读核对真实 feedsystem 的 schema_migrations 为 version=10、dirty=false，user_follows 为原四列、DATETIME(3)、原唯一键及双向分页索引，关系行数为 2，没有执行迁移或修改业务库数据。测试使用原有隔离数据库及自动清理流程。

用户切换 Go 1.27.1 windows/amd64 后，实施轮曾完成 57 函数版本的验证。提交轮发现 repo_test.go 已不存在，保留该删除状态，并对当前 51 函数版本从 backend 直接执行 `go vet ./...`、`go test ./...`、`go test -race -count=1 ./...`，均退出 0；普通使用 7 包缓存，race 重跑 7 包。补充普通 JSON 为 51 PASS/0 FAIL/0 SKIP，7 包缓存、0 包重跑；真实 MySQL/Redis/RabbitMQ 的保留流程参与 race 验证。此前 386/race 环境拒绝与中断不计最终通过证据，没有恢复已删除测试或新增断言；全部结果直接输出终端。验证证据与剩余缺口见第 5.1、5.2 节。前端、配置、DDL、缓存/MQ/热度、视频用例和部署未改；没有新增临时脚本或日志文件。后端已提交，本轮不推送、不开始 R3。

### 6.9 R3-A 三个匿名账户读取（未实施）

下一模块只迁 `GET /api/user`、`GET /api/user/:id`、`GET /api/user/:id/profile`。当前入口为 [user/controller.go](../backend/internal/user/controller.go)，用例在 [user/service.go](../backend/internal/user/service.go)，用户列表 v1 编解码在 [user/pagination.go](../backend/internal/user/pagination.go)。本节只冻结下一步边界，不表示已经实施。

| 迁移目标 | 必须保留 |
| --- | --- |
| Domain 独立公开账户/资料模型、ID 分页位置与读取/统计端口 | 内层只用独立模型；不导入旧 user/auth/video、Gin/GORM、bcrypt 或数据库驱动 |
| Application 三个读取用例、列表双模式、分页与原 v1 游标 | 不带 limit/cursor 保留历史全量；任一 query 存在进入分页。仅省略 limit 使用 20，显式空/0/超出 1–50 均为 400；先校验 limit 再游标。`id ASC`、`id > i`、limit+1、最后返回 ID 续页，保持 RawURLEncoding、v/k/i、version=1、kind=users 与原字段检查，合法旧 v1 继续可用 |
| Infrastructure 复用 [user.Repository](../backend/internal/user/repo.go)、视频计数与现有资料统计 | 只转换旧 ORM/游标/指标，不复制 SQL，不新增 User ORM。资料保持账户→完整公开视频数→获赞→粉丝→关注及失败立即返回；视频数继续复用 PublicVideoQuery。正常装配资料共 5 条 SQL、统计部分 3 条；这是当前实现预算，不表示已有独立持续断言 |
| HTTP Account Handler/DTO 与 router 只切换三个匿名 GET | 保留 user/users/account 包装、avatar_url/bio 的 omitempty、四项零值统计、空数组、末页省略 next_cursor、状态码/错误文案与校验顺序；不暴露密码/软删除字段。当前 Gin 对 query 存在性的处理也须保留：`?cursor=` 进入分页，`?limit=` 拒绝 |

确认引用后，仅删除被替代的旧读取 Controller 方法、列表/资料用例及无用途助手。旧 Service.GetByID、publicUser、登录/刷新/头像等仍用的共享类型与方法必须保留；user.Repository.GetByIDs 的作者批量读取不改。用户/会话 ORM、注册、登录/刷新/撤销、改密/注销事务、头像上传与补偿留后续 Account 模块；关系、Interaction、Following、Video/Feed、缓存/MQ/热度、前端、配置、DDL 与部署不在范围内。

复用当前 7 文件/51 函数的 HTTP、业务与真实依赖流程，仅作迁移必要适配；未经用户明确指令不新增测试、扩展断言或恢复已删除专项。从 backend 直接执行 vet、普通全量、无缓存 race 全量，最后 git diff --check；不新增临时脚本或日志文件。同步必要文档，分别报告实际验证、账户旧 v1 兼容证据、缓存/重跑、跳过和未覆盖项；关系列表旧 v1 的通过不能代替账户游标兼容证明。完成后停止等待 review，不提交、不推送、不开始 R3 后续模块。
