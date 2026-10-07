package interfaceshttprelation

import (
	"net/http"
	"strconv"

	applicationrelation "gofeed/internal/application/relation"
	domainrelation "gofeed/internal/domain/relation"
	apierror "gofeed/internal/error"
	"gofeed/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	service *applicationrelation.Service
}

func New(service *applicationrelation.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) GetFollowState(c *gin.Context) {
	followerID, followeeID, ok := followUsers(c)
	if !ok {
		return
	}
	state, err := h.service.GetFollowState(c.Request.Context(), followerID, followeeID)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, followStateFromDomain(state))
}

func (h *Handler) CreateFollow(c *gin.Context) {
	h.setFollow(c, true)
}

func (h *Handler) RemoveFollow(c *gin.Context) {
	h.setFollow(c, false)
}

func (h *Handler) setFollow(c *gin.Context, following bool) {
	followerID, followeeID, ok := followUsers(c)
	if !ok {
		return
	}
	var state domainrelation.FollowState
	var err error
	if following {
		state, err = h.service.CreateFollow(c.Request.Context(), followerID, followeeID)
	} else {
		state, err = h.service.RemoveFollow(c.Request.Context(), followerID, followeeID)
	}
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, followStateFromDomain(state))
}

func followUsers(c *gin.Context) (uint, uint, bool) {
	followerID, ok := jwt.UserID(c)
	if !ok {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return 0, 0, false
	}
	followeeID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || followeeID == 0 {
		writeError(c, domainrelation.ErrInvalidUserID)
		return 0, 0, false
	}
	return followerID, uint(followeeID), true
}

var errorRules = []apierror.Rule{
	{Match: apierror.Is(domainrelation.ErrInvalidUserID, domainrelation.ErrSelfFollow), Code: apierror.CodeInvalid, UseErrorText: true},
	{Match: apierror.Is(domainrelation.ErrUserNotFound), Code: apierror.CodeNotFound, UseErrorText: true},
}

func writeError(c *gin.Context, err error) {
	apierror.Write(c, err, "social operation failed", errorRules...)
}
