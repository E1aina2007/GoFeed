package video

import "context"

type DeletionSnapshot struct {
	AuthorID uint
	Status   string
}

// PublishedVideoDeleter 提供删除判定快照与已发布视频的条件软删除
type PublishedVideoDeleter interface {
	GetDeletionSnapshot(ctx context.Context, id uint) (*DeletionSnapshot, error)
	DeletePublishedVideo(ctx context.Context, id, authorID uint) error
}
