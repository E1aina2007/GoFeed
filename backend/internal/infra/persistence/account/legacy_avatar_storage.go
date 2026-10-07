package infraaccount

import (
	"context"
	"io"

	applicationaccount "gofeed/internal/application/account"
	"gofeed/internal/video"
)

type AvatarStorage struct {
	storage *video.LocalStorage
}

var _ applicationaccount.AvatarStorage = (*AvatarStorage)(nil)

func NewAvatarStorage(storage *video.LocalStorage) applicationaccount.AvatarStorage {
	if storage == nil {
		return nil
	}
	return &AvatarStorage{storage: storage}
}

func (s *AvatarStorage) SaveAvatar(ctx context.Context, ownerID uint, filename string, src io.Reader) (string, error) {
	return s.storage.SaveAvatar(ctx, ownerID, filename, src)
}

func (s *AvatarStorage) RemoveAvatar(ctx context.Context, publicURL string) error {
	return s.storage.RemoveAvatar(ctx, publicURL)
}
