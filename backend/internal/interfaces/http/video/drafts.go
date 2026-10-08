package interfaceshttpvideo

import (
	"net/http"
	"time"

	applicationvideo "gofeed/internal/application/video"
	domainvideo "gofeed/internal/domain/video"
	apierror "gofeed/internal/error"
	interfaceshttpauth "gofeed/internal/interfaces/http/auth"

	"github.com/gin-gonic/gin"
)

type DraftHandler struct {
	service *applicationvideo.DraftService
}

func NewDrafts(service *applicationvideo.DraftService) *DraftHandler {
	return &DraftHandler{service: service}
}

type DraftRequest struct {
	Title       string `json:"title" binding:"required,max=255"`
	Description string `json:"description" binding:"omitempty,max=1000"`
}

type DraftItem struct {
	ID                uint      `json:"id"`
	Title             string    `json:"title"`
	Description       string    `json:"description"`
	Status            string    `json:"status"`
	HasVideo          bool      `json:"has_video"`
	HasCover          bool      `json:"has_cover"`
	PlayOriginalName  string    `json:"play_original_name,omitempty"`
	CoverOriginalName string    `json:"cover_original_name,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (h *DraftHandler) CreateDraft(c *gin.Context) {
	userID, ok := interfaceshttpauth.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}

	var req DraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid draft payload")
		return
	}
	draft, err := h.service.CreateDraft(c.Request.Context(), userID, domainvideo.DraftInput{Title: req.Title, Description: req.Description})
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"draft": draftResponse(draft)})
}

func (h *DraftHandler) GetDraft(c *gin.Context) {
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

	draft, err := h.service.GetDraft(c.Request.Context(), draftID, userID)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"draft": draftResponse(draft)})
}

func draftResponse(draft domainvideo.DraftItem) DraftItem {
	return DraftItem{
		ID:                draft.ID,
		Title:             draft.Title,
		Description:       draft.Description,
		Status:            draft.Status,
		HasVideo:          draft.HasVideo,
		HasCover:          draft.HasCover,
		PlayOriginalName:  draft.PlayOriginalName,
		CoverOriginalName: draft.CoverOriginalName,
		CreatedAt:         draft.CreatedAt,
		UpdatedAt:         draft.UpdatedAt,
	}
}
