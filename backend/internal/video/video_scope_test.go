package video

import (
	"testing"
	"time"

	"gofeed/internal/testutil"
)

// 测试目标：验证公开视频查询入口自行绑定 Video 模型并应用公开边界
// 预期效果：调用方无需额外 Model，只有完整未软删除的已发布记录可查询
func TestPublicVideoQueryBindsVideoModel(t *testing.T) {
	db := testutil.DB(t)
	base := time.Date(2026, 8, 29, 12, 0, 0, 0, time.Local)
	valid := newVideoFixture(1, "valid", VideoStatusPublished, base)
	draft := newVideoFixture(1, "draft", VideoStatusDraft, base.Add(time.Minute))
	deleted := newVideoFixture(1, "deleted", VideoStatusPublished, base.Add(2*time.Minute))
	for _, item := range []*Video{valid, draft, deleted} {
		if err := db.Create(item).Error; err != nil {
			t.Fatalf("创建测试视频 %q 失败: %v", item.Title, err)
		}
	}
	if err := db.Delete(&Video{}, deleted.ID).Error; err != nil {
		t.Fatalf("软删除测试视频失败: %v", err)
	}

	var rows []Video
	if err := PublicVideoQuery(db).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("公开视频查询入口执行失败: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != valid.ID {
		t.Fatalf("公开视频查询边界错误 got=%+v want=%d", rows, valid.ID)
	}
}
