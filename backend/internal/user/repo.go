package user

import (
	"context"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, user *User) error {
	if err := r.db.WithContext(ctx).Create(user).Error; err != nil {
		if isDuplicateKey(err) {
			return ErrUsernameTaken
		}
		return err
	}
	return nil
}

func (r *Repository) UpdateName(ctx context.Context, id uint, newName string) error {
	result := r.db.WithContext(ctx).Model(&User{}).
		Where("id = ?", id).
		Update("username", newName)
	if result.Error != nil {
		if isDuplicateKey(result.Error) {
			return ErrUsernameTaken
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func (r *Repository) UpdatePassword(ctx context.Context, id uint, expectedHash, newPassword string) error {
	result := r.db.WithContext(ctx).Model(&User{}).
		Where("id = ? AND password = ?", id, expectedHash).
		Update("password", newPassword)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Repository) GetByID(ctx context.Context, id uint) (*User, error) {
	var user User
	if err := r.db.WithContext(ctx).First(&user, id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *Repository) GetByUsername(ctx context.Context, username string) (*User, error) {
	var account User
	if err := r.db.WithContext(ctx).
		Where("username = ?", username).
		First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (r *Repository) UpdateAvatar(ctx context.Context, accountID uint, avatarURL string) error {
	result := r.db.WithContext(ctx).Model(&User{}).
		Where("id = ?", accountID).
		Update("avatar_url", avatarURL)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Repository) UpdateFields(ctx context.Context, id uint, updates map[string]any) error {
	result := r.db.WithContext(ctx).Model(&User{}).
		Where("id = ?", id).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Repository) GetAll(ctx context.Context) ([]*User, error) {
	var users []*User
	if err := r.db.WithContext(ctx).Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// SoftDelete
func (r *Repository) Delete(ctx context.Context, id uint) error {
	result := r.db.WithContext(ctx).Delete(&User{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// PurgeExpired 硬删除宽限期已届满的注销用户及其会话数据，返回删除的用户数
// cutoff 由调用方计算，便于测试指定任意时间点而不依赖真实时钟
func (r *Repository) PurgeExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	var purged int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先清该批用户的会话，避免留下孤儿数据
		if err := tx.Exec(
			"DELETE FROM auth_sessions WHERE user_id IN (SELECT id FROM users WHERE deleted_at IS NOT NULL AND deleted_at <= ?)",
			cutoff,
		).Error; err != nil {
			return err
		}
		// 原生 SQL 硬删除不受 GORM 软删除过滤影响
		result := tx.Exec(
			"DELETE FROM users WHERE deleted_at IS NOT NULL AND deleted_at <= ?",
			cutoff,
		)
		if result.Error != nil {
			return result.Error
		}
		purged = result.RowsAffected
		return nil
	})
	return purged, err
}
