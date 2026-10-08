package interfaceshttpfeed

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	applicationfeed "gofeed/internal/application/feed"
	domainfeed "gofeed/internal/domain/feed"
	apierror "gofeed/internal/error"
	interfaceshttpauth "gofeed/internal/interfaces/http/auth"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	service         *applicationfeed.Service
	followingAuth   gin.HandlerFunc
	requestObserver RequestObserver
}

type Option func(*Handler)

func WithFollowingAuth(auth gin.HandlerFunc) Option {
	return func(h *Handler) { h.followingAuth = auth }
}

// RequestObservation 携带一次 Feed 请求的原始观测事实
// 场景与状态保留原始输入，指标标签的归一化由装配方负责
type RequestObservation struct {
	Scene         string
	CursorPresent bool
	Status        int
	Items         int
	Duration      time.Duration
}

// RequestObserver 在请求结束后收到一次观测，不得改变请求本身的行为
type RequestObserver func(RequestObservation)

func WithRequestObserver(observer RequestObserver) Option {
	return func(h *Handler) {
		if observer != nil {
			h.requestObserver = observer
		}
	}
}

func New(service *applicationfeed.Service, options ...Option) *Handler {
	handler := &Handler{service: service}
	for _, option := range options {
		option(handler)
	}
	return handler
}

// GetFeed 按场景处理公开 Timeline 与认证 Following
func (h *Handler) GetFeed(c *gin.Context) {
	started := time.Now()
	var observedItems int
	if h.requestObserver != nil {
		defer func() {
			status := c.Writer.Status()
			if recovered := recover(); recovered != nil {
				status = http.StatusInternalServerError
				defer panic(recovered)
			}
			h.requestObserver(RequestObservation{
				Scene:         c.Query("scene"),
				CursorPresent: c.Query("cursor") != "",
				Status:        status,
				Items:         observedItems,
				Duration:      time.Since(started),
			})
		}()
	}
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	for _, scene := range values["scene"] {
		if scene == string(domainfeed.SceneFollowing) {
			privateFollowingResponse(c)
			break
		}
	}
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
	scene := domainfeed.Scene(values.Get("scene"))
	switch scene {
	case "", domainfeed.SceneTimeline, domainfeed.SceneFollowing, domainfeed.SceneHot, domainfeed.SceneRecommend:
	default:
		apierror.WriteCode(c, apierror.CodeInvalid, domainfeed.ErrInvalidScene.Error())
		return
	}
	var viewerID uint
	if scene == domainfeed.SceneFollowing {
		if h.followingAuth == nil {
			apierror.WriteUnauthorized(c, domainfeed.ErrUnauthenticated.Error())
			return
		}
		h.followingAuth(c)
		if c.IsAborted() {
			return
		}
		var ok bool
		viewerID, ok = interfaceshttpauth.UserID(c)
		if !ok || viewerID == 0 {
			apierror.WriteUnauthorized(c, domainfeed.ErrUnauthenticated.Error())
			return
		}
	}
	result, err := h.service.GetFeed(c.Request.Context(), applicationfeed.FeedRequest{
		ViewerID: viewerID,
		Scene:    scene,
		Cursor:   values.Get("cursor"),
		Limit:    limit,
	})
	if err != nil {
		if errors.Is(err, domainfeed.ErrSceneNotEnabled) {
			c.Header("Cache-Control", "no-store")
			c.JSON(http.StatusNotImplemented, gin.H{"error": domainfeed.ErrSceneNotEnabled.Error()})
			return
		}
		apierror.Write(c, err, "feed operation failed",
			apierror.Rule{Match: apierror.Is(domainfeed.ErrUnauthenticated), Code: apierror.CodeUnauthorized, PublicMessage: domainfeed.ErrUnauthenticated.Error()},
			apierror.Rule{Match: apierror.Is(domainfeed.ErrInvalidScene, domainfeed.ErrInvalidLimit, domainfeed.ErrInvalidCursor), Code: apierror.CodeInvalid, UseErrorText: true},
			apierror.Rule{Match: apierror.Is(domainfeed.ErrUnavailable), Code: apierror.CodeUnavailable, PublicMessage: domainfeed.ErrUnavailable.Error()},
		)
		return
	}
	observedItems = len(result.Items)
	c.JSON(http.StatusOK, feedItemsResponseFromResult(result))
}

// privateFollowingResponse 防止观看者专属的关注流响应被共享缓存复用
func privateFollowingResponse(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	values := c.Writer.Header().Values("Vary")
	for _, value := range values {
		for _, field := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(field), "Authorization") {
				return
			}
		}
	}
	values = append(values, "Authorization")
	c.Header("Vary", strings.Join(values, ", "))
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
