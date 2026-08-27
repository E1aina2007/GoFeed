# GoFeed API 文档

本文档依据后端当前实际注册的路由整理，路由定义位于 `backend/internal/router/router.go`。

## 基本约定

- 开发环境服务地址：`http://localhost:8080`
- 除文件上传接口外，请求和响应均使用 `application/json; charset=utf-8`
- 需要认证的接口必须携带请求头：`Authorization: Bearer <access_token>`
- `access_token` 有效期为 15 分钟；刷新令牌有效期为 7 天。刷新令牌每次调用刷新接口后都会轮换，旧令牌立即失效。
- 时间字段使用 RFC 3339 格式，例如 `2026-08-19T08:00:00Z`
- 业务处理器返回错误时，响应格式为 `{"error":"错误说明"}`。未注册路径和未支持方法由 Gin 返回默认 404/405 响应。

## 路由总览

| 方法 | 路径 | 认证 | 说明 |
| --- | --- | --- | --- |
| GET | `/health` | 否 | 健康检查 |
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
| GET | `/api/video` | 否 | 查询公开视频流 |
| GET | `/api/video/:id` | 否 | 查询公开视频详情 |
| GET | `/api/video/:id/comments` | 否 | 查询公开视频评论 |
| POST | `/api/video/auth/drafts` | 是 | 创建视频草稿 |
| GET | `/api/video/auth/drafts/:id` | 是 | 查询当前草稿状态 |
| POST | `/api/video/auth/drafts/:id/play` | 是 | 上传草稿视频文件 |
| POST | `/api/video/auth/drafts/:id/cover` | 是 | 上传草稿封面图片 |
| POST | `/api/video/auth/drafts/:id/publish` | 是 | 发布完整草稿 |
| DELETE | `/api/video/auth/drafts/:id` | 是 | 丢弃草稿并排入异步清扫 |
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
  "next_cursor": "eyJwdWJsaXNoZWRfYXQiOiIyMDI2LTA4LTE5VDA4OjAwOjAwWiIsImlkIjoxMDB9"
}
```

当没有下一页时，`next_cursor` 不返回。后续分页将该字段原样作为 `cursor` 查询参数传回；它是服务端生成的不透明值，不应自行构造或修改。

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
  "next_cursor": "eyJjcmVhdGVkX2F0IjoiMjAyNi0wOC0yNlQwODowMDowMFoiLCJpZCI6MzAxfQ"
}
```

评论按创建时间和 ID 倒序排列。已删除评论不会返回；评论作者已注销时，作者资料会显示为 `已注销用户`。

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
  "next_cursor": "eyJjcmVhdGVkX2F0IjoiMjAyNi0wOC0yNlQwODowMDowMFoiLCJpZCI6N30"
}
```

粉丝和关注列表均按建立关注关系的时间和关系 ID 倒序分页。`user` 为关系另一端的公开资料。

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
| `username` | string | 是 | 去除首尾空格后长度为 3-32 |
| `password` | string | 是 | 长度为 8-72 |

成功响应：`201 Created`

```json
{
  "user": {
    "id": 42,
    "username": "alice"
  }
}
```

常见失败：`400` 请求体格式或字段不合法，`409` 用户名已存在。

### 登录

`POST /api/user/login`

请求体字段与注册接口相同。

成功响应：`200 OK`，响应体为 [`LoginResponse`](#loginresponse)。

常见失败：`400` 请求体格式不合法，`401` 用户名或密码错误。

### 刷新令牌

`POST /api/user/refresh`

请求体：

```json
{
  "refresh_token": "<refresh-token>"
}
```

成功响应：`200 OK`，响应体为 [`LoginResponse`](#loginresponse)。请使用响应中的新刷新令牌替换本地旧值。

常见失败：`400` 请求体缺少 `refresh_token`，`401` 令牌无效、过期、已撤销或已被使用。

### 查询用户列表

`GET /api/user`

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

该接口当前不分页，仅返回未注销用户；密码不会返回。

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

`video_count` 统计当前已发布、未软删除的视频数量。`total_likes` 统计该用户当前可见视频的点赞关系数，`follower_count` 统计活跃粉丝数，`vlogger_count` 统计仍可见的关注对象数；三项互动统计均由关系表实时计算。

常见失败：`400` `id` 格式不正确，`404` 用户不存在或已注销。

### 查询粉丝和关注列表

`GET /api/user/:id/followers`

`GET /api/user/:id/following`

路径参数 `id` 必须是大于 0 的无符号整数。两个接口都支持以下查询参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `cursor` | string | 否 | 上一页响应中的 `next_cursor` |
| `limit` | int | 否 | 每页数量，范围 1-50，默认 20 |

成功响应：`200 OK`，响应体为 [`FollowListResponse`](#followlistresponse)。`followers` 返回关注该用户的账号，`following` 返回该用户正在关注的账号。

常见失败：`400` 路径参数、`cursor` 或 `limit` 不合法，`404` 用户不存在或已注销。

### 退出当前会话

`POST /api/user/auth/logout`

无需请求体。成功响应：`204 No Content`。

该操作仅撤销当前访问令牌所属的会话，不影响同一账号在其他设备创建的会话。

常见失败：`401` 未携带、格式错误、过期或已撤销的访问令牌。

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
| `old_password` | string | 是 | 长度为 8-72 |
| `new_password` | string | 是 | 长度为 8-72 |

成功响应：`200 OK`

```json
{
  "message": "password updated; sign in again"
}
```

成功后当前账号的全部会话都会被撤销，必须重新登录才能继续调用受保护接口。

常见失败：`400` 请求体或新密码不合法，`401` 未认证，`403` 旧密码错误。

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

无需请求体。成功响应：`204 No Content`。

账号会被软删除，并撤销该账号的全部会话。软删除后公开用户接口和登录接口均不可再访问该账号；后台清扫任务会在保留期结束后彻底清除记录。

常见失败：`401` 未认证或令牌已失效。

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

常见失败：`400` `author_id`、`cursor` 或 `limit` 不合法。

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

常见失败：`400` `id` 不合法，`404` 视频不存在、未发布或已删除。

### 查询视频评论

`GET /api/video/:id/comments`

路径参数 `id` 必须是大于 0 的已发布且未软删除视频标识。查询参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `cursor` | string | 否 | 上一页响应中的 `next_cursor` |
| `limit` | int | 否 | 每页数量，范围 1-50，默认 20 |

成功响应：`200 OK`，响应体为 [`CommentListResponse`](#commentlistresponse)。

常见失败：`400` 路径参数、`cursor` 或 `limit` 不合法，`404` 视频不存在、未发布或已删除。

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

草稿保留期由 `RETENTION_VIDEO_DRAFT_HOURS` 控制。保留期届满后，后台 sweeper 会在下一轮将未发布草稿转入不可逆的 `purging` 状态；进入该状态后不能继续上传或发布。

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

路径参数 `id` 必须是当前用户处于 `draft` 状态的草稿标识。服务端在单个事务中将草稿转换为 `purging`，不会在 HTTP 请求内直接删除媒体文件；后台 sweeper 会使用既有租约和检查点完成可重试清扫。

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

`202` 只表示服务端已持久化接受清扫，不代表媒体已物理删除。若客户端在收到响应前断开，可对仍处于 `purging` 的同一草稿重复调用该接口，响应仍为 `202`；草稿被 sweeper 最终硬删除后再次请求会返回 `404`。

常见失败：`400` 路径参数不合法，`401` 未认证，`403` 草稿不属于当前用户，`404` 草稿不存在或已被清扫，`409` 视频已发布或草稿不再可进入清扫。

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

路径参数 `id` 必须是当前用户完整的 `draft` 草稿。该接口没有请求体；客户端不能提交 `play_url`、`cover_url`、物理文件名或原始文件名。服务端在单个事务中验证两类媒体都已绑定，再写入实际 `published_at` 并转换为 `published`。

成功响应：`201 Created`，响应体为 `{"video": <VideoItem>}`。

常见失败：`400` 提交了请求体或路径参数不合法，`401` 未认证，`403` 草稿不属于当前用户，`404` 草稿不存在，`409` 草稿未完成或不再可发布。

### 查询我的视频

`GET /api/video/auth/mine`

查询参数：

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `cursor` | string | 否 | 上一页响应中的 `next_cursor` |
| `limit` | int | 否 | 每页数量，范围 1-50，默认 20 |

成功响应：`200 OK`，响应体为 [`VideoListResponse`](#videolistresponse)。该接口仅返回当前用户已发布且未软删除的视频；草稿不混入没有状态字段的 `VideoItem` 列表。

常见失败：`400` `cursor` 或 `limit` 不合法，`401` 未认证。

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

常见失败：`400` 路径参数或评论内容不合法，`401` 未认证，`403` 当前用户不是评论作者，`404` 用户、视频或评论不存在，或视频未发布/已删除。

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
