package applicationaccount

import (
	"context"
	"io"
	"strings"

	domainaccount "gofeed/internal/domain/account"
)

type AvatarStorage interface {
	SaveAvatar(ctx context.Context, ownerID uint, filename string, src io.Reader) (string, error)
	RemoveAvatar(ctx context.Context, publicURL string) error
}

type ProfileService struct {
	accounts AccountReader
	writer   domainaccount.ProfileWriter
	storage  AvatarStorage
}

func NewProfile(accounts AccountReader, writer domainaccount.ProfileWriter, storage AvatarStorage) *ProfileService {
	return &ProfileService{accounts: accounts, writer: writer, storage: storage}
}

func (s *ProfileService) HasAvatarStorage() bool {
	return s.storage != nil
}

func (s *ProfileService) UpdateName(ctx context.Context, userID uint, username string) error {
	username, err := domainaccount.NormalizeNewUsername(username)
	if err != nil {
		return err
	}
	return s.writer.UpdateName(ctx, userID, username)
}

func (s *ProfileService) UpdateProfile(ctx context.Context, userID uint, changes domainaccount.ProfileChanges) error {
	changes, err := domainaccount.NormalizeProfileChanges(changes)
	if err != nil {
		return err
	}
	return s.writer.UpdateProfile(ctx, userID, changes)
}

// UpdateAvatar 保留保存后写库、写库失败清理新对象及成功后清理旧对象的顺序
func (s *ProfileService) UpdateAvatar(ctx context.Context, userID uint, filename string, src io.Reader) (string, error) {
	current, err := s.accounts.GetByID(ctx, userID)
	if err != nil {
		return "", err
	}
	avatarURL, err := s.storage.SaveAvatar(ctx, userID, strings.TrimSpace(filename), src)
	if err != nil {
		return "", err
	}
	if err := s.updateAvatarURL(ctx, userID, avatarURL); err != nil {
		_ = s.storage.RemoveAvatar(ctx, avatarURL)
		return "", err
	}
	if current.AvatarURL != "" && current.AvatarURL != avatarURL {
		_ = s.storage.RemoveAvatar(ctx, current.AvatarURL)
	}
	return avatarURL, nil
}

func (s *ProfileService) updateAvatarURL(ctx context.Context, userID uint, avatarURL string) error {
	avatarURL, err := domainaccount.NormalizeAvatarURL(avatarURL)
	if err != nil {
		return err
	}
	return s.writer.UpdateAvatar(ctx, userID, avatarURL)
}
