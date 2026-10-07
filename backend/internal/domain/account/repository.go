package domainaccount

import "context"

type Creator interface {
	Create(ctx context.Context, input CreateInput) (PublicAccount, error)
}

type AccountSecurityWriter interface {
	UpdatePasswordAndRevokeSessions(ctx context.Context, input PasswordChange) error
	DeleteUserAndRevokeSessions(ctx context.Context, userID uint) error
}

type CredentialReader interface {
	GetByUsername(ctx context.Context, username string) (Credentials, error)
}

type SessionLifecycle interface {
	Create(ctx context.Context, userID uint, username string) (TokenPair, error)
	UpdateRefreshToken(ctx context.Context, refreshToken string) (Session, string, error)
	UpdateSessionRevocation(ctx context.Context, sessionID string, userID uint) error
}

type Reader interface {
	GetByID(ctx context.Context, id uint) (PublicAccount, error)
	GetUserList(ctx context.Context) ([]PublicAccount, error)
	GetUserListPage(ctx context.Context, position *ListPosition, limit int) ([]PublicAccount, error)
}

type PublishedVideoCounter interface {
	GetPublishedVideoCountByAuthor(ctx context.Context, authorID uint) (int64, error)
}

type ProfileMetricsReader interface {
	GetProfileMetrics(ctx context.Context, accountID uint) (ProfileMetrics, error)
}
