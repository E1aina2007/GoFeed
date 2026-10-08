package user

import "gorm.io/gorm"

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
