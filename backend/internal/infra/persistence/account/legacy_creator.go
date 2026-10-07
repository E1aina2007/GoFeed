package infraaccount

import (
	"context"
	"errors"

	domainaccount "gofeed/internal/domain/account"
	"gofeed/internal/user"
)

type Creator struct {
	users *user.Repository
}

var _ domainaccount.Creator = (*Creator)(nil)

func NewCreator(users *user.Repository) *Creator {
	return &Creator{users: users}
}

func (r *Creator) Create(ctx context.Context, input domainaccount.CreateInput) (domainaccount.PublicAccount, error) {
	account := &user.User{Username: input.Username, Password: input.PasswordHash}
	if err := r.users.Create(ctx, account); err != nil {
		switch {
		case errors.Is(err, user.ErrUsernameTaken):
			return domainaccount.PublicAccount{}, domainaccount.ErrUsernameTaken
		case errors.Is(err, user.ErrInvalidInput):
			return domainaccount.PublicAccount{}, domainaccount.ErrInvalidInput
		default:
			return domainaccount.PublicAccount{}, err
		}
	}
	return publicAccount(account), nil
}
