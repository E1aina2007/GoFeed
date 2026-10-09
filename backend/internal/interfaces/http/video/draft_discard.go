package interfaceshttpvideo

import (
	"net/http"

	applicationvideo "gofeed/internal/application/video"
	apierror "gofeed/internal/error"
	interfaceshttpauth "gofeed/internal/interfaces/http/auth"

	"github.com/gin-gonic/gin"
)

type DraftDiscardHandler struct {
	service *applicationvideo.DraftDiscardService
}

func NewDraftDiscard(service *applicationvideo.DraftDiscardService) *DraftDiscardHandler {
	return &DraftDiscardHandler{service: service}
}

func (h *DraftDiscardHandler) DiscardDraft(c *gin.Context) {
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

	draft, err := h.service.DiscardDraft(c.Request.Context(), draftID, userID)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"draft": draftResponse(draft)})
}
