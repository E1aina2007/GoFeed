package infraaccount

import (
	"context"
	"errors"

	"gofeed/internal/auth"
	domainaccount "gofeed/internal/domain/account"
)

type Sessions struct {
	sessions *auth.SessionService
}

var _ domainaccount.SessionLifecycle = (*Sessions)(nil)

func NewSessions(sessions *auth.SessionService) *Sessions {
	return &Sessions{sessions: sessions}
}

func (s *Sessions) Create(ctx context.Context, userID uint, username string) (domainaccount.TokenPair, error) {
	pair, err := s.sessions.Create(ctx, userID, username)
	if err != nil {
		return domainaccount.TokenPair{}, sessionError(err)
	}
	return domainaccount.TokenPair{AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken, ExpiresAt: pair.ExpiresAt}, nil
}

func (s *Sessions) UpdateRefreshToken(ctx context.Context, refreshToken string) (domainaccount.Session, string, error) {
	session, nextRefreshToken, err := s.sessions.UpdateRefreshToken(ctx, refreshToken)
	if err != nil {
		return domainaccount.Session{}, "", sessionError(err)
	}
	return domainaccount.Session{ID: session.ID, UserID: session.UserID, ExpiresAt: session.ExpiresAt}, nextRefreshToken, nil
}

func (s *Sessions) UpdateSessionRevocation(ctx context.Context, sessionID string, userID uint) error {
	return sessionError(s.sessions.UpdateSessionRevocation(ctx, sessionID, userID))
}

func sessionError(err error) error {
	if errors.Is(err, auth.ErrSessionInvalid) {
		return domainaccount.ErrInvalidSession
	}
	return err
}
