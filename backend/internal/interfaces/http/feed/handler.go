package interfaceshttpfeed

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	applicationfeed "gofeed/internal/application/feed"
	domainfeed "gofeed/internal/domain/feed"
	apierror "gofeed/internal/error"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	service *applicationfeed.Service
}

func New(service *applicationfeed.Service) *Handler {
	return &Handler{service: service}
}

// GetFeed 处理 GET /api/feed；F0 只启用匿名 Timeline
func (h *Handler) GetFeed(c *gin.Context) {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil || !validQuery(values) {
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid feed query")
		return
	}
	limit := 0
	if raw, present := values["limit"]; present {
		limit, err = strconv.Atoi(raw[0])
		if err != nil || limit < 1 || limit > domainfeed.MaxLimit {
			apierror.WriteCode(c, apierror.CodeInvalid, "invalid limit")
			return
		}
	}
	result, err := h.service.GetFeed(c.Request.Context(), applicationfeed.FeedRequest{
		Scene:  domainfeed.Scene(values.Get("scene")),
		Cursor: values.Get("cursor"),
		Limit:  limit,
	})
	if err != nil {
		if errors.Is(err, domainfeed.ErrSceneNotEnabled) {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusNotImplemented, gin.H{"error": domainfeed.ErrSceneNotEnabled.Error()})
			return
		}
		apierror.Write(c, err, "feed operation failed",
			apierror.Rule{Match: apierror.Is(domainfeed.ErrInvalidScene, domainfeed.ErrInvalidLimit, domainfeed.ErrInvalidCursor), Code: apierror.CodeInvalid, UseErrorText: true},
			apierror.Rule{Match: apierror.Is(domainfeed.ErrUnavailable), Code: apierror.CodeUnavailable, PublicMessage: domainfeed.ErrUnavailable.Error()},
		)
		return
	}
	c.JSON(http.StatusOK, feedItemsResponseFromResult(result))
}

// 参数只允许 F0 已冻结的单值查询，避免把 author_id 等过滤条件静默忽略
func validQuery(values url.Values) bool {
	for key, value := range values {
		if len(value) != 1 {
			return false
		}
		switch key {
		case "scene", "cursor", "limit":
		default:
			return false
		}
	}
	return true
}
