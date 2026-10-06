package domaininteraction

import "context"

type Reader interface {
	RequireActiveUser(ctx context.Context, userID uint) error
	RequirePublicVideo(ctx context.Context, videoID uint) error
	GetLikeCount(ctx context.Context, videoID uint) (int64, error)
	GetLikeState(ctx context.Context, videoID, userID uint) (bool, error)
	GetCommentList(ctx context.Context, videoID uint, position *CommentPosition, limit int) ([]CommentWithAuthor, error)
	GetAuthor(ctx context.Context, userID uint) (Author, error)
}

// MutationWriter 保证真实业务变更与启用的互动事件在同一事务内持久化
type MutationWriter interface {
	CreateLike(ctx context.Context, videoID, userID uint) (bool, error)
	RemoveLike(ctx context.Context, videoID, userID uint) (bool, error)
	CreateComment(ctx context.Context, videoID, authorID uint, content string) (Comment, error)
	DeleteComment(ctx context.Context, videoID, commentID, authorID uint) (bool, error)
}
