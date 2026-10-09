package infraaccount

import (
	"context"
	"errors"

	domainaccount "gofeed/internal/domain/account"
	domainvideo "gofeed/internal/domain/video"
)

const deletedUsername = "已注销用户"

type AuthorReader struct {
	accounts domainaccount.PublicAccountReader
}

var _ domainvideo.AuthorReader = (*AuthorReader)(nil)

func NewAuthorReader(accounts domainaccount.PublicAccountReader) *AuthorReader {
	return &AuthorReader{accounts: accounts}
}

// GetPublicAuthor 在账户注销或不存在时保留原 ID 并返回占位作者
func (r *AuthorReader) GetPublicAuthor(ctx context.Context, id uint) (domainvideo.Author, error) {
	account, err := r.accounts.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, domainaccount.ErrUserNotFound) {
			return domainvideo.Author{ID: id, Username: deletedUsername}, nil
		}
		return domainvideo.Author{}, err
	}
	return domainvideo.Author{ID: account.ID, Username: account.Username, AvatarURL: account.AvatarURL}, nil
}

// GetPublicAuthors 按首次出现顺序去重并批量读取，缺失账户补占位作者
func (r *AuthorReader) GetPublicAuthors(ctx context.Context, ids []uint) (map[uint]domainvideo.Author, error) {
	authors := make(map[uint]domainvideo.Author, len(ids))
	queried := make([]uint, 0, len(ids))
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			authors[0] = domainvideo.Author{ID: 0, Username: deletedUsername}
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		queried = append(queried, id)
	}
	if len(queried) == 0 {
		return authors, nil
	}

	accounts, err := r.accounts.GetByIDs(ctx, queried)
	if err != nil {
		return nil, err
	}
	byID := make(map[uint]domainaccount.PublicAccount, len(accounts))
	for _, account := range accounts {
		byID[account.ID] = account
	}
	for _, id := range queried {
		if account, ok := byID[id]; ok {
			authors[id] = domainvideo.Author{ID: account.ID, Username: account.Username, AvatarURL: account.AvatarURL}
			continue
		}
		authors[id] = domainvideo.Author{ID: id, Username: deletedUsername}
	}
	return authors, nil
}
