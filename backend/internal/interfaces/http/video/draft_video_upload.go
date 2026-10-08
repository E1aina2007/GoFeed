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

type DraftVideoUploadHandler struct {
	service *applicationvideo.DraftVideoUploadService
}

func NewDraftVideoUpload(service *applicationvideo.DraftVideoUploadService) *DraftVideoUploadHandler {
	return &DraftVideoUploadHandler{service: service}
}

func (h *DraftVideoUploadHandler) UpdateDraftVideo(c *gin.Context) {
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

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, domainvideo.MaxMediaRequestSize(domainvideo.MediaVideo))
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

	if header.Size <= 0 || header.Size > domainvideo.MaxMediaSize(domainvideo.MediaVideo) {
		apierror.WriteCode(c, apierror.CodeTooLarge, domainvideo.ErrMediaTooLarge.Error())
		return
	}

	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		apierror.WriteCode(c, apierror.CodeInternal, "failed to read upload")
		return
	}
	if !domainvideo.ValidateMedia(domainvideo.MediaVideo, header.Filename, head[:n]) {
		apierror.WriteCode(c, apierror.CodeInvalid, domainvideo.ErrInvalidMedia.Error())
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		apierror.WriteCode(c, apierror.CodeInternal, "failed to read upload")
		return
	}

	uploaded, err := h.service.UploadDraftVideo(c.Request.Context(), draftID, userID, header.Filename, file)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"draft_id":           draftID,
		"play_url":           uploaded.SavedFile.PublicURL,
		"play_file_name":     uploaded.SavedFile.FileName,
		"play_original_name": uploaded.OriginalName,
	})
}
