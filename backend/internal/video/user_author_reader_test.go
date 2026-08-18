package video

import (
	"context"
	"errors"
	"testing"

	"gofeed/internal/user"

	"gorm.io/gorm"
)

// 测试目标：模拟视频模块查询作者资料所需的用户读取能力
// 预期效果：返回预设账户或错误，并记录查询的用户标识
type fakeUserReader struct {
	account *user.User
	err     error
	ids     []uint
}

// 测试目标：实现 UserReader 并记录作者资料查询参数
// 预期效果：供作者读取器单元测试验证查询行为
func (r *fakeUserReader) GetByID(_ context.Context, id uint) (*user.User, error) {
	r.ids = append(r.ids, id)
	return r.account, r.err
}

// 测试目标：验证活跃用户资料会被转换为视频作者资料
// 预期效果：返回用户的标识、用户名与头像，并按目标标识查询一次
func TestUserAuthorReaderGetPublicAuthor(t *testing.T) {
	reader := &fakeUserReader{account: &user.User{
		ID:        7,
		Username:  "video-author",
		AvatarURL: "https://example.test/avatar.png",
	}}

	author, err := NewUserAuthorReader(reader).GetPublicAuthor(context.Background(), 7)
	if err != nil {
		t.Fatalf("GetPublicAuthor: %v", err)
	}
	want := Author{
		ID:        7,
		Username:  "video-author",
		AvatarURL: "https://example.test/avatar.png",
	}
	if author != want {
		t.Fatalf("author mismatch: got=%+v want=%+v", author, want)
	}
	if len(reader.ids) != 1 || reader.ids[0] != 7 {
		t.Fatalf("user lookup IDs = %v, want [7]", reader.ids)
	}
}

// 测试目标：验证已注销或不存在的作者不会导致历史视频读取失败
// 预期效果：用户仓储返回记录不存在时，返回带占位用户名的作者资料
func TestUserAuthorReaderReturnsPlaceholderForMissingAuthor(t *testing.T) {
	reader := &fakeUserReader{err: gorm.ErrRecordNotFound}

	author, err := NewUserAuthorReader(reader).GetPublicAuthor(context.Background(), 12)
	if err != nil {
		t.Fatalf("GetPublicAuthor: %v", err)
	}
	want := Author{ID: 12, Username: deletedUsername}
	if author != want {
		t.Fatalf("placeholder mismatch: got=%+v want=%+v", author, want)
	}
}

// 测试目标：验证非记录不存在的用户查询错误不会被吞掉
// 预期效果：原样向调用方透传底层读取错误
func TestUserAuthorReaderPropagatesLookupError(t *testing.T) {
	want := errors.New("user database unavailable")
	reader := &fakeUserReader{err: want}

	_, err := NewUserAuthorReader(reader).GetPublicAuthor(context.Background(), 12)
	if !errors.Is(err, want) {
		t.Fatalf("lookup error should be propagated, got=%v", err)
	}
}
