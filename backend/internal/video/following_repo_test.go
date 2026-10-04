package video

import (
	"context"
	"errors"
	"testing"

	"gofeed/internal/testutil"
)

// 测试目标：关注仓储拒绝零观看者与非法探测数量
// 预期效果：非法参数在访问数据库前被拒绝，不接受无限数量或超过五十一条的读取
func TestFollowingVideoListRejectsInvalidParameters(t *testing.T) {
	repo := NewRepository(nil)
	if _, err := repo.GetFollowingVideoList(t.Context(), 0, nil, 1); !errors.Is(err, ErrInvalidVideoID) {
		t.Fatalf("观看者 err=%v", err)
	}
	for _, limit := range []int{-1, 0, MaxPublishedVideoBatchSize + 1} {
		if _, err := repo.GetFollowingVideoList(t.Context(), 42, nil, limit); !errors.Is(err, ErrInvalidLimit) {
			t.Fatalf("数量=%d err=%v", limit, err)
		}
	}
}

// 测试目标：关注视频查询传递调用方取消上下文
// 预期效果：真实 MySQL 仓储返回取消错误而不把中止读取当成空页
func TestFollowingVideoListCancelledContext(t *testing.T) {
	repo := NewRepository(testutil.DB(t))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := repo.GetFollowingVideoList(ctx, 42, nil, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消 err=%v", err)
	}
}
