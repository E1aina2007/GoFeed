package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"gorm.io/gorm"
)

const refreshTokenTTL = 7 * 24 * time.Hour

var ErrSessionInvalid = errors.New("session is invalid or expired")

// Session represents one independently revocable login session. Refresh tokens
// are stored only as SHA-256 hashes so a database leak cannot be used as a token.
type Session struct {
	ID               string     `gorm:"primaryKey;size:64" json:"id"`
	UserID           uint       `gorm:"not null;index:idx_auth_sessions_user_active" json:"user_id"`
	RefreshTokenHash string     `gorm:"size:64;not null;uniqueIndex" json:"-"`
	ExpiresAt        time.Time  `gorm:"not null;index" json:"expires_at"`
	RevokedAt        *time.Time `gorm:"index:idx_auth_sessions_user_active" json:"revoked_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type SessionRepository struct {
	db *gorm.DB
}

func NewSessionRepository(db *gorm.DB) *SessionRepository {
	return &SessionRepository{db: db}
}

func (r *SessionRepository) Create(ctx context.Context, session *Session) error {
	return r.db.WithContext(ctx).Create(session).Error
}

func (r *SessionRepository) FindActiveByID(ctx context.Context, id string, userID uint) (*Session, error) {
	var session Session
	err := r.db.WithContext(ctx).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL AND expires_at > ?", id, userID, time.Now()).
		First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (r *SessionRepository) FindActiveByRefreshTokenHash(ctx context.Context, hash string) (*Session, error) {
	var session Session
	err := r.db.WithContext(ctx).
		Where("refresh_token_hash = ? AND revoked_at IS NULL AND expires_at > ?", hash, time.Now()).
		First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSessionInvalid
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

// RotateRefresh atomically replaces a refresh token. A reused or racing token
// cannot rotate the session after the first successful update.
func (r *SessionRepository) RotateRefresh(ctx context.Context, session *Session, expectedHash, nextHash string) error {
	result := r.db.WithContext(ctx).Model(&Session{}).
		Where("id = ? AND user_id = ? AND refresh_token_hash = ? AND revoked_at IS NULL AND expires_at > ?", session.ID, session.UserID, expectedHash, time.Now()).
		Updates(map[string]any{"refresh_token_hash": nextHash})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrSessionInvalid
	}
	return nil
}

func (r *SessionRepository) Revoke(ctx context.Context, id string, userID uint) error {
	now := time.Now()
	result := r.db.WithContext(ctx).Model(&Session{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
		Update("revoked_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrSessionInvalid
	}
	return nil
}

func (r *SessionRepository) RevokeAllForUser(ctx context.Context, userID uint) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&Session{}).
		Where("user_id = ? AND revoked_at IS NULL", userID).
		Update("revoked_at", now).Error
}

type SessionService struct {
	repo *SessionRepository
}

func NewSessionService(repo *SessionRepository) *SessionService {
	return &SessionService{repo: repo}
}

type TokenPair struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (s *SessionService) Create(ctx context.Context, userID uint, username string) (*TokenPair, error) {
	sessionID, err := GenerateRefreshToken()
	if err != nil {
		return nil, err
	}
	refreshToken, err := GenerateRefreshToken()
	if err != nil {
		return nil, err
	}

	session := &Session{
		ID:               sessionID,
		UserID:           userID,
		RefreshTokenHash: hashToken(refreshToken),
		ExpiresAt:        time.Now().Add(refreshTokenTTL),
	}
	if err := s.repo.Create(ctx, session); err != nil {
		return nil, err
	}

	accessToken, err := GenerateToken(userID, username, session.ID)
	if err != nil {
		return nil, err
	}
	return &TokenPair{AccessToken: accessToken, RefreshToken: refreshToken, ExpiresAt: session.ExpiresAt}, nil
}

// Refresh replaces the submitted refresh token while preserving the session ID.
// The caller generates the access token after loading the current user profile.
func (s *SessionService) Refresh(ctx context.Context, refreshToken string) (*Session, string, error) {
	if refreshToken == "" {
		return nil, "", ErrSessionInvalid
	}
	currentHash := hashToken(refreshToken)
	session, err := s.repo.FindActiveByRefreshTokenHash(ctx, currentHash)
	if err != nil {
		return nil, "", err
	}
	nextRefreshToken, err := GenerateRefreshToken()
	if err != nil {
		return nil, "", err
	}
	if err := s.repo.RotateRefresh(ctx, session, currentHash, hashToken(nextRefreshToken)); err != nil {
		return nil, "", err
	}
	return session, nextRefreshToken, nil
}

func (s *SessionService) Validate(ctx context.Context, sessionID string, userID uint) error {
	if sessionID == "" || userID == 0 {
		return ErrSessionInvalid
	}
	_, err := s.repo.FindActiveByID(ctx, sessionID, userID)
	return err
}

func (s *SessionService) Revoke(ctx context.Context, sessionID string, userID uint) error {
	return s.repo.Revoke(ctx, sessionID, userID)
}

func (s *SessionService) RevokeAllForUser(ctx context.Context, userID uint) error {
	return s.repo.RevokeAllForUser(ctx, userID)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
