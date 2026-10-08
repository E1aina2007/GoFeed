package video

import "context"

// DraftPublisher 原子提交草稿处理状态与处理事件
type DraftPublisher interface {
	UpdateDraftPublication(ctx context.Context, draftID, authorID uint) (*DraftSnapshot, error)
}
