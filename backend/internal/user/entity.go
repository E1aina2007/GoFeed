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

type FindByIDRequest struct {
	ID uint `json:"id"`
}

type FindByIDResponse struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url,omitempty"`
	Bio       string `json:"bio,omitempty"`
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

type GetProfileRequest struct {
	AccountID uint `json:"account_id"`
}

// Profile 表示公开资料读取所需的用户数据与聚合指标。
// HTTP 层负责将其中的用户实体转换为不含敏感字段的响应 DTO。
type Profile struct {
	Account    *User
	VideoCount int64
}

type GetProfileResponse struct {
	Account       FindByIDResponse `json:"account"`
	VideoCount    int64            `json:"video_count"`
	TotalLikes    int64            `json:"total_likes"`
	FollowerCount int64            `json:"follower_count"`
	VloggerCount  int64            `json:"vlogger_count"`
}
