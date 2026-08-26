package user

import (
	"context"
	"errors"
	"testing"

	"gofeed/internal/testutil"

	"gorm.io/gorm"
)

type fakePublishedVideoCounter struct {
	count     int64
	err       error
	authorIDs []uint
}

func (f *fakePublishedVideoCounter) GetPublishedVideoCountByAuthor(_ context.Context, authorID uint) (int64, error) {
	f.authorIDs = append(f.authorIDs, authorID)
	return f.count, f.err
}

// 测试目标：验证用户资料聚合当前公开可见的视频数量。
// 预期效果：服务返回账户与计数，并以目标用户 ID 查询视频统计。
func TestServiceGetProfile(t *testing.T) {
	db := testutil.DB(t)
	accountID := seedUser(t, db, "profile-owner")
	counter := &fakePublishedVideoCounter{count: 3}
	service := NewService(NewRepository(db), counter)

	profile, err := service.GetProfile(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if profile.Account.ID != accountID || profile.Account.Username != "profile-owner" || profile.VideoCount != 3 {
		t.Fatalf("资料聚合结果不正确, got=%+v", profile)
	}
	if len(counter.authorIDs) != 1 || counter.authorIDs[0] != accountID {
		t.Fatalf("视频统计应按账户查询, got=%v", counter.authorIDs)
	}
}

// 测试目标：验证视频统计失败不会伪造零值资料响应。
// 预期效果：服务原样返回计数错误。
func TestServiceGetProfilePropagatesVideoCounterError(t *testing.T) {
	db := testutil.DB(t)
	accountID := seedUser(t, db, "profile-counter-error")
	want := errors.New("video database unavailable")
	service := NewService(NewRepository(db), &fakePublishedVideoCounter{err: want})

	if _, err := service.GetProfile(context.Background(), accountID); !errors.Is(err, want) {
		t.Fatalf("视频统计错误应透传, got=%v", err)
	}
}

// 测试目标：验证不存在或已删除的用户不会触发视频统计。
// 预期效果：先返回用户不存在，再避免不必要的跨模块读取。
func TestServiceGetProfileSkipsCounterForMissingUser(t *testing.T) {
	db := testutil.DB(t)
	counter := &fakePublishedVideoCounter{}
	service := NewService(NewRepository(db), counter)

	if _, err := service.GetProfile(context.Background(), 999); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("不存在用户应返回 ErrRecordNotFound, got=%v", err)
	}
	if len(counter.authorIDs) != 0 {
		t.Fatalf("不存在用户不应查询视频统计, got=%v", counter.authorIDs)
	}
}

// 测试目标：验证资料读取在依赖缺失时返回明确错误而非发生空指针异常。
// 预期效果：装配错误以内部错误形式上浮给 HTTP 层。
func TestServiceGetProfileRequiresVideoCounter(t *testing.T) {
	db := testutil.DB(t)
	accountID := seedUser(t, db, "profile-no-counter")
	service := NewService(NewRepository(db), nil)

	if _, err := service.GetProfile(context.Background(), accountID); !errors.Is(err, ErrVideoCounterUnavailable) {
		t.Fatalf("缺失视频统计依赖错误不正确, got=%v", err)
	}
}
