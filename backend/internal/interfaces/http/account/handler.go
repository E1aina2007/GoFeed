package interfaceshttpaccount

import (
	"net/http"
	"strconv"

	applicationaccount "gofeed/internal/application/account"
	domainaccount "gofeed/internal/domain/account"
	apierror "gofeed/internal/error"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	service *applicationaccount.Service
}

func New(service *applicationaccount.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) GetUser(c *gin.Context) {
	id, err := getPathID(c)
	if err != nil {
		writeError(c, err)
		return
	}
	account, err := h.service.GetByID(c.Request.Context(), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": publicAccountFromDomain(account)})
}

func (h *Handler) GetUserList(c *gin.Context) {
	rawLimit, hasLimit := c.GetQuery("limit")
	rawCursor, hasCursor := c.GetQuery("cursor")
	result, err := h.service.GetUserList(c.Request.Context(), applicationaccount.UserListQuery{
		Limit: rawLimit, Cursor: rawCursor, HasLimit: hasLimit, HasCursor: hasCursor,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, userListFromApplication(result))
}

func (h *Handler) GetProfile(c *gin.Context) {
	id, err := getPathID(c)
	if err != nil {
		writeError(c, err)
		return
	}
	profile, err := h.service.GetProfile(c.Request.Context(), id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, profileFromDomain(profile))
}

func getPathID(c *gin.Context) (uint, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, domainaccount.ErrInvalidUserID
	}
	return uint(id), nil
}

var errorRules = []apierror.Rule{
	{Match: apierror.Is(domainaccount.ErrInvalidUserID, domainaccount.ErrInvalidUserListLimit, domainaccount.ErrInvalidUserCursor), Code: apierror.CodeInvalid, UseErrorText: true},
	{Match: apierror.Is(domainaccount.ErrUserNotFound), Code: apierror.CodeNotFound, UseErrorText: true},
}

func writeError(c *gin.Context, err error) {
	apierror.Write(c, err, "user operation failed", errorRules...)
}
