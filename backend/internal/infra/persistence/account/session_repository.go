package infraaccount

import (
	"context"
	"errors"
	"time"

	domainaccount "gofeed/internal/domain/account"

	"gorm.io/gorm"
)

type SessionRepository struct {
	db *gorm.DB
}

func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

func (r *SessionRepository) Create(ctx context.Context, input domainaccount.SessionCreateInput) error {
	session := &AuthSession{
		ID:               input.ID,
		UserID:           input.UserID,
		RefreshTokenHash: input.RefreshTokenHash,
		ExpiresAt:        input.ExpiresAt,
	}
	return r.db.WithContext(ctx).Create(session).Error
}

func (r *SessionRepository) GetActiveByID(ctx context.Context, id string, userID uint) (domainaccount.Session, error) {
	var session AuthSession
	err := r.db.WithContext(ctx).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL AND expires_at > ?", id, userID, time.Now()).
		First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainaccount.Session{}, domainaccount.ErrInvalidSession
	}
	if err != nil {
		return domainaccount.Session{}, err
	}
	return domainaccount.Session{ID: session.ID, UserID: session.UserID, ExpiresAt: session.ExpiresAt}, nil
}

func (r *SessionRepository) GetActiveByRefreshTokenHash(ctx context.Context, hash string) (domainaccount.Session, error) {
	var session AuthSession
	err := r.db.WithContext(ctx).
		Where("refresh_token_hash = ? AND revoked_at IS NULL AND expires_at > ?", hash, time.Now()).
		First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainaccount.Session{}, domainaccount.ErrInvalidSession
	}
	if err != nil {
		return domainaccount.Session{}, err
	}
	return domainaccount.Session{ID: session.ID, UserID: session.UserID, ExpiresAt: session.ExpiresAt}, nil
}

// 原子替换刷新令牌，重复使用或并发竞争的旧令牌
// 在首次成功更新后无法再次轮换会话
func (r *SessionRepository) UpdateRefreshToken(ctx context.Context, session domainaccount.Session, expectedHash, nextHash string) error {
	result := r.db.WithContext(ctx).Model(&AuthSession{}).
		Where("id = ? AND user_id = ? AND refresh_token_hash = ? AND revoked_at IS NULL AND expires_at > ?", session.ID, session.UserID, expectedHash, time.Now()).
		Updates(map[string]any{"refresh_token_hash": nextHash})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return domainaccount.ErrInvalidSession
	}
	return nil
}

func (r *SessionRepository) UpdateSessionRevocation(ctx context.Context, id string, userID uint) error {
	now := time.Now()
	result := r.db.WithContext(ctx).Model(&AuthSession{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
		Update("revoked_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return domainaccount.ErrInvalidSession
	}
	return nil
}

func (r *SessionRepository) UpdateUserSessionRevocations(ctx context.Context, userID uint) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&AuthSession{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", now).Error
}

var (
	_ domainaccount.SessionReader = (*SessionRepository)(nil)
	_ domainaccount.SessionWriter = (*SessionRepository)(nil)
)
