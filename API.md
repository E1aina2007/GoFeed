# GoFeed API 文档

本文档依据后端当前实际注册的路由整理，路由定义位于 `backend/internal/router/router.go`。

## 基本约定

- 开发环境服务地址：`http://localhost:8080`
- 除文件上传接口外，请求和响应均使用 `application/json; charset=utf-8`
- 需要认证的接口必须携带请求头：`Authorization: Bearer <access_token>`
- 每个响应都会返回 `X-Request-ID`；客户端可在请求中携带该值，以便关联服务端日志
- `access_token` 有效期为 15 分钟；刷新令牌有效期为 7 天。刷新令牌每次调用刷新接口后都会轮换，旧令牌立即失效。
- 时间字段使用 RFC 3339 格式，例如 `2026-08-19T08:00:00Z`
- 业务处理器返回错误时，响应格式为 `{"error":"错误说明"}`。未注册路径和未支持方法由 Gin 返回默认 404/405 响应。

## 路由总览

| 方法 | 路径 | 认证 | 说明 |
| --- | --- | --- | --- |
| GET | `/health` | 否 | 健康检查 |
| GET | `/ready` | 否 | 检查 API 依赖是否就绪 |
| GET | `/static/*filepath` | 否 | 已上传媒体文件 |
| HEAD | `/static/*filepath` | 否 | 查询已上传媒体的响应头 |
| POST | `/api/user/register` | 否 | 注册用户 |
| POST | `/api/user/login` | 否 | 登录并创建会话 |
| POST | `/api/user/refresh` | 否 | 刷新会话令牌 |
| GET | `/api/user` | 否 | 查询用户列表 |
| GET | `/api/user/:id` | 否 | 查询用户详情 |
| GET | `/api/user/:id/profile` | 否 | 查询用户公开主页 |
| GET | `/api/user/:id/followers` | 否 | 查询用户的粉丝列表 |
| GET | `/api/user/:id/following` | 否 | 查询用户的关注列表 |
| POST | `/api/user/auth/logout` | 是 | 退出当前会话 |
| PATCH | `/api/user/auth/name` | 是 | 修改用户名 |
| PATCH | `/api/user/auth/password` | 是 | 修改密码并撤销全部会话 |
| POST | `/api/user/auth/avatar` | 是 | 上传并更新头像 |
| PATCH | `/api/user/auth/profile` | 是 | 修改个人资料 |
| GET | `/api/user/auth/:id/follow` | 是 | 查询我是否关注指定用户 |
| PUT | `/api/user/auth/:id/follow` | 是 | 关注指定用户 |
| DELETE | `/api/user/auth/:id/follow` | 是 | 取消关注指定用户 |
| DELETE | `/api/user/auth` | 是 | 注销当前账号 |
| GET | `/api/feed` | 按场景 | Timeline 匿名读取；Following 需要 Bearer JWT 与活动 session |
| GET | `/api/video` | 否 | 查询公开视频流 |
| GET | `/api/video/:id` | 否 | 查询公开视频详情 |
| GET | `/api/video/:id/comments` | 否 | 查询公开视频评论 |
| POST | `/api/video/auth/drafts` | 是 | 创建视频草稿 |
| GET | `/api/video/auth/drafts/:id` | 是 | 查询当前草稿状态 |
| POST | `/api/video/auth/drafts/:id/play` | 是 | 上传草稿视频文件 |
| POST | `/api/video/auth/drafts/:id/cover` | 是 | 上传草稿封面图片 |
| POST | `/api/video/auth/drafts/:id/publish` | 是 | 发布完整草稿（异步，返回 processing） |
| DELETE | `/api/video/auth/drafts/:id` | 是 | 丢弃 draft/rejected 视频并排入异步清扫 |
| GET | `/api/video/auth/:id/status` | 是 | 查询视频异步处理状态 |
| GET | `/api/video/auth/mine` | 是 | 查询我的视频 |
| GET | `/api/video/auth/:id/like` | 是 | 查询我是否点赞指定视频 |
| PUT | `/api/video/auth/:id/like` | 是 | 点赞指定视频 |
| DELETE | `/api/video/auth/:id/like` | 是 | 取消点赞指定视频 |
| POST | `/api/video/auth/:id/comments` | 是 | 发表评论 |
| DELETE | `/api/video/auth/:id/comments/:commentID` | 是 | 删除自己的评论 |
| DELETE | `/api/video/auth/:id` | 是 | 删除自己的视频 |

## 公共数据结构

### `PublicUser`

```json
{
  "id": 42,
  "username": "alice",
  "avatar_url": "/static/avatars/42/20260824/avatar.png",
  "bio": "视频创作者"
}
```

`avatar_url` 和 `bio` 在空值时不会返回。密码及密码哈希不会出现在任何响应中。

### `LoginResponse`

```json
{
  "access_token": "<JWT>",
  "refresh_token": "<refresh-token>",
  "expires_at": "2026-08-26T08:00:00Z",
  "user": {
    "id": 42,
    "username": "alice"
  }
}
```

`expires_at` 是会话和刷新令牌的过期时间，不是 15 分钟的访问令牌过期时间。

### `VideoItem`

```json
{
  "id": 100,
  "title": "我的第一条视频",
  "description": "视频介绍",
  "play_url": "/static/videos/42/20260819/demo_0123456789abcdef0123456789abcdef.mp4",
  "play_file_name": "demo_0123456789abcdef0123456789abcdef.mp4",
  "play_original_name": "我的视频.mp4",
  "cover_url": "/static/covers/42/20260819/cover_0123456789abcdef0123456789abcdef.png",
  "cover_file_name": "cover_0123456789abcdef0123456789abcdef.png",
  "cover_original_name": "封面.png",
  "published_at": "2026-08-19T08:00:00Z",
  "likes_count": 0,
  "comments_count": 0,
  "author": {
    "id": 42,
    "username": "alice",
    "avatar_url": "/static/avatars/42/20260824/avatar.png"
  }
}
```

媒体地址为站内相对路径。浏览器访问时可拼接服务地址，例如 `http://localhost:8080` + `play_url`。

### `VideoListResponse`

```json
{
  "items": [
    { "id": 100, "title": "我的第一条视频" }
  ],
  "next_cursor": "eyJ2IjoxLCJrIjoicHVibGljIiwicCI6IjIwMjYtMDgtMTlUMDg6MDA6MDBaIiwiaSI6MTAwfQ"
}
```

当没有下一页时，`next_cursor` 不返回。后续分页将该字段原样作为 `cursor` 查询参数传回；它是服务端生成的不透明值，不应自行构造或修改。视频列表游标当前版本为 `1`，并绑定生成它的查询范围：全局列表使用 `public`，指定作者列表使用 `author` 和作者 ID，`/api/video/auth/mine` 使用 `mine` 和当前用户 ID。跨范围、跨作者、版本不支持、旧格式或字段被篡改的游标均返回 `400`；空游标仍表示第一页。

### `CommentListResponse`

```json
{
  "items": [
    {
      "id": 301,
      "video_id": 100,
      "author": {
        "id": 7,
        "username": "bob",
        "avatar_url": "/static/avatars/7/20260826/avatar.png",
        "bio": "视频爱好者"
      },
      "content": "很精彩",
      "created_at": "2026-08-26T08:00:00Z"
    }
  ],
  "next_cursor": "eyJ2IjoxLCJrIjoiY29tbWVudHMiLCJyIjoxMDAsInAiOiIyMDI2LTA4LTI2VDA4OjAwOjAwWiIsImkiOjMwMX0"
}
```

评论按创建时间和 ID 倒序排列。游标当前版本为 `1`，绑定 `comments` 列表类型和请求的视频 ID，只能原样回传到同一视频的评论列表；R1-A 迁移前取得的合法 v1 游标继续可用，无版本旧格式、版本不支持、结构字段不合法或跨视频复用均返回 `400`。已删除评论不会返回；评论作者已注销时，作者资料会显示为 `已注销用户`。

### `FollowListResponse`

```json
{
  "items": [
    {
      "user": {
        "id": 7,
        "username": "bob"
      },
      "followed_at": "2026-08-26T08:00:00Z"
    }
  ],
  "next_cursor": "eyJ2IjoxLCJrIjoiZm9sbG93ZXJzIiwiciI6NywicCI6IjIwMjYtMDgtMjZUMDg6MDA6MDBaIiwiaSI6N30"
}
```

粉丝和关注列表均按建立关注关系的时间和关系 ID 倒序分页。游标当前版本为 `1`，同时绑定 `followers` 或 `following` 列表类型和目标用户 ID；不能在另一类关系列表或其他用户间复用，旧格式、版本不支持或结构字段不合法均返回 `400`。`user` 为关系另一端的公开资料。

## 系统接口

### 健康检查

`GET /health`

成功响应：`200 OK`

```json
{
  "name": "GoFeed",
  "status": "ok"
}
```

`/health` 只表示 API 进程存活，不检查外部依赖。部署探针应使用 `GET /ready`：数据库连接可用时返回 `200 OK`，响应为：

```json
{
  "name": "GoFeed",
  "status": "ready",
  "dependencies": {
    "database": "ok"
  }
}
```

数据库不可用时返回 `503 Service Unavailable`，响应中的 `status` 为 `not_ready`，`dependencies.database` 为 `unavailable`。内部错误详情只写入服务端日志，不通过接口返回。

### 静态媒体

`GET /static/*filepath`

`GET` 用于访问上传接口返回的媒体 URL，例如：

```text
GET /static/videos/42/20260819/demo_0123456789abcdef0123456789abcdef.mp4
```

成功时直接返回文件内容及其 MIME 类型。`HEAD` 使用相同路径，仅返回响应头。该路由只暴露上传目录中的文件；文件不存在时返回 `404 Not Found`。

## 用户接口

### 注册用户

`POST /api/user/register`

请求体：

```json
{
  "username": "alice",
  "password": "password-123"
}
```

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `username` | string | 是 | 原值先按字符数 3–32 绑定校验；绑定成功后去首尾空白，再按字节长度检查 3–32 |
| `password` | string | 是 | 原值先按字符数 8–72 绑定校验，再按字节长度检查 8–72；不去首尾空白 |

绑定继续使用原 `ShouldBindJSON` 和 `required,min=3,max=32` / `required,min=8,max=72` 标签，不新增未知字段或尾部 JSON 限制。业务校验后先使用 bcrypt 默认成本哈希，再创建用户；重名请求也先哈希，用户名唯一性、大小写语义及软删除账户的用户名占用由原数据库唯一键决定，不预查重。

成功响应：`201 Created`

```json
{
  "user": {
    "id": 42,
    "username": "alice"
  }
}
```

响应只包含公开用户字段 `id`、`username` 和非空时的 `avatar_url`、`bio`；不包含密码或软删除字段，也不创建会话或令牌。

| 状态码 | 错误文案 | 原因 |
| --- | --- | --- |
| `400` | `invalid registration payload` | JSON 绑定或 binding 标签校验失败 |
| `400` | `invalid user input` | 绑定后的业务字节长度校验失败 |
| `409` | `username already exists` | 原仓储将 MySQL 1062 重名错误转换为冲突 |
| `500` | `user operation failed` | 未知哈希或创建错误 |

超限为下方共享限流行为的 `429`，限流仍先于 JSON 绑定。

### 注册与登录的限流

`POST /api/user/register` 与 `POST /api/user/login` 共享固定窗口限流，并在 JSON 请求体绑定、注册或认证处理前执行。注册每个 IP 每小时最多 5 次，Redis Key 为 `rl:v1:register:<IP>`；登录每个 IP 每分钟最多 10 次，Redis Key 为 `rl:v1:login:<IP>`。

超限响应固定为 `429 Too Many Requests`，带 `Retry-After` 响应头；其值为正整数秒。响应体固定为：

```json
{
  "error": "rate limit exceeded"
}
```

Redis 不可用时限流会 fail-open，注册或登录继续按原有业务契约处理。接口不返回限流内部状态或 Redis 故障详情。

### 登录

`POST /api/user/login`

请求体字段为 `username`、`password`，仍用原 `required,min=3,max=32` 与 `required,min=8,max=72` 标签按 rune 绑定校验。绑定成功后仅对用户名 `TrimSpace`，密码保持原样；登录不增加注册的业务字节长度校验。

成功响应：`200 OK`，响应体为 [`LoginResponse`](#loginresponse)。

| 状态码 | 错误文案 | 原因 |
| --- | --- | --- |
| `400` | `invalid login payload` | JSON 绑定或 binding 标签校验失败 |
| `401` | `invalid username or password` | 用户不存在或密码比较失败 |
| `500` | `failed to authenticate` | 其他用户读取错误 |
| `500` | `failed to create session` | 会话创建阶段的随机生成、存储或访问令牌签发错误 |

超限行为见上方共享限流说明。会话先保存再签发访问令牌；签发失败不额外回滚已保存的会话。

### 刷新令牌

`POST /api/user/refresh`

请求体：

```json
{
  "refresh_token": "<refresh-token>"
}
```

`refresh_token` 保留原 `required` 绑定规则，绑定后原样使用，不 Trim 或增加格式校验。

成功响应：`200 OK`，响应体为 [`LoginResponse`](#loginresponse)。请使用响应中的新刷新令牌替换本地旧值；轮换保留原会话 ID 和 `expires_at`，不延长七天会话期限。

| 状态码 | 错误文案 | 原因 |
| --- | --- | --- |
| `400` | `invalid refresh payload` | JSON 绑定失败或缺少 `refresh_token` |
| `401` | `invalid refresh token` | 轮换阶段任意错误，包括令牌无效、过期、撤销、已使用、存储或随机生成失败；轮换后用户读取失败也返回该错误 |
| `500` | `failed to create access token` | 轮换后访问令牌签发失败 |

先以旧刷新哈希做 CAS 轮换，再读取当前用户并签发访问令牌。用户读取失败时尝试撤销该会话，忽略撤销错误。轮换已提交后不回滚旧哈希或自动重试；刷新和退出不新增限流。

### 查询用户列表

`GET /api/user`

该接口兼容两种读取方式：不带 `limit` 和 `cursor` 时保留旧的全量读取行为；只要携带任一分页参数即启用分页。用户按 ID 正序排列，已注销用户不返回。

分页查询参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `limit` | int | 否 | 每页数量，范围 1-50；分页模式下省略时默认 20 |
| `cursor` | string | 否 | 上一页响应中的 `next_cursor`；单独使用时同样进入分页模式 |

参数按是否存在判断，`?cursor=` 仍启用分页；仅省略 `limit` 时默认 20，显式 `?limit=`、`0` 或超出 1–50 均返回 `400 invalid user list limit`。先校验 limit，再校验游标；两者都不合法时返回 limit 错误。

成功响应：`200 OK`

```json
{
  "users": [
    {
      "id": 42,
      "username": "alice",
      "avatar_url": "/static/avatars/42/20260824/avatar.png",
      "bio": "视频创作者"
    }
  ]
}
```

分页模式存在后续页面时额外返回不透明的 `next_cursor`：

```json
{
  "users": [
    { "id": 42, "username": "alice" }
  ],
  "next_cursor": "eyJ2IjoxLCJrIjoidXNlcnMiLCJpIjo0Mn0"
}
```

游标当前版本为 `1`，绑定固定的 `users` 列表和最后一条用户 ID；客户端不得构造或修改。载荷仍为 RawURL Base64 的 `v/k/i`，R3-A 迁移前的合法 v1 继续可用，原字段检查保持不变。旧格式、未知字段、错误版本、错误列表范围、零 ID、`limit` 超出范围或无法解析时均返回 `400 Bad Request`。分页按 `id ASC`、`id > 游标 ID` 读取，多读一条判断续页，以实际返回末条 ID 生成游标；空页返回 `users: []`，末页省略 `next_cursor`。公开资料不返回密码或软删除字段。

### 查询用户详情

`GET /api/user/:id`

路径参数 `id` 必须是大于 0 的无符号整数。

成功响应：`200 OK`

```json
{
  "user": {
    "id": 42,
    "username": "alice",
    "avatar_url": "/static/avatars/42/20260824/avatar.png",
    "bio": "视频创作者"
  }
}
```

常见失败：`400` `id` 格式不正确，`404` 用户不存在或已注销。

### 查询公开主页

`GET /api/user/:id/profile`

路径参数 `id` 必须是大于 0 的无符号整数。

成功响应：`200 OK`

```json
{
  "account": {
    "id": 42,
    "username": "alice",
    "avatar_url": "/static/avatars/42/20260824/avatar.png",
    "bio": "视频创作者"
  },
  "video_count": 3,
  "total_likes": 0,
  "follower_count": 0,
  "vlogger_count": 0
}
```

`video_count` 统计符合完整公开规则的视频数量：已发布、未软删除，播放/封面的地址、存储文件名及原始文件名均非空，发布时间非 NULL。`total_likes` 统计该用户当前可见视频的点赞关系数，`follower_count` 统计活跃粉丝数，`vlogger_count` 统计仍可见的关注对象数；三项互动统计均由关系表实时计算。四项统计为零时仍返回字段，账户中的空 `avatar_url`、`bio` 按原 omitempty 省略。

常见失败：`400` `id` 格式不正确，`404` 用户不存在或已注销。

### 查询粉丝和关注列表

`GET /api/user/:id/followers`

`GET /api/user/:id/following`

路径参数 `id` 必须是大于 0 的无符号整数。两个接口均允许匿名读取，支持以下查询参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `cursor` | string | 否 | 上一页响应中的 `next_cursor` |
| `limit` | int | 否 | 每页数量，范围 1-50；省略、空值或显式 0 使用 20，越界不裁剪 |

成功响应：`200 OK`，响应体为 [`FollowListResponse`](#followlistresponse)。`followers` 返回关注该用户的账号，`following` 返回该用户正在关注的账号。

常见失败：`400` 路径参数、`limit` 或 `cursor` 不合法；`cursor` 必须由同一目标用户的同一列表生成，旧格式、版本不支持、跨用户或 `followers`/`following` 互换复用均返回 `400`。`404` 用户不存在或已注销。

校验顺序为路径 ID、limit 文本解析、活动目标用户、limit 范围、游标、列表读取；例如缺失用户携带 `limit=99` 为 `404 user not found`，携带 `limit=abc` 先为 `400 invalid limit`。合法既有 v1 游标可继续使用，载荷仍为 RawURL Base64 的 `v/k/r/p/i`（版本、列表、目标用户、关系时间、关系 ID）。列表过滤注销对端，按关系时间与关系 ID 严格倒序；空页返回 `items: []`，末页省略 `next_cursor`。

### 退出当前会话

`POST /api/user/auth/logout`

无需请求体。成功响应：`204 No Content`，响应体为空。

该操作仅撤销当前访问令牌所属的会话，不影响同一账号在其他设备创建的会话。

常见失败：`401` 未携带、格式错误、过期或已撤销的访问令牌，沿用原 JWT 中间件文案。缺少当前用户/会话身份或撤销阶段任意错误为 `401 invalid or expired token`；重复退出仍为 `401`。

### 修改用户名

`PATCH /api/user/auth/name`

请求体：

```json
{
  "new_username": "alice_new"
}
```

`new_username` 去除首尾空格后长度必须为 3-32。

成功响应：`200 OK`

```json
{
  "message": "username updated successfully"
}
```

常见失败：`400` 请求体或用户名不合法，`401` 未认证，`409` 用户名已存在。

### 修改密码

`PATCH /api/user/auth/password`

请求体：

```json
{
  "old_password": "password-123",
  "new_password": "new-password-123"
}
```

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `old_password` | string | 是 | 保留 `required,min=8,max=72` binding 标签 |
| `new_password` | string | 是 | 保留 `required,min=8,max=72` binding 标签；绑定后再检查 8-72 字节 |

JSON 绑定仍使用原规则，字符串长度标签按 rune 校验。两个密码均不 Trim；绑定后只对新密码按 Go `len` 检查字节长度。之后读取用户、比较旧密码并生成 bcrypt 默认成本哈希，再在同一事务中以原密码哈希做 CAS 更新并撤销该账号的全部会话。CAS 未匹配仍按旧密码错误处理；撤销失败会回滚密码更新，不签发新会话或令牌。

成功响应：`200 OK`

```json
{
  "message": "password updated; sign in again"
}
```

成功后当前账号的全部会话都会被撤销，必须重新登录才能继续调用受保护接口。

| 状态码 | 错误文案 | 原因 |
| --- | --- | --- |
| `400` | `invalid password payload` | JSON 绑定或 binding 标签校验失败 |
| `400` | `invalid user input` | 新密码字节长度不在 8-72 |
| `403` | `wrong password` | 任意旧密码比较失败，或密码 CAS 未匹配 |
| `404` | `user not found` | 用户不存在/已注销，或其他原仓储未找到错误 |
| `500` | `user operation failed` | 未知读取、哈希或事务错误 |

认证失败仍为 `401` 并沿用原 JWT 中间件文案；Handler 缺少当前用户身份时为 `401 invalid or expired token`。本接口不新增限流。

### 上传头像

`POST /api/user/auth/avatar`

请求使用 `multipart/form-data`，文件字段名为 `file`。

支持 JPG、JPEG、PNG、WebP，单文件最大 10 MiB。当前默认实现将文件保存到本地 `/static/avatars/{user_id}/{yyyyMMdd}/` 目录，并返回相对地址；存储抽象保留替换为 OSS 等对象存储的能力。

成功响应：`201 Created`

```json
{
  "avatar_url": "/static/avatars/42/20260824/avatar.png"
}
```

常见失败：`400` 文件格式或表单不合法，`401` 未认证，`413` 文件超过大小限制。

### 修改个人资料

`PATCH /api/user/auth/profile`

请求体至少提供一个非空字段：

```json
{
  "bio": "视频创作者"
}
```

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `avatar_url` | string | 否 | 最多 512 个字符，保留对象存储 URL 兼容能力 |
| `bio` | string | 否 | 最多 255 个字符 |

新前端通过头像上传接口更新头像；`avatar_url` 仍可由对象存储客户端直接提交。空字符串不会更新对应字段，因此该接口当前不能清空头像或简介。

成功响应：`200 OK`

```json
{
  "message": "profile updated successfully"
}
```

常见失败：`400` 请求体不合法，`401` 未认证。

### 查询关注状态

`GET /api/user/auth/:id/follow`

路径参数 `id` 是要查询的目标用户。成功响应：`200 OK`

```json
{
  "following": true,
  "follower_count": 12
}
```

`following` 表示当前认证用户是否关注目标用户，`follower_count` 是目标用户的实时粉丝数。

常见失败：`400` `id` 不合法或目标是当前用户，`401` 未认证，`404` 当前用户或目标用户不存在或已注销。

### 关注和取消关注

`PUT /api/user/auth/:id/follow`

`DELETE /api/user/auth/:id/follow`

路径参数 `id` 是要操作的目标用户，无需请求体。成功响应均为 `200 OK`，响应体与查询关注状态相同。重复关注保持已关注状态，重复取消保持未关注状态，因此两个写操作均可安全重试。用户不能关注自己。

常见失败：`400` `id` 不合法或尝试关注自己，`401` 未认证，`404` 当前用户或目标用户不存在或已注销。

### 注销账号

`DELETE /api/user/auth`

无需请求体。成功响应：`204 No Content`，响应体为空。

账号会被软删除，并撤销该账号的全部会话。软删除后公开用户接口和登录接口均不可再访问该账号；后台清扫任务会在保留期结束后彻底清除记录。

先软删除用户，再在同一事务中撤销其全部会话；任一失败都回滚。不增加用户预读、幂等成功、媒体删除或关系/视频级联操作，不新增限流。

常见失败：`401` 未认证或令牌已失效，沿用原 JWT 中间件文案；Handler 缺少当前用户身份时为 `401 invalid or expired token`。用户不存在/已注销或原仓储未找到错误仍为 `404 user not found`，其他未知事务错误为 `500 user operation failed`。重复请求会被原会话校验拒绝，不承诺再次返回 `204`。

## Feed 接口

### 查询 Feed

`GET /api/feed`

已启用匿名 `timeline` 与需要 Bearer JWT、活动 session 和活动观看者的 `following`，均按 `(published_at DESC, id DESC)` 排序。Feed 应用层负责分页与批量组装，基础设施适配器复用现有 MySQL 仓储、作者读取和互动聚合。只返回已发布、未软删除且具备完整媒体字段及发布时间的视频；HTTP DTO 保持现有展示字段。携带 `Authorization` 不改变 Timeline 结果，不返回用户专属点赞或关注状态。

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `scene` | string | 否 | 省略或空值时为 `timeline`；`following` 已启用且需要认证；`hot`、`recommend` 尚未启用 |
| `limit` | int | 否 | 范围 1-50，省略时默认 20；显式空值或 0 均无效 |
| `cursor` | string | 否 | 当前场景上一次响应的 `next_cursor`；省略或空值表示第一页 |

只接受上述三个参数，每个参数最多出现一次。未知参数（包括 `author_id`）、重复参数或无法解析的查询字符串返回 `400`；按作者读取继续使用 `/api/video?author_id=...`。

成功返回 `200 OK`，响应字段与 `VideoListResponse` 相同：

```json
{
  "items": []
}
```

示例展示空列表；非空列表的每项采用 `VideoItem`，存在后续页面时另返回字符串字段 `next_cursor`，没有后续页面时省略它。客户端应直接复用展示字段并原样回传游标。

Feed 游标版本为 `1`，独立绑定 `timeline` 场景、排序版本 `1`（发布时间与 ID 倒序）和分页位置。不能与 `/api/video` 的 `public`、`author`、`mine` 游标互换；结构、版本、场景、排序版本或位置不合法、含未知字段、编码不正确或长度超过 1024 字符时返回 `400`。Timeline 游标不绑定用户。Following 使用独立 `following` 场景载荷并增加非零 `viewer_id`，必须与已认证观看者一致；跨场景、跨观看者或社交关注列表游标不能复用。

游标由以下 JSON 载荷经过 Base64 URL 编码生成；字段说明用于后端维护，客户端仍应原样回传游标：

| JSON 字段 | 含义 |
|---|---|
| `version` | 游标载荷结构版本，当前为 `1` |
| `scene` | 游标所属 Feed 场景：`timeline` 或 `following` |
| `sort_version` | 排序规则版本，当前为 `1`，对应 `(published_at DESC, id DESC)` |
| `published_at` | 上一页最后一条视频的发布时间，统一按 UTC 渲染（例如 `2026-08-01T07:59:50Z`）；客户端应原样回传，不要自行改写时区 |
| `video_id` | 上一页最后一条视频的 ID，与发布时间共同确定下一页读取位置 |
| `viewer_id` | 仅 Following 载荷包含，必须等于已认证观看者；Timeline 不包含该字段 |

新 Feed 游标统一使用上述完整字段名，不接受此前开发过程中的单字母字段名；既有 `/api/video` 游标格式不变。Following 使用无填充 URL-safe Base64，拒绝未知字段、尾随 JSON 与非规范编码。游标只提供查询位置，不能用作认证凭据；查询始终使用 JWT 上下文的观看者 ID。相同用户的新活动 session 可续用其游标，limit 可在 1–50 之间改变。

Following 在同一视频查询内限定观看者当前关注的活动作者和完整公开视频，不包含未关注作者或自动包含自己的视频。不要求视频在关注后发布，新关注作者的可见历史视频也属于集合；续页只读取当前游标边界之后的记录。取关、作者注销和视频软删除在下一次查询生效，跨页不保证冻结快照。作者在视频查询后注销仍可能出现既有“已注销用户”占位，下一次查询排除。无关注或无可见视频为正常空页。

所有可识别的 Following 请求及错误响应设置 `Cache-Control: private, no-store`，将 `Authorization` 合入已有 `Vary`。Following 直接读取 MySQL，不读取或写入 Timeline 页/卡片缓存，也不占用其缓存并发名额。

| 状态码 | 条件 | 错误响应 |
| --- | --- | --- |
| `400` | 未知场景 | `{"error":"invalid feed scene"}` |
| `400` | 数量不合法 | `{"error":"invalid limit"}` |
| `400` | 游标不合法 | `{"error":"invalid feed cursor"}` |
| `400` | 查询参数结构不合法 | `{"error":"invalid feed query"}` |
| `401` | Following 缺少、过期、无效或撤销凭据；观看者缺失或已注销 | 沿用现有鉴权错误文案；活动观看者检查失败为 `{"error":"authentication required"}` |
| `501` | 合法参数请求尚未启用的 `hot` 或 `recommend` | `{"error":"feed scene is not enabled"}`，并设置 `Cache-Control: no-store` |
| `503` | Following 活动观看者或视频、作者、统计读取不可用；开启缓存的 Timeline 请求容量耗尽 | `{"error":"feed temporarily unavailable"}` |
| `500` | 未预期的内部错误 | `{"error":"feed operation failed"}` |

处理顺序为查询字符串和数量校验、场景选择；Timeline 直接校验游标及读取，Following 先认证再校验游标并读取。合法 Following 参数缺少凭据时，即使游标也非法仍返回 401；认证成功加非法游标返回 400。活动观看者检查及业务数据库故障为安全的 503；共享 session 中间件的数据库校验失败仍沿用 401。未启用场景返回 `501`，不会读取数据库或静默返回 Timeline。内部读取错误不回显给客户端。

Timeline 页缓存直接装配，仅缓存带游标的后续页，保存轻量排序条目及下一页探测记录；命中仍读取 MySQL 校验当前公开卡片。卡片缺失或排序变化按原游标整页回源，Redis 未命中、坏载荷或故障也回源。缓存写失败不改变成功响应，作者和互动统计实时读取。每实例最多并发处理 32 个缓存链路请求、16 次缓存操作；缓存容量耗尽时跳过缓存回源。查询参数、响应、游标与 `/ready` 契约不变。

基础卡片缓存直接装配，仅参与后续页的页缓存命中路径。读取前按完整公开规则批量验证 MySQL 当前状态；卡片 ID、作者 ID、发布时间必须匹配，缺失、坏值及缓存故障批量回源，MySQL 错误仍为 503。作者资料与互动统计实时读取；首屏及页缓存未命中走 MySQL 原路径。卡片命中沿用本次 MySQL 时间表示，响应与游标不变；两个缓存共享上述容量，旧接口不受影响。历史验收及本次边界见开发计划第 5 节。

首页通过专用 `listTimelineFeed` 读取 `/api/feed?scene=timeline&limit=12`，后续页原样回传该接口的 `next_cursor`；首屏及重新加载不携带旧游标。作者主页仍通过 `listPublishedVideos` 读取 `/api/video?author_id=...`，其他既有视频及社交接口保持原路径。首页失败后不会自动切换到旧接口续页。新旧游标不得互换，切换或回滚入口后须重新加载页面清空分页状态。

接口描述来自当前源码；2026-10-02 首页模块以浏览器、真实 Go API、隔离 MySQL 和真实 Redis 验证，缓存开关前后浏览器首屏及分页响应逐字节一致，命中由实际缓存事件与 Redis 读写记录证明。历史后端契约验收与本轮结果分别见 [开发计划](./docs/DEVELOPMENT_PLAN.md) 第 5 节。首页迁移未新增数据库迁移或 Feed MQ 事件，旧接口参数、响应和游标保持兼容。

## 视频接口

### 查询公开视频流

`GET /api/video`

查询参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `author_id` | uint | 否 | 仅查询指定作者的公开视频 |
| `cursor` | string | 否 | 上一页响应中的 `next_cursor` |
| `limit` | int | 否 | 每页数量，范围 1-50，默认 20 |

视频按发布时间倒序排列；发布时间相同时按 ID 倒序排列。成功响应：`200 OK`，响应体为 [`VideoListResponse`](#videolistresponse)。

常见失败：`400` `author_id`、`cursor` 或 `limit` 不合法。`cursor` 必须是本接口当前查询范围生成的游标，不能复用于全局、其他作者或“我的视频”列表。互动统计查询暂不可用时返回 `503 Service Unavailable`，属于可重试的临时故障。

### 查询公开视频详情

`GET /api/video/:id`

路径参数 `id` 必须是大于 0 的无符号整数。

成功响应：`200 OK`

```json
{
  "video": {
    "id": 100,
    "title": "我的第一条视频",
    "description": "视频介绍",
    "play_url": "/static/videos/42/20260819/demo_0123456789abcdef0123456789abcdef.mp4",
    "play_file_name": "demo_0123456789abcdef0123456789abcdef.mp4",
    "play_original_name": "我的视频.mp4",
    "cover_url": "/static/covers/42/20260819/cover_0123456789abcdef0123456789abcdef.png",
    "cover_file_name": "cover_0123456789abcdef0123456789abcdef.png",
    "cover_original_name": "封面.png",
    "published_at": "2026-08-19T08:00:00Z",
    "likes_count": 0,
    "comments_count": 0,
    "author": {
      "id": 42,
      "username": "alice"
    }
  }
}
```

常见失败：`400` `id` 不合法，`404` 视频不存在、未发布或已删除。互动统计查询暂不可用时返回 `503 Service Unavailable`，属于可重试的临时故障。

### 查询视频评论

`GET /api/video/:id/comments`

路径参数 `id` 必须是大于 0 的已发布且未软删除视频标识。查询参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `cursor` | string | 否 | 上一页响应中的 `next_cursor` |
| `limit` | int | 否 | 每页数量，范围 1-50，默认 20 |

成功响应：`200 OK`，响应体为 [`CommentListResponse`](#commentlistresponse)。

显式 `limit=0` 与省略参数一样使用默认 20；合法 v1 游标在读入口迁入 Interaction 后仍兼容。空页返回 `items: []`，没有下一页时省略 `next_cursor`。

常见失败：`400` 路径参数、`limit` 或 `cursor` 不合法；`cursor` 必须由同一视频评论列表生成，无版本旧格式、版本不支持或跨视频复用均返回 `400`。`404` 视频不存在、未发布、已删除或媒体字段不完整。数据库读取失败返回安全 `500` 错误，不返回空页。

### 创建视频草稿

`POST /api/video/auth/drafts`

请求体：

```json
{
  "title": "我的第一条视频",
  "description": "视频介绍"
}
```

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `title` | string | 是 | 去除首尾空格后不能为空，最多 255 个字符 |
| `description` | string | 否 | 最多 1000 个字符 |

成功响应：`201 Created`

```json
{
  "draft": {
    "id": 100,
    "title": "我的第一条视频",
    "description": "视频介绍",
    "status": "draft",
    "has_video": false,
    "has_cover": false,
    "created_at": "2026-08-21T08:00:00Z",
    "updated_at": "2026-08-21T08:00:00Z"
  }
}
```

草稿没有 `published_at`，也没有客户端可填写的媒体字段。`has_video` 和 `has_cover` 分别表示对应媒体是否已由服务端成功绑定到草稿，客户端可用它们在上传响应丢失后恢复后续流程。

草稿保留期由 `RETENTION_VIDEO_DRAFT_HOURS` 控制。保留期届满后，后台 sweeper 会在下一轮将未发布草稿转入不可逆的 `purging` 状态；进入该状态后不能继续上传或发布。处理失败的视频从 `rejected_at` 起使用同一保留时长，届满后也会进入 `purging`。

常见失败：`400` 请求体或标题、简介不合法，`401` 未认证。

### 查询草稿状态

`GET /api/video/auth/drafts/:id`

路径参数 `id` 必须是当前用户的草稿标识。接口只返回仍可写的 `draft` 或已排入清扫的 `purging` 草稿；已发布视频不通过此路径返回。

成功响应：`200 OK`

```json
{
  "draft": {
    "id": 100,
    "title": "我的第一条视频",
    "description": "视频介绍",
    "status": "draft",
    "has_video": true,
    "has_cover": false,
    "play_original_name": "我的视频.mp4",
    "created_at": "2026-08-21T08:00:00Z",
    "updated_at": "2026-08-21T08:03:00Z"
  }
}
```

响应不包含媒体 URL 或物理存储名。`purging` 表示草稿已不可恢复，媒体会由后台 sweeper 异步删除；此时完成标识仅表示该媒体曾被成功绑定，不能用于判断对象是否仍可访问。

常见失败：`400` 路径参数不合法，`401` 未认证，`403` 草稿不属于当前用户，`404` 草稿不存在、已被清扫或不再处于草稿流程。

### 丢弃视频草稿

`DELETE /api/video/auth/drafts/:id`

路径参数 `id` 必须是当前用户处于 `draft` 或 `rejected` 状态的视频标识。服务端在单个事务中将记录转换为 `purging`，不会在 HTTP 请求内直接删除媒体文件；后台 sweeper 会使用既有租约和检查点完成可重试清扫。`rejected` 视频也可以主动丢弃，不需要等待保留期届满。

成功响应：`202 Accepted`

```json
{
  "draft": {
    "id": 100,
    "title": "我的第一条视频",
    "description": "视频介绍",
    "status": "purging",
    "has_video": true,
    "has_cover": true,
    "play_original_name": "我的视频.mp4",
    "cover_original_name": "封面.png",
    "created_at": "2026-08-21T08:00:00Z",
    "updated_at": "2026-08-21T08:05:00Z"
  }
}
```

`202` 只表示服务端已持久化接受清扫，不代表媒体已物理删除。若客户端在收到响应前断开，可对仍处于 `purging` 的同一记录重复调用该接口，响应仍为 `202`；记录被 sweeper 最终硬删除后再次请求会返回 `404`。

常见失败：`400` 路径参数不合法，`401` 未认证，`403` 视频不属于当前用户，`404` 视频不存在或已被清扫，`409` 视频已发布或不再可进入清扫。

### 上传草稿视频文件

`POST /api/video/auth/drafts/:id/play`

路径参数 `id` 必须是当前用户处于 `draft` 状态的草稿标识。请求类型为 `multipart/form-data`，表单必须包含 `file` 文件字段。

| 项目 | 要求 |
| --- | --- |
| 文件大小 | 大于 0 且不超过 200 MiB |
| 可用扩展名 | `.mp4`、`.webm`、`.mov` |
| 文件内容 | 校验对应的 MP4/MOV `ftyp` 或 WebM EBML 文件头 |

成功响应：`201 Created`

```json
{
  "draft_id": 100,
  "play_url": "/static/videos/42/20260821/demo_0123456789abcdef0123456789abcdef.mp4",
  "play_file_name": "demo_0123456789abcdef0123456789abcdef.mp4",
  "play_original_name": "我的视频.mp4"
}
```

`play_file_name` 是服务端清洗后的实际对象名，末尾附带 32 位随机对象键，因此已删除对象的路径不会被后续同名上传复用；`play_original_name` 是客户端文件名去掉路径后的展示名称。草稿的同一媒体类型不能重复绑定。

常见失败：`400` 缺少文件或类型校验失败，`401` 未认证，`403` 草稿不属于当前用户，`404` 草稿不存在，`409` 草稿不再可写或该媒体已绑定，`413` 文件过大。

### 上传草稿封面图片

`POST /api/video/auth/drafts/:id/cover`

路径参数 `id` 必须是当前用户处于 `draft` 状态的草稿标识。请求类型为 `multipart/form-data`，表单必须包含 `file` 文件字段。

| 项目 | 要求 |
| --- | --- |
| 文件大小 | 大于 0 且不超过 10 MiB |
| 可用扩展名 | `.jpg`、`.jpeg`、`.png`、`.webp` |
| 文件内容 | 校验 JPEG、PNG 或 WebP 文件头 |

成功响应：`201 Created`

```json
{
  "draft_id": 100,
  "cover_url": "/static/covers/42/20260821/cover_0123456789abcdef0123456789abcdef.png",
  "cover_file_name": "cover_0123456789abcdef0123456789abcdef.png",
  "cover_original_name": "封面.png"
}
```

常见失败：`400` 缺少文件或类型校验失败，`401` 未认证，`403` 草稿不属于当前用户，`404` 草稿不存在，`409` 草稿不再可写或该媒体已绑定，`413` 文件过大。

### 发布草稿

`POST /api/video/auth/drafts/:id/publish`

路径参数 `id` 必须是当前用户完整的 `draft` 草稿。该接口没有请求体；客户端不能提交 `play_url`、`cover_url`、物理文件名或原始文件名。服务端在单个事务中验证两类媒体都已绑定，写入实际 `published_at`，将草稿转换为 `processing` 并原子写入异步处理事件。

发布是异步语义：`processing` 期间视频在公开列表、详情与“我的视频”中不可见，worker 完成媒体校验后自动转为 `published`；校验失败则转为 `rejected`。处理结果通过 `GET /api/video/auth/:id/status` 查询。

成功响应：`202 Accepted`，响应体为 `{"draft": <DraftItem>}`，其中 `status` 为 `processing`。

常见失败：`400` 提交了请求体或路径参数不合法，`401` 未认证，`403` 草稿不属于当前用户，`404` 草稿不存在，`409` 草稿未完成或不再可发布。

### 查询视频处理状态

`GET /api/video/auth/:id/status`

路径参数 `id` 必须是当前用户已提交处理的视频标识；处于 `draft` 或 `purging` 状态、不属于当前用户或不存在的视频统一返回 `404`，避免探测他人资源。

成功响应：`200 OK`

```json
{
  "status": "processing",
  "published_at": "2026-08-21T08:00:00Z",
  "rejected_at": null,
  "rejected_reason": ""
}
```

响应字段位于顶层，四个字段始终返回；尚未发生的时间使用 `null`，没有拒绝原因时使用空字符串。

`status` 为 `processing` 或 `published` 时返回 `published_at`（发布请求时刻）；`rejected` 时返回 `rejected_at` 与 `rejected_reason`。

常见失败：`400` `id` 不合法，`401` 未认证，`404` 视频不存在、不属于当前用户或尚未提交处理。

### 查询我的视频

`GET /api/video/auth/mine`

查询参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `cursor` | string | 否 | 上一页响应中的 `next_cursor` |
| `limit` | int | 否 | 每页数量，范围 1-50，默认 20 |

成功响应：`200 OK`，响应体为 [`VideoListResponse`](#videolistresponse)。该接口仅返回当前用户已发布且未软删除的视频；草稿不混入没有状态字段的 `VideoItem` 列表。

常见失败：`400` `cursor` 或 `limit` 不合法，或游标不是当前用户 `mine` 范围生成的值；`401` 未认证。互动统计查询暂不可用时返回 `503 Service Unavailable`，属于可重试的临时故障。

### 查询、点赞和取消点赞

`GET /api/video/auth/:id/like`

`PUT /api/video/auth/:id/like`

`DELETE /api/video/auth/:id/like`

路径参数 `id` 必须是大于 0 的已发布且未软删除视频标识，无需请求体。三个接口成功时均返回 `200 OK`：

```json
{
  "liked": true,
  "likes_count": 18
}
```

`liked` 表示当前认证用户的点赞状态，`likes_count` 是视频的实时点赞数。重复点赞保持已点赞状态，重复取消保持未点赞状态，因此两个写操作均可安全重试。

实际点赞或取消直接与互动事实在同一个 MySQL 事务提交，重复操作不追加事件；响应形状和认证方式不变。启动前须应用迁移 `000010_interaction_outbox`。事件写入失败返回安全 `500`，该事务不提交；提交后统计读取失败或提交响应丢失仍可能返回错误，不能仅凭 HTTP 错误判定没有发生变更。

worker 直接装配互动 Relay 与热度消费者，不改变互动 HTTP 的成功条件，API 不连接 RabbitMQ。`dispatched` 仅表示 broker 发布确认，不能表示热度更新完成。分钟桶保持 coverage=unverified，没有 MySQL 热榜快照；`GET /api/feed?scene=hot` 仍返回 `501`。这七项功能的布尔配置与环境变量已移除，本模块不新增 HTTP 接口或请求字段。

常见失败：`400` `id` 不合法，`401` 未认证，`404` 当前用户不存在或已注销，或视频不存在、未发布或已删除。

### 创建和删除评论

`POST /api/video/auth/:id/comments`

请求体：

```json
{
  "content": "很精彩"
}
```

`content` 去除首尾空格后不能为空，最多 1000 个 Unicode 字符。成功响应：`201 Created`

```json
{
  "comment": {
    "id": 301,
    "video_id": 100,
    "author": { "id": 7, "username": "bob" },
    "content": "很精彩",
    "created_at": "2026-08-26T08:00:00Z"
  }
}
```

`DELETE /api/video/auth/:id/comments/:commentID`

只有评论作者可以删除自己的评论。成功响应：`204 No Content`。删除为软删除，会立即从评论列表和视频 `comments_count` 中消失。

实际评论创建或软删除直接与互动事实在同一个 MySQL 事务保存，启动前须应用迁移 `000010_interaction_outbox`。事件写入失败使同一事务失败；重复删除返回 `404`，评论创建仍没有请求幂等键，重复 POST 可以创建多条评论。创建评论的作者展示信息在提交后读取，该读取失败不撤销已提交的评论和事件。删除只检查活动用户和评论归属，不额外要求视频当前仍公开；创建评论仍要求公开视频。

常见失败：`400` 路径参数或评论内容不合法，`401` 未认证，`403` 当前用户不是评论作者，`404` 用户或评论不存在、评论与路径视频不匹配；创建评论还会在视频不存在、未发布或已删除时返回 `404`。

### 删除自己的视频

`DELETE /api/video/auth/:id`

路径参数 `id` 必须是大于 0 的无符号整数。成功响应：`204 No Content`。

只有视频作者可以删除已发布视频。草稿和正在清扫的草稿不接受该接口，避免它们进入已发布视频的软删除保留期。已发布视频删除采用软删除：视频会立即从公开流和“我的视频”中消失，后台清扫任务在保留期结束后删除媒体文件和数据库记录。

常见失败：`400` `id` 不合法，`401` 未认证，`403` 当前用户不是作者，`404` 视频不存在或已删除。

## 典型调用顺序

1. `POST /api/user/register` 注册账号。
2. `POST /api/user/login` 获取访问令牌和刷新令牌。
3. 携带 `Authorization: Bearer <access_token>` 调用 `POST /api/video/auth/drafts` 创建草稿。
4. 使用草稿 ID 调用视频和封面上传接口。
5. 调用 `POST /api/video/auth/drafts/:id/publish`，不提交请求体。
6. 通过 `GET /api/video` 消费公开视频流；令牌即将过期或已过期时，使用 `POST /api/user/refresh` 更新令牌对。
7. 登录后可通过点赞、评论和关注接口完成互动；公开页面使用评论、粉丝和关注列表接口读取关系数据。
