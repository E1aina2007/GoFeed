package interfaceshttpaccount

import (
	"net/http"

	applicationaccount "gofeed/internal/application/account"
	domainaccount "gofeed/internal/domain/account"
	apierror "gofeed/internal/error"
	"gofeed/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
)

type SessionHandler struct {
	service *applicationaccount.SessionService
}

func NewSessions(service *applicationaccount.SessionService) *SessionHandler {
	return &SessionHandler{service: service}
}

func (h *SessionHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid login payload")
		return
	}
	result, err := h.service.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		apierror.Write(c, err, "failed to authenticate", loginErrorRules...)
		return
	}
	c.JSON(http.StatusOK, sessionFromDomain(result))
}

func (h *SessionHandler) UpdateRefreshToken(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		apierror.WriteCode(c, apierror.CodeInvalid, "invalid refresh payload")
		return
	}
	result, err := h.service.UpdateRefreshToken(c.Request.Context(), req.RefreshToken)
	if err != nil {
		apierror.Write(c, err, "invalid refresh token", refreshErrorRules...)
		return
	}
	c.JSON(http.StatusOK, sessionFromDomain(result))
}

func (h *SessionHandler) UpdateSessionRevocation(c *gin.Context) {
	userID, ok := jwt.UserID(c)
	sessionID, hasSession := jwt.SessionID(c)
	if !ok || !hasSession {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	if err := h.service.UpdateSessionRevocation(c.Request.Context(), sessionID, userID); err != nil {
		apierror.WriteUnauthorized(c, "invalid or expired token")
		return
	}
	c.Status(http.StatusNoContent)
}

var loginErrorRules = []apierror.Rule{
	{Match: apierror.Is(domainaccount.ErrSessionCreationFailed), Code: apierror.CodeInternal, PublicMessage: "failed to create session"},
	{Match: apierror.Is(domainaccount.ErrInvalidCredentials), Code: apierror.CodeUnauthorized, PublicMessage: "invalid username or password"},
}

var refreshErrorRules = []apierror.Rule{
	{Match: apierror.Is(domainaccount.ErrAccessTokenCreationFailed), Code: apierror.CodeInternal, PublicMessage: "failed to create access token"},
	{Match: func(error) bool { return true }, Code: apierror.CodeUnauthorized, PublicMessage: "invalid refresh token"},
}
