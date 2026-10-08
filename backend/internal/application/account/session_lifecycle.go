package applicationaccount

import (
	"context"
	"time"

	domainaccount "gofeed/internal/domain/account"
)

const refreshTokenTTL = 7 * 24 * time.Hour

type RefreshTokenGenerator interface {
	GenerateRefreshToken() (string, error)
}

type RefreshTokenHasher interface {
	Hash(token string) string
}

type SessionLifecycleService struct {
	reader    domainaccount.SessionReader
	writer    domainaccount.SessionWriter
	generator RefreshTokenGenerator
	hasher    RefreshTokenHasher
	issuer    AccessTokenIssuer
}

var _ domainaccount.SessionLifecycle = (*SessionLifecycleService)(nil)

func NewSessionLifecycle(reader domainaccount.SessionReader, writer domainaccount.SessionWriter,
	generator RefreshTokenGenerator, hasher RefreshTokenHasher, issuer AccessTokenIssuer,
) *SessionLifecycleService {
	return &SessionLifecycleService{reader: reader, writer: writer, generator: generator, hasher: hasher, issuer: issuer}
}

func (s *SessionLifecycleService) Create(ctx context.Context, userID uint, username string) (domainaccount.TokenPair, error) {
	sessionID, err := s.generator.GenerateRefreshToken()
	if err != nil {
		return domainaccount.TokenPair{}, err
	}
	refreshToken, err := s.generator.GenerateRefreshToken()
	if err != nil {
		return domainaccount.TokenPair{}, err
	}

	session := domainaccount.SessionCreateInput{
		ID:               sessionID,
		UserID:           userID,
		RefreshTokenHash: s.hasher.Hash(refreshToken),
		ExpiresAt:        time.Now().Add(refreshTokenTTL),
	}
	if err := s.writer.Create(ctx, session); err != nil {
		return domainaccount.TokenPair{}, err
	}

	accessToken, err := s.issuer.GenerateToken(userID, username, session.ID)
	if err != nil {
		return domainaccount.TokenPair{}, err
	}
	return domainaccount.TokenPair{AccessToken: accessToken, RefreshToken: refreshToken, ExpiresAt: session.ExpiresAt}, nil
}

// UpdateRefreshToken 保留先查询、生成再 CAS，成功后由账户用例读取资料并签发令牌
func (s *SessionLifecycleService) UpdateRefreshToken(ctx context.Context, refreshToken string) (domainaccount.Session, string, error) {
	if refreshToken == "" {
		return domainaccount.Session{}, "", domainaccount.ErrInvalidSession
	}
	currentHash := s.hasher.Hash(refreshToken)
	session, err := s.reader.GetActiveByRefreshTokenHash(ctx, currentHash)
	if err != nil {
		return domainaccount.Session{}, "", err
	}
	nextRefreshToken, err := s.generator.GenerateRefreshToken()
	if err != nil {
		return domainaccount.Session{}, "", err
	}
	if err := s.writer.UpdateRefreshToken(ctx, session, currentHash, s.hasher.Hash(nextRefreshToken)); err != nil {
		return domainaccount.Session{}, "", err
	}
	return session, nextRefreshToken, nil
}

func (s *SessionLifecycleService) Validate(ctx context.Context, sessionID string, userID uint) error {
	if sessionID == "" || userID == 0 {
		return domainaccount.ErrInvalidSession
	}
	_, err := s.reader.GetActiveByID(ctx, sessionID, userID)
	return err
}

func (s *SessionLifecycleService) UpdateSessionRevocation(ctx context.Context, sessionID string, userID uint) error {
	return s.writer.UpdateSessionRevocation(ctx, sessionID, userID)
}
