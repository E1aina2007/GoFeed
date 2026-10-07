package user

import (
	"time"

	"gorm.io/gorm"
)

type User struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Username  string `gorm:"unique" json:"username"`
	Password  string `json:"-"`
	AvatarURL string `gorm:"type:varchar(512)" json:"avatar_url,omitempty"`
	Bio       string `gorm:"type:varchar(255)" json:"bio,omitempty"`

	// DeletedAt 触发 GORM 软删除：所有查询/更新/删除自动过滤 deleted_at IS NULL，
	// 如需包含软删记录请使用 Unscoped()
	DeletedAt gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}

type CreateRequest struct {
	Username string `json:"username" binding:"required,min=3,max=32"`
	Password string `json:"password" binding:"required,min=8,max=72"`
}

type UpdateNameRequest struct {
	NewUsername string `json:"new_username" binding:"required,min=3,max=32"`
}

type FindByIDResponse struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url,omitempty"`
	Bio       string `json:"bio,omitempty"`
}

// UserCursor 记录用户列表分页位置及固定的列表范围
// 游标由服务端签发，客户端只可原样传回同一列表
type UserCursor struct {
	Version int    `json:"v"`
	Kind    string `json:"k"`
	ID      uint   `json:"i"`
}

type FindByUsernameRequest struct {
	Username string `json:"username"`
}

type FindByUsernameResponse struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
}

type UpdatePasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required,min=8,max=72"`
	NewPassword string `json:"new_password" binding:"required,min=8,max=72"`
}

type UpdateProfileRequest struct {
	// AvatarURL 保留对象存储等外部存储实现的兼容能力，当前前端优先使用头像上传接口
	AvatarURL string `json:"avatar_url" binding:"omitempty,max=512"`
	Bio       string `json:"bio" binding:"omitempty,max=255"`
}

type LoginRequest struct {
	Username string `json:"username" binding:"required,min=3,max=32"`
	Password string `json:"password" binding:"required,min=8,max=72"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

type LoginResponse struct {
	AccessToken  string           `json:"access_token"`
	RefreshToken string           `json:"refresh_token"`
	ExpiresAt    time.Time        `json:"expires_at"`
	User         FindByIDResponse `json:"user"`
}

// ProfileMetrics 表示公开主页由互动关系计算出的实时统计值
type ProfileMetrics struct {
	TotalLikes    int64
	FollowerCount int64
	VloggerCount  int64
}
