package user

import (
	"context"
	"errors"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// notDeleted adds a soft-delete filter to queries.
func (r *Repository) notDeleted(db *gorm.DB) *gorm.DB {
	return db.Where("deleted_at IS NULL")
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
	result := r.notDeleted(r.db.WithContext(ctx).Model(&User{})).
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

func (r *Repository) UpdatePassword(ctx context.Context, id uint, newPassword string) error {
	result := r.notDeleted(r.db.WithContext(ctx).Model(&User{})).
		Where("id = ?", id).
		Update("password", newPassword)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Repository) GetByID(ctx context.Context, id uint) (*User, error) {
	var user User
	if err := r.notDeleted(r.db.WithContext(ctx)).First(&user, id).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *Repository) GetByUsername(ctx context.Context, username string) (*User, error) {
	var account User
	if err := r.notDeleted(r.db.WithContext(ctx)).
		Where("username = ?", username).
		First(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (r *Repository) UpdateAvatar(ctx context.Context, accountID uint, avatarURL string) error {
	result := r.notDeleted(r.db.WithContext(ctx).Model(&User{})).
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
	result := r.notDeleted(r.db.WithContext(ctx).Model(&User{})).
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
	if err := r.notDeleted(r.db.WithContext(ctx)).Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// CheckDeleted checks whether a user exists (including soft-deleted ones).
// It returns (exists, isDeleted, error).
func (r *Repository) CheckDeleted(ctx context.Context, id uint) (bool, bool, error) {
	var user User
	if err := r.db.Unscoped().WithContext(ctx).Select("id", "deleted_at").First(&user, id).Error; err != nil {
		return false, false, err
	}
	isDeleted := user.DeletedAt.Valid && !user.DeletedAt.Time.IsZero()
	return true, isDeleted, nil
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
