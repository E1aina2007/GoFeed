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

type ProcessingStatusHandler struct {
	service *applicationvideo.ProcessingStatusService
}

func NewProcessingStatus(service *applicationvideo.ProcessingStatusService) *ProcessingStatusHandler {
	return &ProcessingStatusHandler{service: service}
}

type VideoProcessingStatus struct {
	Status         string     `json:"status"`
	PublishedAt    *time.Time `json:"published_at"`
	RejectedAt     *time.Time `json:"rejected_at"`
	RejectedReason string     `json:"rejected_reason"`
}

func (h *ProcessingStatusHandler) GetVideoStatus(c *gin.Context) {
	userID, ok := interfaceshttpauth.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	videoID, err := parsePathID(c.Param("id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}
	status, err := h.service.GetVideoStatus(c.Request.Context(), videoID, userID)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	// 状态响应使用顶层固定字段，客户端无需区分额外包装层
	c.JSON(http.StatusOK, processingStatusResponse(status))
}

func processingStatusResponse(status domainvideo.ProcessingStatus) VideoProcessingStatus {
	return VideoProcessingStatus{
		Status:         status.Status,
		PublishedAt:    status.PublishedAt,
		RejectedAt:     status.RejectedAt,
		RejectedReason: status.RejectedReason,
	}
}
