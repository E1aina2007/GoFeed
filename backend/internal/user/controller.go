package user

import (
	"errors"
	"net/http"
	"strconv"

	authn "gofeed/internal/auth"
	"gofeed/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Controller struct {
	Srv      *Service
	Sessions *authn.SessionService
}

func NewController(srv *Service, sessions *authn.SessionService) *Controller {
	return &Controller{Srv: srv, Sessions: sessions}
}

func getPathID(c *gin.Context) (uint, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("invalid user id")
	}
	return uint(id), nil
}

func currentUserID(c *gin.Context) (uint, bool) {
	return jwt.UserID(c)
}

// CreateUser handles POST /api/user/register.
func (ctl *Controller) CreateUser(c *gin.Context) {
	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid registration payload"})
		return
	}
	user := &User{Username: req.Username, Password: req.Password}
	if err := ctl.Srv.CreateUser(c.Request.Context(), user); err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user": publicUser(user)})
}

// Login handles POST /api/user/login.
func (ctl *Controller) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid login payload"})
		return
	}
	user, err := ctl.Srv.Authenticate(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		handleLoginError(c, err)
		return
	}
	pair, err := ctl.Sessions.Create(c.Request.Context(), user.ID, user.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
		return
	}
	c.JSON(http.StatusOK, loginResponse(pair, user))
}

// Refresh handles POST /api/user/refresh and rotates the opaque refresh token.
func (ctl *Controller) Refresh(c *gin.Context) {
	var req RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid refresh payload"})
		return
	}
	session, nextRefreshToken, err := ctl.Sessions.Refresh(c.Request.Context(), req.RefreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token"})
		return
	}
	user, err := ctl.Srv.GetByID(c.Request.Context(), session.UserID)
	if err != nil {
		_ = ctl.Sessions.Revoke(c.Request.Context(), session.ID, session.UserID)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token"})
		return
	}
	accessToken, err := authn.GenerateToken(user.ID, user.Username, session.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create access token"})
		return
	}
	c.JSON(http.StatusOK, LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: nextRefreshToken,
		ExpiresAt:    session.ExpiresAt,
		User:         publicUser(user),
	})
}

// Logout handles POST /api/user/auth/logout and revokes only this session.
func (ctl *Controller) Logout(c *gin.Context) {
	userID, ok := currentUserID(c)
	sessionID, hasSession := jwt.SessionID(c)
	if !ok || !hasSession {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	if err := ctl.Sessions.Revoke(c.Request.Context(), sessionID, userID); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	c.Status(http.StatusNoContent)
}

// GetUser handles GET /api/user/:id.
func (ctl *Controller) GetUser(c *gin.Context) {
	id, err := getPathID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, err := ctl.Srv.GetByID(c.Request.Context(), id)
	if err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": publicUser(user)})
}

// ListUsers handles GET /api/user.
func (ctl *Controller) ListUsers(c *gin.Context) {
	users, err := ctl.Srv.GetAll(c.Request.Context())
	if err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users})
}

// UpdateName handles PATCH /api/user/auth/name.
func (ctl *Controller) UpdateName(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	var req UpdateNameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid username payload"})
		return
	}
	if err := ctl.Srv.UpdateName(c.Request.Context(), userID, req.NewUsername); err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "username updated successfully"})
}

// UpdatePassword handles PATCH /api/user/auth/password.
func (ctl *Controller) UpdatePassword(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	var req UpdatePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid password payload"})
		return
	}
	if err := ctl.Srv.UpdatePassword(c.Request.Context(), userID, req.OldPassword, req.NewPassword); err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "password updated; sign in again"})
}

// UpdateProfile handles PATCH /api/user/auth/profile.
func (ctl *Controller) UpdateProfile(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid profile payload"})
		return
	}
	if err := ctl.Srv.UpdateProfile(c.Request.Context(), userID, &req); err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "profile updated successfully"})
}

// DeleteUser handles DELETE /api/user/auth.
func (ctl *Controller) DeleteUser(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	if err := ctl.Srv.Delete(c.Request.Context(), userID); err != nil {
		handleUserError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// GetProfile handles GET /api/user/:id/profile.
func (ctl *Controller) GetProfile(c *gin.Context) {
	id, err := getPathID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, err := ctl.Srv.GetByID(c.Request.Context(), id)
	if err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, GetProfileResponse{Account: publicUser(user)})
}

func publicUser(user *User) FindByIDResponse {
	return FindByIDResponse{ID: user.ID, Username: user.Username, AvatarURL: user.AvatarURL, Bio: user.Bio}
}

func loginResponse(pair *authn.TokenPair, user *User) LoginResponse {
	return LoginResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresAt:    pair.ExpiresAt,
		User:         publicUser(user),
	}
}

func handleLoginError(c *gin.Context, err error) {
	if errors.Is(err, ErrInvalidCredentials) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to authenticate"})
}

func handleUserError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNewUserNameRequired), errors.Is(err, ErrInvalidInput):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, ErrUsernameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, ErrWrongPassword):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "user operation failed"})
	}
}
