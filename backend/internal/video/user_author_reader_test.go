package video

import (
	"context"
	"errors"
	"testing"

	"gofeed/internal/user"

	"gorm.io/gorm"
)

// 测试目标：模拟视频模块查询作者资料所需的用户读取能力
// 预期效果：返回预设账户或错误，并记录单条与批量查询参数
type fakeUserReader struct {
	account     *user.User
	err         error
	ids         []uint
	batchErr    error
	batchIDs    [][]uint
	batchResult []*user.User
}

// 测试目标：实现 UserReader 并记录作者资料查询参数
// 预期效果：供作者读取器单元测试验证查询行为
func (r *fakeUserReader) GetByID(_ context.Context, id uint) (*user.User, error) {
	r.ids = append(r.ids, id)
	return r.account, r.err
}

// 测试目标：实现批量用户读取并记录每批查询的作者标识
// 预期效果：返回预设用户切片或错误，供批量适配测试使用
func (r *fakeUserReader) GetByIDs(_ context.Context, ids []uint) ([]*user.User, error) {
	r.batchIDs = append(r.batchIDs, ids)
	if r.batchErr != nil {
		return nil, r.batchErr
	}
	return r.batchResult, nil
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

// 测试目标：验证批量作者读取一次查询即可映射活跃作者资料
// 预期效果：零值被过滤、重复标识按首次出现去重，且不触发单条读取
func TestUserAuthorReaderGetPublicAuthorsMapsActiveAuthors(t *testing.T) {
	reader := &fakeUserReader{batchResult: []*user.User{
		{ID: 3, Username: "third", AvatarURL: "https://example.test/3.png"},
		{ID: 2, Username: "second", AvatarURL: "https://example.test/2.png"},
	}}

	authors, err := NewUserAuthorReader(reader).GetPublicAuthors(context.Background(), []uint{3, 2, 3, 0, 2})
	if err != nil {
		t.Fatalf("GetPublicAuthors: %v", err)
	}
	want := map[uint]Author{
		2: {ID: 2, Username: "second", AvatarURL: "https://example.test/2.png"},
		3: {ID: 3, Username: "third", AvatarURL: "https://example.test/3.png"},
		0: {ID: 0, Username: deletedUsername},
	}
	if len(authors) != len(want) {
		t.Fatalf("作者数量错误 got=%d want=%d authors=%+v", len(authors), len(want), authors)
	}
	for id, author := range want {
		if authors[id] != author {
			t.Fatalf("作者 %d 资料错误 got=%+v want=%+v", id, authors[id], author)
		}
	}
	if len(reader.batchIDs) != 1 || len(reader.batchIDs[0]) != 2 || reader.batchIDs[0][0] != 3 || reader.batchIDs[0][1] != 2 {
		t.Fatalf("批量查询参数错误 got=%v want 一次查询 [[3 2]]", reader.batchIDs)
	}
	if len(reader.ids) != 0 {
		t.Fatalf("批量路径不应触发单条读取 got=%v", reader.ids)
	}
}

// 测试目标：验证批量结果中缺失或已注销的作者补占位资料
// 预期效果：仓储未返回的每个标识均得到稳定的已注销作者占位
func TestUserAuthorReaderGetPublicAuthorsFillsPlaceholder(t *testing.T) {
	reader := &fakeUserReader{batchResult: []*user.User{
		{ID: 5, Username: "survivor"},
	}}

	authors, err := NewUserAuthorReader(reader).GetPublicAuthors(context.Background(), []uint{5, 6, 7})
	if err != nil {
		t.Fatalf("GetPublicAuthors: %v", err)
	}
	if authors[5] != (Author{ID: 5, Username: "survivor"}) {
		t.Fatalf("活跃作者资料错误 got=%+v", authors[5])
	}
	if authors[6] != (Author{ID: 6, Username: deletedUsername}) || authors[7] != (Author{ID: 7, Username: deletedUsername}) {
		t.Fatalf("缺失作者应补占位资料 got=%+v", authors)
	}
}

// 测试目标：验证空输入和仅含零值的输入不会访问数据库
// 预期效果：空输入返回空 map，零值输入只返回零值占位且批量查询为零次
func TestUserAuthorReaderGetPublicAuthorsSkipsQueryWithoutValidIDs(t *testing.T) {
	reader := &fakeUserReader{}
	app := NewUserAuthorReader(reader)

	authors, err := app.GetPublicAuthors(context.Background(), nil)
	if err != nil || len(authors) != 0 {
		t.Fatalf("空输入应返回空 map got=%+v error=%v", authors, err)
	}
	authors, err = app.GetPublicAuthors(context.Background(), []uint{0, 0})
	if err != nil || len(authors) != 1 || authors[0] != (Author{ID: 0, Username: deletedUsername}) {
		t.Fatalf("零值输入应只返回零值占位 got=%+v error=%v", authors, err)
	}
	if len(reader.batchIDs) != 0 {
		t.Fatalf("无效输入不应触发批量查询 got=%v", reader.batchIDs)
	}
}

// 测试目标：验证批量作者读取错误不会被占位资料掩盖
// 预期效果：批量查询失败时原样透传错误且不返回部分结果
func TestUserAuthorReaderGetPublicAuthorsPropagatesError(t *testing.T) {
	want := errors.New("user database unavailable")
	reader := &fakeUserReader{batchErr: want}

	_, err := NewUserAuthorReader(reader).GetPublicAuthors(context.Background(), []uint{5, 6})
	if !errors.Is(err, want) {
		t.Fatalf("批量读取错误应透传 got=%v", err)
	}
}
