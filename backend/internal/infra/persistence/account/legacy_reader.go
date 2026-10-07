package infraaccount

import (
	"context"
	"errors"

	domainaccount "gofeed/internal/domain/account"
	"gofeed/internal/user"

	"gorm.io/gorm"
)

type Reader struct {
	users *user.Repository
}

type PublishedVideoCounter struct {
	counter user.PublishedVideoCounter
}

type ProfileMetricsReader struct {
	reader user.ProfileMetricsReader
}

var (
	_ domainaccount.Reader                = (*Reader)(nil)
	_ domainaccount.PublishedVideoCounter = (*PublishedVideoCounter)(nil)
	_ domainaccount.ProfileMetricsReader  = (*ProfileMetricsReader)(nil)
)

func NewReader(users *user.Repository) *Reader {
	return &Reader{users: users}
}

func NewPublishedVideoCounter(counter user.PublishedVideoCounter) domainaccount.PublishedVideoCounter {
	if counter == nil {
		return nil
	}
	return &PublishedVideoCounter{counter: counter}
}

func NewProfileMetricsReader(reader user.ProfileMetricsReader) domainaccount.ProfileMetricsReader {
	if reader == nil {
		return nil
	}
	return &ProfileMetricsReader{reader: reader}
}

func (r *Reader) GetByID(ctx context.Context, id uint) (domainaccount.PublicAccount, error) {
	account, err := r.users.GetByID(ctx, id)
	if err != nil {
		return domainaccount.PublicAccount{}, accountError(err)
	}
	return publicAccount(account), nil
}

func (r *Reader) GetUserList(ctx context.Context) ([]domainaccount.PublicAccount, error) {
	accounts, err := r.users.GetUserList(ctx)
	if err != nil {
		return nil, accountError(err)
	}
	return publicAccounts(accounts), nil
}

func (r *Reader) GetUserListPage(ctx context.Context, position *domainaccount.ListPosition, limit int) ([]domainaccount.PublicAccount, error) {
	var cursor *user.UserCursor
	if position != nil {
		cursor = &user.UserCursor{ID: position.ID}
	}
	accounts, err := r.users.GetUserListPage(ctx, cursor, limit)
	if err != nil {
		return nil, accountError(err)
	}
	return publicAccounts(accounts), nil
}

func (r *PublishedVideoCounter) GetPublishedVideoCountByAuthor(ctx context.Context, authorID uint) (int64, error) {
	count, err := r.counter.GetPublishedVideoCountByAuthor(ctx, authorID)
	return count, accountError(err)
}

func (r *ProfileMetricsReader) GetProfileMetrics(ctx context.Context, accountID uint) (domainaccount.ProfileMetrics, error) {
	metrics, err := r.reader.GetProfileMetrics(ctx, accountID)
	if err != nil {
		return domainaccount.ProfileMetrics{}, accountError(err)
	}
	return domainaccount.ProfileMetrics{
		TotalLikes: metrics.TotalLikes, FollowerCount: metrics.FollowerCount, VloggerCount: metrics.VloggerCount,
	}, nil
}

func publicAccount(account *user.User) domainaccount.PublicAccount {
	return domainaccount.PublicAccount{ID: account.ID, Username: account.Username, AvatarURL: account.AvatarURL, Bio: account.Bio}
}

func publicAccounts(accounts []*user.User) []domainaccount.PublicAccount {
	result := make([]domainaccount.PublicAccount, 0, len(accounts))
	for _, account := range accounts {
		result = append(result, publicAccount(account))
	}
	return result
}

func accountError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domainaccount.ErrUserNotFound
	}
	return err
}
