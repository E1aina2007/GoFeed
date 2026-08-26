package user

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	authn "gofeed/internal/auth"
	"gofeed/internal/middleware/jwt"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Controller struct {
	Srv           *Service
	Sessions      *authn.SessionService
	AvatarStorage AvatarStorage
}

func NewController(srv *Service, sessions *authn.SessionService, avatarStorage ...AvatarStorage) *Controller {
	var storage AvatarStorage
	if len(avatarStorage) > 0 {
		storage = avatarStorage[0]
	}
	return &Controller{Srv: srv, Sessions: sessions, AvatarStorage: storage}
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

// 处理用户注册请求
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

// 处理用户登录请求
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

// 处理刷新令牌请求并轮换刷新令牌
func (ctl *Controller) UpdateRefreshToken(c *gin.Context) {
	var req RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid refresh payload"})
		return
	}
	session, nextRefreshToken, err := ctl.Sessions.UpdateRefreshToken(c.Request.Context(), req.RefreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid refresh token"})
		return
	}
	user, err := ctl.Srv.GetByID(c.Request.Context(), session.UserID)
	if err != nil {
		_ = ctl.Sessions.UpdateSessionRevocation(c.Request.Context(), session.ID, session.UserID)
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

// 处理退出登录请求并仅撤销当前会话
func (ctl *Controller) UpdateSessionRevocation(c *gin.Context) {
	userID, ok := currentUserID(c)
	sessionID, hasSession := jwt.SessionID(c)
	if !ok || !hasSession {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	if err := ctl.Sessions.UpdateSessionRevocation(c.Request.Context(), sessionID, userID); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	c.Status(http.StatusNoContent)
}

// 处理用户详情读取请求
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

// 处理用户列表读取请求
func (ctl *Controller) GetUserList(c *gin.Context) {
	users, err := ctl.Srv.GetUserList(c.Request.Context())
	if err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users})
}

// 处理用户名修改请求
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

// 处理密码修改请求
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

// 处理用户资料修改请求
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

// UpdateAvatar 处理 POST /api/user/auth/avatar
func (ctl *Controller) UpdateAvatar(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	if ctl.AvatarStorage == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "avatar storage unavailable"})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAvatarRequestSize())
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": ErrAvatarTooLarge.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid avatar upload payload"})
		return
	}
	defer file.Close()

	if header.Size <= 0 || header.Size > MaxAvatarSize {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": ErrAvatarTooLarge.Error()})
		return
	}

	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read avatar upload"})
		return
	}
	if !validateAvatar(header.Filename, head[:n]) {
		c.JSON(http.StatusBadRequest, gin.H{"error": ErrInvalidAvatar.Error()})
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read avatar upload"})
		return
	}

	current, err := ctl.Srv.GetByID(c.Request.Context(), userID)
	if err != nil {
		handleUserError(c, err)
		return
	}
	avatarURL, err := ctl.AvatarStorage.SaveAvatar(c.Request.Context(), userID, strings.TrimSpace(header.Filename), file)
	if err != nil {
		handleUserError(c, err)
		return
	}
	if err := ctl.Srv.UpdateAvatar(c.Request.Context(), userID, avatarURL); err != nil {
		_ = ctl.AvatarStorage.RemoveAvatar(c.Request.Context(), avatarURL)
		handleUserError(c, err)
		return
	}
	if current.AvatarURL != "" && current.AvatarURL != avatarURL {
		// 旧头像清理失败不影响已提交的新头像，具体存储实现决定是否可回收旧对象
		_ = ctl.AvatarStorage.RemoveAvatar(c.Request.Context(), current.AvatarURL)
	}

	c.JSON(http.StatusCreated, gin.H{"avatar_url": avatarURL})
}

// 处理账号注销请求
func (ctl *Controller) DeleteUser(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
		return
	}
	if err := ctl.Srv.DeleteUser(c.Request.Context(), userID); err != nil {
		handleUserError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// 处理公开资料读取请求
func (ctl *Controller) GetProfile(c *gin.Context) {
	id, err := getPathID(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	profile, err := ctl.Srv.GetProfile(c.Request.Context(), id)
	if err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(http.StatusOK, GetProfileResponse{
		Account:    publicUser(profile.Account),
		VideoCount: profile.VideoCount,
	})
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
	case errors.Is(err, ErrNewUserNameRequired), errors.Is(err, ErrInvalidInput),
		errors.Is(err, ErrNothingToUpdate), errors.Is(err, ErrInvalidAvatar):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, ErrAvatarTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": err.Error()})
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
