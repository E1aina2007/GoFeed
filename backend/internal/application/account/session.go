package applicationaccount

import (
	"context"
	"fmt"
	"strings"

	domainaccount "gofeed/internal/domain/account"
)

type AccountReader interface {
	GetByID(ctx context.Context, id uint) (domainaccount.PublicAccount, error)
}

type PasswordVerifier interface {
	Compare(passwordHash, password string) error
}

type AccessTokenIssuer interface {
	GenerateToken(userID uint, username, sessionID string) (string, error)
}

type SessionService struct {
	credentials domainaccount.CredentialReader
	accounts    AccountReader
	verifier    PasswordVerifier
	sessions    domainaccount.SessionLifecycle
	issuer      AccessTokenIssuer
}

func NewSessions(credentials domainaccount.CredentialReader, accounts AccountReader, verifier PasswordVerifier,
	sessions domainaccount.SessionLifecycle, issuer AccessTokenIssuer,
) *SessionService {
	return &SessionService{credentials: credentials, accounts: accounts, verifier: verifier, sessions: sessions, issuer: issuer}
}

func (s *SessionService) Login(ctx context.Context, username, password string) (domainaccount.SessionResult, error) {
	credentials, err := s.credentials.GetByUsername(ctx, strings.TrimSpace(username))
	if err != nil {
		return domainaccount.SessionResult{}, err
	}
	if err := s.verifier.Compare(credentials.PasswordHash, password); err != nil {
		return domainaccount.SessionResult{}, domainaccount.ErrInvalidCredentials
	}
	pair, err := s.sessions.Create(ctx, credentials.Account.ID, credentials.Account.Username)
	if err != nil {
		return domainaccount.SessionResult{}, fmt.Errorf("%w: %w", domainaccount.ErrSessionCreationFailed, err)
	}
	return domainaccount.SessionResult{Tokens: pair, Account: credentials.Account}, nil
}

// UpdateRefreshToken 保留先轮换后读取用户，读取失败时尝试撤销当前会话
func (s *SessionService) UpdateRefreshToken(ctx context.Context, refreshToken string) (domainaccount.SessionResult, error) {
	session, nextRefreshToken, err := s.sessions.UpdateRefreshToken(ctx, refreshToken)
	if err != nil {
		return domainaccount.SessionResult{}, fmt.Errorf("%w: %w", domainaccount.ErrInvalidRefreshToken, err)
	}
	account, err := s.accounts.GetByID(ctx, session.UserID)
	if err != nil {
		_ = s.sessions.UpdateSessionRevocation(ctx, session.ID, session.UserID)
		return domainaccount.SessionResult{}, fmt.Errorf("%w: %w", domainaccount.ErrInvalidRefreshToken, err)
	}
	accessToken, err := s.issuer.GenerateToken(account.ID, account.Username, session.ID)
	if err != nil {
		return domainaccount.SessionResult{}, fmt.Errorf("%w: %w", domainaccount.ErrAccessTokenCreationFailed, err)
	}
	return domainaccount.SessionResult{
		Tokens:  domainaccount.TokenPair{AccessToken: accessToken, RefreshToken: nextRefreshToken, ExpiresAt: session.ExpiresAt},
		Account: account,
	}, nil
}

func (s *SessionService) UpdateSessionRevocation(ctx context.Context, sessionID string, userID uint) error {
	if err := s.sessions.UpdateSessionRevocation(ctx, sessionID, userID); err != nil {
		return fmt.Errorf("%w: %w", domainaccount.ErrInvalidSession, err)
	}
	return nil
}
