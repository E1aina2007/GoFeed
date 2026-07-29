package user

import (
	"errors"
	"strconv"

	apierror "gofeed/internal/error"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Controller struct {
	Srv *Service
}

func NewController(srv *Service) *Controller {
	return &Controller{Srv: srv}
}

// getPathID extracts the user ID from the URL path parameter ":id".
func getPathID(c *gin.Context) (uint, error) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		return 0, errors.New("invalid user id")
	}
	return uint(id), nil
}

// CreateUser POST /api/users
func (ctl *Controller) CreateUser(c *gin.Context) {
	req := CreateRequest{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ParseStatusCode(err), gin.H{"error": err.Error()})
		return
	}
	if err := ctl.Srv.CreateUser(c.Request.Context(), &User{
		Username: req.Username,
		Password: req.Password,
	}); err != nil {
		c.JSON(apierror.ParseStatusCode(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"message": "created successfully"})
}

// GetUser GET /api/users/:id
func (ctl *Controller) GetUser(c *gin.Context) {
	id, err := getPathID(c)
	if err != nil {
		c.JSON(apierror.ParseStatusCode(err), gin.H{"error": err.Error()})
		return
	}
	user, err := ctl.Srv.GetByID(c.Request.Context(), id)
	if err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(200, gin.H{"user": user})
}

// ListUsers GET /api/users
func (ctl *Controller) ListUsers(c *gin.Context) {
	users, err := ctl.Srv.GetAll(c.Request.Context())
	if err != nil {
		c.JSON(apierror.ParseStatusCode(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"users": users})
}

// UpdateName PUT /api/users/:id/name
func (ctl *Controller) UpdateName(c *gin.Context) {
	id, err := getPathID(c)
	if err != nil {
		c.JSON(apierror.ParseStatusCode(err), gin.H{"error": err.Error()})
		return
	}

	req := UpdateNameRequest{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ParseStatusCode(err), gin.H{"error": err.Error()})
		return
	}

	if err := ctl.Srv.UpdateName(c.Request.Context(), id, req.NewUsername); err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(200, gin.H{"message": "username updated successfully"})
}

// UpdatePassword PUT /api/users/:id/password
func (ctl *Controller) UpdatePassword(c *gin.Context) {
	id, err := getPathID(c)
	if err != nil {
		c.JSON(apierror.ParseStatusCode(err), gin.H{"error": err.Error()})
		return
	}
	_ = id // keep for consistency; password update uses username lookup

	req := UpdatePasswordRequest{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ParseStatusCode(err), gin.H{"error": err.Error()})
		return
	}

	if err := ctl.Srv.UpdatePassword(c.Request.Context(), req.Username, req.OldPassword, req.NewPassword); err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(200, gin.H{"message": "password updated successfully"})
}

// UpdateProfile PUT /api/users/:id/profile
func (ctl *Controller) UpdateProfile(c *gin.Context) {
	id, err := getPathID(c)
	if err != nil {
		c.JSON(apierror.ParseStatusCode(err), gin.H{"error": err.Error()})
		return
	}

	req := UpdateProfileRequest{}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(apierror.ParseStatusCode(err), gin.H{"error": err.Error()})
		return
	}

	if err := ctl.Srv.UpdateProfile(c.Request.Context(), id, &req); err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(200, gin.H{"message": "profile updated successfully"})
}

// DeleteUser DELETE /api/users/:id
func (ctl *Controller) DeleteUser(c *gin.Context) {
	id, err := getPathID(c)
	if err != nil {
		c.JSON(apierror.ParseStatusCode(err), gin.H{"error": err.Error()})
		return
	}
	if err := ctl.Srv.Delete(c.Request.Context(), id); err != nil {
		handleUserError(c, err)
		return
	}
	c.JSON(200, gin.H{"message": "user deleted successfully"})
}

// GetProfile GET /api/users/:id/profile
func (ctl *Controller) GetProfile(c *gin.Context) {
	id, err := getPathID(c)
	if err != nil {
		c.JSON(apierror.ParseStatusCode(err), gin.H{"error": err.Error()})
		return
	}
	user, err := ctl.Srv.GetByID(c.Request.Context(), id)
	if err != nil {
		handleUserError(c, err)
		return
	}
	resp := GetProfileResponse{
		Account: FindByIDResponse{
			ID:        user.ID,
			Username:  user.Username,
			AvatarURL: user.AvatarURL,
			Bio:       user.Bio,
		},
	}
	c.JSON(200, resp)
}

// handleUserError maps known sentinel errors to HTTP status codes.
func handleUserError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNewUserNameRequired):
		c.JSON(400, gin.H{"error": err.Error()})
	case errors.Is(err, ErrUsernameTaken):
		c.JSON(409, gin.H{"error": err.Error()})
	case errors.Is(err, ErrUserDeleted):
		c.JSON(410, gin.H{"error": err.Error()})
	case errors.Is(err, ErrWrongPassword):
		c.JSON(403, gin.H{"error": err.Error()})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(404, gin.H{"error": "user not found"})
	default:
		c.JSON(500, gin.H{"error": err.Error()})
	}
}
