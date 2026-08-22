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
| POST | `/api/user/auth/logout` | 是 | 退出当前会话 |
| PATCH | `/api/user/auth/name` | 是 | 修改用户名 |
| PATCH | `/api/user/auth/password` | 是 | 修改密码并撤销全部会话 |
| PATCH | `/api/user/auth/profile` | 是 | 修改个人资料 |
| DELETE | `/api/user/auth` | 是 | 注销当前账号 |
| GET | `/api/video` | 否 | 查询公开视频流 |
| GET | `/api/video/:id` | 否 | 查询公开视频详情 |
| POST | `/api/video/auth/drafts` | 是 | 创建视频草稿 |
| POST | `/api/video/auth/drafts/:id/play` | 是 | 上传草稿视频文件 |
| POST | `/api/video/auth/drafts/:id/cover` | 是 | 上传草稿封面图片 |
| POST | `/api/video/auth/drafts/:id/publish` | 是 | 发布完整草稿 |
| GET | `/api/video/auth/mine` | 是 | 查询我的视频 |
| DELETE | `/api/video/auth/:id` | 是 | 删除自己的视频 |

## 公共数据结构

### `PublicUser`

```json
{
  "id": 42,
  "username": "alice",
  "avatar_url": "/static/covers/42/avatar.png",
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
    "avatar_url": "/static/covers/42/avatar.png"
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
      "avatar_url": "/static/covers/42/avatar.png",
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
    "avatar_url": "/static/covers/42/avatar.png",
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
    "avatar_url": "/static/covers/42/avatar.png",
    "bio": "视频创作者"
  },
  "video_count": 3,
  "total_likes": 0,
  "follower_count": 0,
  "vlogger_count": 0
}
```

`video_count` 统计当前已发布、未软删除的视频数量。`total_likes`、`follower_count` 和 `vlogger_count` 为已预留字段，当前固定返回 `0`。

常见失败：`400` `id` 格式不正确，`404` 用户不存在或已注销。

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

### 修改个人资料

`PATCH /api/user/auth/profile`

请求体至少提供一个非空字段：

```json
{
  "avatar_url": "/static/covers/42/avatar.png",
  "bio": "视频创作者"
}
```

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `avatar_url` | string | 否 | 最多 512 个字符 |
| `bio` | string | 否 | 最多 255 个字符 |

空字符串不会更新对应字段，因此该接口当前不能清空头像或简介。

成功响应：`200 OK`

```json
{
  "message": "profile updated successfully"
}
```

常见失败：`400` 请求体不合法，`401` 未认证。

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
    "created_at": "2026-08-21T08:00:00Z",
    "updated_at": "2026-08-21T08:00:00Z"
  }
}
```

草稿没有 `published_at`，也没有客户端可填写的媒体字段。

常见失败：`400` 请求体或标题、简介不合法，`401` 未认证。

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

### 删除自己的视频

`DELETE /api/video/auth/:id`

路径参数 `id` 必须是大于 0 的无符号整数。成功响应：`204 No Content`。

只有视频作者可以删除。删除采用软删除：视频会立即从公开流和“我的视频”中消失，后台清扫任务在保留期结束后删除媒体文件和数据库记录。

常见失败：`400` `id` 不合法，`401` 未认证，`403` 当前用户不是作者，`404` 视频不存在或已删除。

## 典型调用顺序

1. `POST /api/user/register` 注册账号。
2. `POST /api/user/login` 获取访问令牌和刷新令牌。
3. 携带 `Authorization: Bearer <access_token>` 调用 `POST /api/video/auth/drafts` 创建草稿。
4. 使用草稿 ID 调用视频和封面上传接口。
5. 调用 `POST /api/video/auth/drafts/:id/publish`，不提交请求体。
6. 通过 `GET /api/video` 消费公开视频流；令牌即将过期或已过期时，使用 `POST /api/user/refresh` 更新令牌对。
