package video

import (
	"context"
	"errors"

	"gofeed/internal/user"

	"gorm.io/gorm"
)

const deletedUsername = "已注销用户"

// UserReader 是查询视频作者所需的最小用户读取能力
type UserReader interface {
	GetByID(ctx context.Context, id uint) (*user.User, error)
}

// UserAuthorReader 将公开用户资料适配为视频模块的 AuthorReader
type UserAuthorReader struct {
	users UserReader
}

func NewUserAuthorReader(users UserReader) *UserAuthorReader {
	return &UserAuthorReader{users: users}
}

// GetPublicAuthor 在用户已注销或不存在时返回占位作者，
// 从而保证已发布视频仍可读取
func (r *UserAuthorReader) GetPublicAuthor(ctx context.Context, id uint) (Author, error) {
	account, err := r.users.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Author{ID: id, Username: deletedUsername}, nil
		}
		return Author{}, err
	}
	return Author{
		ID:        account.ID,
		Username:  account.Username,
		AvatarURL: account.AvatarURL,
	}, nil
}
