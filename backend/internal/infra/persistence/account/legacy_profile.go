package infraaccount

import (
	"context"
	"errors"

	domainaccount "gofeed/internal/domain/account"
	"gofeed/internal/user"
)

type ProfileWriter struct {
	users *user.Repository
}

var _ domainaccount.ProfileWriter = (*ProfileWriter)(nil)

func NewProfileWriter(users *user.Repository) *ProfileWriter {
	return &ProfileWriter{users: users}
}

func (w *ProfileWriter) UpdateName(ctx context.Context, userID uint, username string) error {
	err := w.users.UpdateName(ctx, userID, username)
	if errors.Is(err, user.ErrUsernameTaken) {
		return domainaccount.ErrUsernameTaken
	}
	return accountError(err)
}

func (w *ProfileWriter) UpdateProfile(ctx context.Context, userID uint, changes domainaccount.ProfileChanges) error {
	updates := map[string]any{}
	if changes.Bio != "" {
		updates["bio"] = changes.Bio
	}
	if changes.AvatarURL != "" {
		updates["avatar_url"] = changes.AvatarURL
	}
	return accountError(w.users.UpdateFields(ctx, userID, updates))
}

func (w *ProfileWriter) UpdateAvatar(ctx context.Context, userID uint, avatarURL string) error {
	return accountError(w.users.UpdateAvatar(ctx, userID, avatarURL))
}
