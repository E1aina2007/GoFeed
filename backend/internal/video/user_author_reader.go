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
	GetByIDs(ctx context.Context, ids []uint) ([]*user.User, error)
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

// GetPublicAuthors 对一批作者标识执行一次批量读取
// 输入先过滤零值并按首次出现顺序去重；未返回或已注销的作者补占位资料，
// 批量读取错误原样返回，不使用占位资料掩盖故障
func (r *UserAuthorReader) GetPublicAuthors(ctx context.Context, ids []uint) (map[uint]Author, error) {
	authors := make(map[uint]Author, len(ids))
	queried := make([]uint, 0, len(ids))
	seen := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			authors[0] = Author{ID: 0, Username: deletedUsername}
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

	accounts, err := r.users.GetByIDs(ctx, queried)
	if err != nil {
		return nil, err
	}
	byID := make(map[uint]*user.User, len(accounts))
	for _, account := range accounts {
		if account != nil {
			byID[account.ID] = account
		}
	}
	for _, id := range queried {
		if account, ok := byID[id]; ok {
			authors[id] = Author{
				ID:        account.ID,
				Username:  account.Username,
				AvatarURL: account.AvatarURL,
			}
			continue
		}
		authors[id] = Author{ID: id, Username: deletedUsername}
	}
	return authors, nil
}
