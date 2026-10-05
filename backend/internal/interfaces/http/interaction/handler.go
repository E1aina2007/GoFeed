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
	comment := result.Comment
	author := result.Author
	c.JSON(http.StatusCreated, gin.H{
		"comment": commentResponse{
			ID:        comment.ID,
			VideoID:   comment.VideoID,
			Content:   comment.Content,
			CreatedAt: comment.CreatedAt,
			Author: authorResponse{
				ID:        author.ID,
				Username:  author.Username,
				AvatarURL: author.AvatarURL,
				Bio:       author.Bio,
			},
		},
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
