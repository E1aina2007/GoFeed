package router

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	videoModel "gofeed/internal/video"
)

// 测试目标：构造绑定完整媒体的待发布草稿
// 预期效果：发布语义用例不重复展开上传细节
func prepareCompleteDraft(t *testing.T, client *http.Client, base, token, title string) draftItem {
	t.Helper()
	draft := createDraft(t, client, base, token, title, "", http.StatusCreated)
	uploadMedia(t, client, base, token, fmt.Sprintf("/api/video/auth/drafts/%d/play", draft.ID), "file", "feed.mp4", mp4Bytes, http.StatusCreated)
	uploadMedia(t, client, base, token, fmt.Sprintf("/api/video/auth/drafts/%d/cover", draft.ID), "file", "feed.png", pngBytes, http.StatusCreated)
	return draft
}

// 测试目标：验证发布事务将草稿原子转入 processing 并写入待派发 outbox 事件
// 预期效果：响应返回 processing 草稿形体，数据库状态与事件字段满足 relay 派发契约
func TestPublishEntersProcessingWithOutboxEvent(t *testing.T) {
	srv, client, _, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "outbox_author", "outbox-password-123")
	sess := login(t, client, base, "outbox_author", "outbox-password-123")

	draft := prepareCompleteDraft(t, client, base, sess.AccessToken, "outbox 视频")
	item := publishDraft(t, gdb, client, base, sess.AccessToken, draft.ID, http.StatusAccepted)
	if item.ID == 0 || item.Status != videoModel.VideoStatusProcessing {
		t.Fatalf("发布响应应为处理中草稿 got=%+v", item)
	}

	var row videoModel.Video
	if err := gdb.First(&row, item.ID).Error; err != nil {
		t.Fatalf("读取发布行失败: %v", err)
	}
	if row.Status != videoModel.VideoStatusProcessing || row.PublishedAt == nil || row.RejectedReason != "" {
		t.Fatalf("发布行应处于 processing 且带发布时刻 got=%+v", row)
	}

	var events []videoModel.OutboxEvent
	if err := gdb.Where("video_id = ?", item.ID).Find(&events).Error; err != nil {
		t.Fatalf("读取 outbox 事件失败: %v", err)
	}
	if len(events) != 1 || events[0].EventType != videoModel.VideoProcessEventType ||
		events[0].Status != videoModel.OutboxEventStatusPending || events[0].EventID == "" ||
		events[0].Attempt != 0 || events[0].DispatchedAt != nil {
		t.Fatalf("outbox 事件字段错误 got=%+v", events)
	}

	var status videoModel.VideoProcessingStatus
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/auth/%d/status", base, item.ID), sess.AccessToken, nil, http.StatusOK, &status)
	if status.Status != videoModel.VideoStatusProcessing || status.PublishedAt == nil || status.RejectedAt != nil || status.RejectedReason != "" {
		t.Fatalf("处理中状态响应错误 got=%+v", status)
	}
}

// 测试目标：验证 processing 视频对外不可见，模拟处理完成后恢复公开可见
// 预期效果：公开列表、详情与我的视频在处理期间不返回该视频
func TestProcessingVideoInvisibleUntilCompleted(t *testing.T) {
	srv, client, _, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "processing_author", "processing-password-123")
	sess := login(t, client, base, "processing_author", "processing-password-123")

	draft := prepareCompleteDraft(t, client, base, sess.AccessToken, "处理中视频")
	item := publishDraft(t, gdb, client, base, sess.AccessToken, draft.ID, http.StatusAccepted)

	var list struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusOK, &list)
	if len(list.Items) != 0 {
		t.Fatalf("processing 视频不应进入公开列表 got=%+v", list.Items)
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, item.ID), "", nil, http.StatusNotFound, nil)
	var mine struct {
		Items []videoItem `json:"items"`
	}
	doJSON(t, client, http.MethodGet, base+"/api/video/auth/mine", sess.AccessToken, nil, http.StatusOK, &mine)
	if len(mine.Items) != 0 {
		t.Fatalf("processing 视频不应进入我的视频 got=%+v", mine.Items)
	}

	// 模拟 worker 校验通过后的 CAS 状态流转
	completeProcessing(t, gdb, item.ID)
	var detail struct {
		Video videoItem `json:"video"`
	}
	doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/video/%d", base, item.ID), "", nil, http.StatusOK, &detail)
	if detail.Video.ID != item.ID {
		t.Fatalf("处理完成后详情应可见 got=%+v", detail)
	}
	doJSON(t, client, http.MethodGet, base+"/api/video", "", nil, http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].ID != item.ID {
		t.Fatalf("处理完成后列表应可见 got=%+v", list.Items)
	}
}

// 测试目标：验证 outbox 写入失败时发布事务整体回滚
// 预期效果：视频保持 draft 可重试发布，注入解除后发布成功
func TestPublishRollsBackWhenOutboxFails(t *testing.T) {
	srv, client, faults, gdb := newResilienceTestServer(t)
	base := srv.URL
	register(t, client, base, "rollback_author", "rollback-password-123")
	sess := login(t, client, base, "rollback_author", "rollback-password-123")
	draft := prepareCompleteDraft(t, client, base, sess.AccessToken, "回滚视频")

	faults.arm("video_outbox_events", errors.New("injected outbox outage"))
	var errBody map[string]any
	doJSON(t, client, http.MethodPost, fmt.Sprintf("%s/api/video/auth/drafts/%d/publish", base, draft.ID), sess.AccessToken, nil, http.StatusInternalServerError, &errBody)
	faults.disarm()

	var row videoModel.Video
	if err := gdb.First(&row, draft.ID).Error; err != nil {
		t.Fatalf("读取回滚行失败: %v", err)
	}
	if row.Status != videoModel.VideoStatusDraft || row.PublishedAt != nil {
		t.Fatalf("outbox 失败应回滚为 draft got=%+v", row)
	}
	var events []videoModel.OutboxEvent
	if err := gdb.Where("video_id = ?", draft.ID).Find(&events).Error; err != nil {
		t.Fatalf("读取 outbox 事件失败: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("回滚后不应残留事件 got=%+v", events)
	}

	item := publishDraft(t, gdb, client, base, sess.AccessToken, draft.ID, http.StatusAccepted)
	if item.ID == 0 {
		t.Fatalf("解除注入后重试发布应成功 got=%+v", item)
	}
}
