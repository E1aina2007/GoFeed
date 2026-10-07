package domainaccount

import "context"

type Creator interface {
	Create(ctx context.Context, input CreateInput) (PublicAccount, error)
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
