package video

import "context"

// DraftDiscarder 原子将可丢弃视频转入清扫状态
type DraftDiscarder interface {
	UpdateDraftDiscard(ctx context.Context, draftID, authorID uint) (*DraftSnapshot, error)
}
