package video

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"gofeed/internal/error"
	interfaceshttpauth "gofeed/internal/interfaces/http/auth"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Controller 负责视频模块的 HTTP 接入；写操作依赖 JWT 中的用户 ID
type Controller struct {
	srv *Service
}

func NewController(srv *Service) *Controller {
	return &Controller{srv: srv}
}

// UpdateDraftPublication 处理 POST /api/video/auth/drafts/:id/publish
func (ctl *Controller) UpdateDraftPublication(c *gin.Context) {
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
	item, err := ctl.srv.UpdateDraftPublication(c.Request.Context(), draftID, userID)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	// 发布是异步语义：202 表示处理已受理，结果经状态查询端点获取
	c.JSON(http.StatusAccepted, gin.H{"draft": item})
}

// DiscardDraft 处理 DELETE /api/video/auth/drafts/:id
func (ctl *Controller) DiscardDraft(c *gin.Context) {
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

	draft, err := ctl.srv.DiscardDraft(c.Request.Context(), draftID, userID)
	if err != nil {
		handleVideoError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"draft": draft})
}

// DeleteVideo 处理 DELETE /api/video/auth/:id
func (ctl *Controller) DeleteVideo(c *gin.Context) {
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
	if err := ctl.srv.DeleteVideo(c.Request.Context(), id, userID); err != nil {
		handleVideoError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func parsePathID(raw string) (uint, error) {
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, ErrInvalidVideoID
	}
	return uint(id), nil
}

// videoErrorRules 按从最具体到最通用排列，决定视频模块领域错误的公共类别与对外文案
var videoErrorRules = []apierror.Rule{
	{Match: apierror.Is(ErrInvalidVideoID, ErrInvalidLimit, ErrInvalidCursor, ErrInvalidAuthorID, ErrInvalidPublishRequest, ErrInvalidMedia, ErrMediaTooLarge), Code: apierror.CodeInvalid, UseErrorText: true},
	{Match: apierror.Is(ErrVideoNotFound, gorm.ErrRecordNotFound), Code: apierror.CodeNotFound, PublicMessage: "video not found"},
	{Match: apierror.Is(ErrNotAuthor, ErrInvalidMediaURL), Code: apierror.CodeForbidden, UseErrorText: true},
	{Match: apierror.Is(ErrDraftNotWritable, ErrDraftIncomplete), Code: apierror.CodeConflict, UseErrorText: true},
	// 统计暂不可用属于可重试的服务端临时故障，响应使用固定文案，不回显底层错误细节
	{Match: apierror.Is(ErrEngagementUnavailable), Code: apierror.CodeUnavailable, PublicMessage: "engagement stats temporarily unavailable"},
}

func handleVideoError(c *gin.Context, err error) {
	apierror.Write(c, err, "video operation failed", videoErrorRules...)
}
