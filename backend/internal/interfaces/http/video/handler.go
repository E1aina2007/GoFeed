package interfaceshttpvideo

import (
	"net/http"
	"strconv"

	applicationvideo "gofeed/internal/application/video"
	domainvideo "gofeed/internal/domain/video"
	apierror "gofeed/internal/error"
	interfaceshttpauth "gofeed/internal/interfaces/http/auth"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	service *applicationvideo.Service
}

func New(service *applicationvideo.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) GetVideo(c *gin.Context) {
	id, err := parsePathID(c.Param("id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}

	item, err := h.service.GetPublished(c.Request.Context(), id)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"video": videoResponse(item)})
}

func (h *Handler) GetVideoList(c *gin.Context) {
	limit, err := parseLimit(c.Query("limit"))
	if err != nil {
		handleVideoError(c, err)
		return
	}
	authorID, err := parseAuthorID(c.Query("author_id"))
	if err != nil {
		handleVideoError(c, err)
		return
	}

	resp, err := h.service.GetPublishedVideoList(c.Request.Context(), authorID, c.Query("cursor"), limit)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusOK, listResponseFrom(resp))
}

func parsePathID(raw string) (uint, error) {
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, domainvideo.ErrInvalidVideoID
	}
	return uint(id), nil
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil // 交给服务层使用默认值
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, domainvideo.ErrInvalidLimit
	}
	return limit, nil
}

func parseAuthorID(raw string) (uint, error) {
	if raw == "" {
		return 0, nil // 0 表示不过滤作者
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, domainvideo.ErrInvalidAuthorID
	}
	return uint(id), nil
}

var videoErrorRules = []apierror.Rule{
	{Match: apierror.Is(domainvideo.ErrInvalidVideoID, domainvideo.ErrInvalidLimit, domainvideo.ErrInvalidCursor, domainvideo.ErrInvalidAuthorID, domainvideo.ErrInvalidInput, domainvideo.ErrInvalidPublishRequest, domainvideo.ErrInvalidMedia, domainvideo.ErrMediaTooLarge), Code: apierror.CodeInvalid, UseErrorText: true},
	{Match: apierror.Is(domainvideo.ErrVideoNotFound), Code: apierror.CodeNotFound, PublicMessage: "video not found"},
	{Match: apierror.Is(domainvideo.ErrForbidden, domainvideo.ErrNotAuthor, domainvideo.ErrInvalidMediaURL), Code: apierror.CodeForbidden, UseErrorText: true},
	{Match: apierror.Is(domainvideo.ErrConflict), Code: apierror.CodeConflict, UseErrorText: true},
	{Match: apierror.Is(domainvideo.ErrEngagementUnavailable), Code: apierror.CodeUnavailable, PublicMessage: "engagement stats temporarily unavailable"},
}

func handleVideoError(c *gin.Context, err error) {
	apierror.Write(c, err, "video operation failed", videoErrorRules...)
}

func (h *Handler) GetMyVideoList(c *gin.Context) {
	userID, ok := interfaceshttpauth.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}

	limit, err := parseLimit(c.Query("limit"))
	if err != nil {
		handleVideoError(c, err)
		return
	}
	resp, err := h.service.GetMyVideoList(c.Request.Context(), userID, c.Query("cursor"), limit)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusOK, listResponseFrom(resp))
}
