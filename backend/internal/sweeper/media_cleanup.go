package sweeper

import (
	"context"

	infravideo "gofeed/internal/infra/persistence/video"
)

// removeMedia 删除一组公开媒体，MediaRemover 的不存在即成功语义使其可安全重试
func removeMedia(ctx context.Context, remover infravideo.MediaRemover, urls ...string) error {
	for _, publicURL := range urls {
		if publicURL == "" {
			continue
		}
		if err := remover.Remove(ctx, publicURL); err != nil {
			return err
		}
	}
	return nil
}
