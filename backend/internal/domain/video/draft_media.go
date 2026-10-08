package video

import "context"

type DraftMediaUpload struct {
	SavedFile    SavedFile
	OriginalName string
}

type DraftMediaBinder interface {
	UpdateDraftMedia(ctx context.Context, draftID, authorID uint, kind MediaKind, saved SavedFile, originalName string) error
}

func IsValidDraftMedia(draftID, ownerID uint, kind MediaKind, publicURL, fileName string) bool {
	if draftID == 0 || ownerID == 0 || (kind != MediaVideo && kind != MediaCover) ||
		!IsOwnedMediaURL(publicURL, kind, ownerID) || !IsValidStoredFile(publicURL, fileName) {
		return false
	}
	return true
}
