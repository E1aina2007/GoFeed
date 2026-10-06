package interfaceshttpinteraction

import (
	"net/http"
	"strconv"

	applicationinteraction "gofeed/internal/application/interaction"
	domaininteraction "gofeed/internal/domain/interaction"
	apierror "gofeed/internal/error"
	"gofeed/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	service *applicationinteraction.Service
}

func New(service *applicationinteraction.Service) *Handler {
	return &Handler{
		service: service,
	}
}

func (h *Handler) GetLikeState(c *gin.Context) {
	userID, videoID, ok := actorAndVideo(c)
	if !ok {
		return
	}
	state, err := h.service.GetLikeState(c.Request.Context(), videoID, userID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, likeStateResponse{Liked: state.Liked, LikesCount: state.LikesCount})
}

func (h *Handler) GetCommentList(c *gin.Context) {
	videoID, err := pathID(c.Param("id"), domaininteraction.ErrInvalidVideoID)
	if err != nil {
		writeError(c, err)
		return
	}
	limit := 0
	if raw := c.Query("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeError(c, domaininteraction.ErrInvalidLimit)
			return
		}
	}
	result, err := h.service.GetCommentList(c.Request.Context(), videoID, c.Query("cursor"), limit)
	if err != nil {
		writeError(c, err)
		return
	}
	response := commentListResponse{Items: make([]commentResponse, 0, len(result.Items)), NextCursor: result.NextCursor}
	for _, item := range result.Items {
		response.Items = append(response.Items, commentResponseFromDomain(item.Comment, item.Author))
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) CreateLike(c *gin.Context) {
	h.setLike(c, true)
}

func (h *Handler) RemoveLike(c *gin.Context) {
	h.setLike(c, false)
}

// CreateComment 将现有评论 HTTP 契约映射到独立的互动写入用例
func (h *Handler) CreateComment(c *gin.Context) {
	userID, videoID, ok := actorAndVideo(c)
	if !ok {
		return
	}
	var request createCommentRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		apierror.WriteCode(c, apierror.CodeInvalid, domaininteraction.ErrInvalidCommentContent.Error())
		return
	}
	result, err := h.service.CreateComment(c.Request.Context(), videoID, userID, request.Content)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"comment": commentResponseFromDomain(result.Comment, result.Author),
	})
}

func (h *Handler) DeleteComment(c *gin.Context) {
	userID, videoID, ok := actorAndVideo(c)
	if !ok {
		return
	}
	commentID, err := pathID(c.Param("commentID"), domaininteraction.ErrInvalidCommentID)
	if err != nil {
		writeError(c, err)
		return
	}
	if err := h.service.DeleteComment(c.Request.Context(), videoID, commentID, userID); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) setLike(c *gin.Context, liked bool) {
	userID, videoID, ok := actorAndVideo(c)
	if !ok {
		return
	}
	var state domaininteraction.LikeState
	var err error
	if liked {
		state, err = h.service.CreateLike(c.Request.Context(), videoID, userID)
	} else {
		state, err = h.service.RemoveLike(c.Request.Context(), videoID, userID)
	}
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, likeStateResponse{
		Liked:      state.Liked,
		LikesCount: state.LikesCount,
	})
}

func actorAndVideo(c *gin.Context) (uint, uint, bool) {
	userID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return 0, 0, false
	}
	videoID, err := pathID(c.Param("id"), domaininteraction.ErrInvalidVideoID)
	if err != nil {
		writeError(c, err)
		return 0, 0, false
	}
	return userID, videoID, true
}

func pathID(raw string, invalid error) (uint, error) {
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, invalid
	}
	return uint(id), nil
}

var errorRules = []apierror.Rule{
	{
		Match: apierror.Is(
			domaininteraction.ErrInvalidUserID,
			domaininteraction.ErrInvalidVideoID,
			domaininteraction.ErrInvalidCommentID,
			domaininteraction.ErrInvalidLimit,
			domaininteraction.ErrInvalidCursor,
			domaininteraction.ErrInvalidCommentContent,
		),
		Code:         apierror.CodeInvalid,
		UseErrorText: true,
	},
	{
		Match: apierror.Is(
			domaininteraction.ErrUserNotFound,
			domaininteraction.ErrVideoNotFound,
			domaininteraction.ErrCommentNotFound,
		),
		Code:         apierror.CodeNotFound,
		UseErrorText: true,
	},
	{
		Match:        apierror.Is(domaininteraction.ErrCommentNotAuthor),
		Code:         apierror.CodeForbidden,
		UseErrorText: true,
	},
}

func writeError(c *gin.Context, err error) {
	apierror.Write(c, err, "social operation failed", errorRules...)
}
