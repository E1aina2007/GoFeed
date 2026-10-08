package interfaceshttpvideo

import (
	"errors"
	"io"
	"net/http"

	applicationvideo "gofeed/internal/application/video"
	apierror "gofeed/internal/error"
	interfaceshttpauth "gofeed/internal/interfaces/http/auth"

	"github.com/gin-gonic/gin"
)

type DraftPublicationHandler struct {
	service *applicationvideo.DraftPublicationService
}

func NewDraftPublication(service *applicationvideo.DraftPublicationService) *DraftPublicationHandler {
	return &DraftPublicationHandler{service: service}
}

func (h *DraftPublicationHandler) UpdateDraftPublication(c *gin.Context) {
	userID, ok := interfaceshttpauth.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	if c.Request.Body != nil {
		var firstByte [1]byte
		n, err := c.Request.Body.Read(firstByte[:])
		if n > 0 || (err != nil && !errors.Is(err, io.EOF)) {
			apierror.WriteCode(c, apierror.CodeInvalid, "publish draft does not accept a request body")
			return
		}
	}
	draftID, err := parsePathID(c.Param("id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}
	item, err := h.service.UpdateDraftPublication(c.Request.Context(), draftID, userID)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"draft": draftResponse(item)})
}
