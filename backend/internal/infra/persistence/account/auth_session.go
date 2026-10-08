package infraaccount

import "time"

// AuthSession 表示一个可独立撤销的登录会话，刷新令牌只以 SHA-256 哈希入库
// 即使数据库泄露也无法直接作为令牌使用
// 类型名通过 GORM 默认命名规则映射到迁移创建的表 auth_sessions
type AuthSession struct {
	ID               string     `gorm:"primaryKey;size:64" json:"id"`
	UserID           uint       `gorm:"not null;index:idx_auth_sessions_user_active" json:"user_id"`
	RefreshTokenHash string     `gorm:"size:64;not null;uniqueIndex" json:"-"`
	ExpiresAt        time.Time  `gorm:"not null;index" json:"expires_at"`
	RevokedAt        *time.Time `gorm:"index:idx_auth_sessions_user_active" json:"revoked_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}
