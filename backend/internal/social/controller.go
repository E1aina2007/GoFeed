package social

import (
	"errors"
	"net/http"
	"strconv"

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

// GetCommentList 处理 GET /api/video/:id/comments?cursor=&limit=
func (ctl *Controller) GetCommentList(c *gin.Context) {
	videoID, err := parsePathID(c.Param("id"), ErrInvalidVideoID)
	if err != nil {
		handleError(c, err)
		return
	}
	limit, err := parseLimit(c.Query("limit"))
	if err != nil {
		handleError(c, err)
		return
	}
	response, err := ctl.service.GetCommentList(c.Request.Context(), videoID, c.Query("cursor"), limit)
	if err != nil {
		handleError(c, err)
		return
	}
	if response.Items == nil {
		response.Items = []CommentItem{}
	}
	c.JSON(http.StatusOK, response)
}

// CreateComment 处理 POST /api/video/auth/:id/comments
func (ctl *Controller) CreateComment(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	videoID, err := parsePathID(c.Param("id"), ErrInvalidVideoID)
	if err != nil {
		handleError(c, err)
		return
	}
	var request CreateCommentRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": ErrInvalidCommentContent.Error()})
		return
	}
	comment, err := ctl.service.CreateComment(c.Request.Context(), videoID, userID, request.Content)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"comment": comment})
}

// DeleteComment 处理 DELETE /api/video/auth/:id/comments/:commentID
func (ctl *Controller) DeleteComment(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	videoID, err := parsePathID(c.Param("id"), ErrInvalidVideoID)
	if err != nil {
		handleError(c, err)
		return
	}
	commentID, err := parsePathID(c.Param("commentID"), ErrInvalidCommentID)
	if err != nil {
		handleError(c, err)
		return
	}
	if err := ctl.service.DeleteComment(c.Request.Context(), videoID, commentID, userID); err != nil {
		handleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// GetLikeState 处理 GET /api/video/auth/:id/like
func (ctl *Controller) GetLikeState(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	videoID, err := parsePathID(c.Param("id"), ErrInvalidVideoID)
	if err != nil {
		handleError(c, err)
		return
	}
	state, err := ctl.service.GetLikeState(c.Request.Context(), videoID, userID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

// CreateLike 处理 PUT /api/video/auth/:id/like
func (ctl *Controller) CreateLike(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	videoID, err := parsePathID(c.Param("id"), ErrInvalidVideoID)
	if err != nil {
		handleError(c, err)
		return
	}
	state, err := ctl.service.CreateLike(c.Request.Context(), videoID, userID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
}

// RemoveLike 处理 DELETE /api/video/auth/:id/like
func (ctl *Controller) RemoveLike(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	videoID, err := parsePathID(c.Param("id"), ErrInvalidVideoID)
	if err != nil {
		handleError(c, err)
		return
	}
	state, err := ctl.service.RemoveLike(c.Request.Context(), videoID, userID)
	if err != nil {
		handleError(c, err)
		return
	}
	c.JSON(http.StatusOK, state)
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
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
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
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
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
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
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

func handleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrInvalidUserID),
		errors.Is(err, ErrInvalidVideoID),
		errors.Is(err, ErrInvalidCommentID),
		errors.Is(err, ErrInvalidLimit),
		errors.Is(err, ErrInvalidCursor),
		errors.Is(err, ErrInvalidCommentContent),
		errors.Is(err, ErrSelfFollow):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, ErrUserNotFound),
		errors.Is(err, ErrVideoNotFound),
		errors.Is(err, ErrCommentNotFound),
		errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, ErrCommentNotAuthor):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "social operation failed"})
	}
}
