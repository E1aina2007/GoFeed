package social

import (
	"net/http"
	"strconv"

	"gofeed/internal/error"
	"gofeed/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Controller struct {
	service *Service
}

func NewController(service *Service) *Controller {
	return &Controller{service: service}
}

// GetFollowerList 处理 GET /api/user/:id/followers?cursor=&limit=
func (ctl *Controller) GetFollowerList(c *gin.Context) {
	userID, err := parsePathID(c.Param("id"), ErrInvalidUserID)
	if err != nil {
		handleError(c, err)
		return
	}
	limit, err := parseLimit(c.Query("limit"))
	if err != nil {
		handleError(c, err)
		return
	}
	response, err := ctl.service.GetFollowerList(c.Request.Context(), userID, c.Query("cursor"), limit)
	if err != nil {
		handleError(c, err)
		return
	}
	if response.Items == nil {
		response.Items = []FollowListItem{}
	}
	c.JSON(http.StatusOK, response)
}

// GetFollowingList 处理 GET /api/user/:id/following?cursor=&limit=
func (ctl *Controller) GetFollowingList(c *gin.Context) {
	userID, err := parsePathID(c.Param("id"), ErrInvalidUserID)
	if err != nil {
		handleError(c, err)
		return
	}
	limit, err := parseLimit(c.Query("limit"))
	if err != nil {
		handleError(c, err)
		return
	}
	response, err := ctl.service.GetFollowingList(c.Request.Context(), userID, c.Query("cursor"), limit)
	if err != nil {
		handleError(c, err)
		return
	}
	if response.Items == nil {
		response.Items = []FollowListItem{}
	}
	c.JSON(http.StatusOK, response)
}

// GetFollowState 处理 GET /api/user/auth/:id/follow
func (ctl *Controller) GetFollowState(c *gin.Context) {
	followerID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	followeeID, err := parsePathID(c.Param("id"), ErrInvalidUserID)
	if err != nil {
		handleError(c, err)
		return
	}
	state, err := ctl.service.GetFollowState(c.Request.Context(), followerID, followeeID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

// CreateFollow 处理 PUT /api/user/auth/:id/follow
func (ctl *Controller) CreateFollow(c *gin.Context) {
	followerID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	followeeID, err := parsePathID(c.Param("id"), ErrInvalidUserID)
	if err != nil {
		handleError(c, err)
		return
	}
	state, err := ctl.service.CreateFollow(c.Request.Context(), followerID, followeeID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

// RemoveFollow 处理 DELETE /api/user/auth/:id/follow
func (ctl *Controller) RemoveFollow(c *gin.Context) {
	followerID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	followeeID, err := parsePathID(c.Param("id"), ErrInvalidUserID)
	if err != nil {
		handleError(c, err)
		return
	}
	state, err := ctl.service.RemoveFollow(c.Request.Context(), followerID, followeeID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

func parsePathID(raw string, invalid error) (uint, error) {
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, invalid
	}
	return uint(id), nil
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, ErrInvalidLimit
	}
	return limit, nil
}

// socialErrorRules 按从最具体到最通用排列，决定关注模块领域错误的公共类别与对外文案
var socialErrorRules = []apierror.Rule{
	{Match: apierror.Is(ErrInvalidUserID, ErrInvalidLimit, ErrInvalidCursor, ErrSelfFollow), Code: apierror.CodeInvalid, UseErrorText: true},
	{Match: apierror.Is(ErrUserNotFound, gorm.ErrRecordNotFound), Code: apierror.CodeNotFound, UseErrorText: true},
}

func handleError(c *gin.Context, err error) {
	apierror.Write(c, err, "social operation failed", socialErrorRules...)
}
