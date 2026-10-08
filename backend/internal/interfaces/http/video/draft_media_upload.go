package interfaceshttpvideo

import (
	"errors"
	"io"
	"net/http"

	applicationvideo "gofeed/internal/application/video"
	domainvideo "gofeed/internal/domain/video"
	apierror "gofeed/internal/error"
	interfaceshttpauth "gofeed/internal/interfaces/http/auth"

	"github.com/gin-gonic/gin"
)

type DraftMediaUploadHandler struct {
	service *applicationvideo.DraftMediaUploadService
}

func NewDraftMediaUpload(service *applicationvideo.DraftMediaUploadService) *DraftMediaUploadHandler {
	return &DraftMediaUploadHandler{service: service}
}

func (h *DraftMediaUploadHandler) UpdateDraftVideo(c *gin.Context) {
	h.uploadDraftMedia(c, domainvideo.MediaVideo, "play_url", "play_file_name", "play_original_name")
}

func (h *DraftMediaUploadHandler) UpdateDraftCover(c *gin.Context) {
	h.uploadDraftMedia(c, domainvideo.MediaCover, "cover_url", "cover_file_name", "cover_original_name")
}

func (h *DraftMediaUploadHandler) uploadDraftMedia(c *gin.Context, kind domainvideo.MediaKind, urlKey, fileNameKey, originalNameKey string) {
	userID, ok := interfaceshttpauth.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	draftID, err := parsePathID(c.Param("id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, domainvideo.MaxMediaRequestSize(kind))
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			apierror.WriteCode(c, apierror.CodeTooLarge, domainvideo.ErrMediaTooLarge.Error())
			return
		}
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid upload payload")
		return
	}
	defer file.Close()

	if header.Size <= 0 || header.Size > domainvideo.MaxMediaSize(kind) {
		apierror.WriteCode(c, apierror.CodeTooLarge, domainvideo.ErrMediaTooLarge.Error())
		return
	}

	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		apierror.WriteCode(c, apierror.CodeInternal, "failed to read upload")
		return
	}
	if !domainvideo.ValidateMedia(kind, header.Filename, head[:n]) {
		apierror.WriteCode(c, apierror.CodeInvalid, domainvideo.ErrInvalidMedia.Error())
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		apierror.WriteCode(c, apierror.CodeInternal, "failed to read upload")
		return
	}

	uploaded, err := h.service.UploadDraftMedia(c.Request.Context(), draftID, userID, kind, header.Filename, file)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"draft_id":      draftID,
		urlKey:          uploaded.SavedFile.PublicURL,
		fileNameKey:     uploaded.SavedFile.FileName,
		originalNameKey: uploaded.OriginalName,
	})
}
