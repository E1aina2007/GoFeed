package infraaccount

import (
	"context"

	domainaccount "gofeed/internal/domain/account"
)

type Creator struct {
	users *Repository
}

var _ domainaccount.Creator = (*Creator)(nil)

func NewCreator(users *Repository) *Creator {
	return &Creator{users: users}
}

func (r *Creator) Create(ctx context.Context, input domainaccount.CreateInput) (domainaccount.PublicAccount, error) {
	account := &User{Username: input.Username, Password: input.PasswordHash}
	if err := r.users.Create(ctx, account); err != nil {
		return domainaccount.PublicAccount{}, err
	}
	return publicAccount(account), nil
}
