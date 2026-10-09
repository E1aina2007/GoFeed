package interfaceshttpvideo

import (
	"net/http"

	applicationvideo "gofeed/internal/application/video"
	apierror "gofeed/internal/error"
	interfaceshttpauth "gofeed/internal/interfaces/http/auth"

	"github.com/gin-gonic/gin"
)

type PublishedDeletionHandler struct {
	service *applicationvideo.PublishedDeletionService
}

func NewPublishedDeletion(service *applicationvideo.PublishedDeletionService) *PublishedDeletionHandler {
	return &PublishedDeletionHandler{service: service}
}

func (h *PublishedDeletionHandler) DeleteVideo(c *gin.Context) {
	userID, ok := interfaceshttpauth.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}

	id, err := parsePathID(c.Param("id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}
	if err := h.service.DeleteVideo(c.Request.Context(), id, userID); err != nil {
		handleVideoError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
