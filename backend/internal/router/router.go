package router

import (
	"log"
	"net/http"

	"gofeed/internal/auth"
	"gofeed/internal/middleware/jwt"
	"gofeed/internal/user"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func New(db *gorm.DB, dev bool) *gin.Engine {
	if dev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.Use(gin.Recovery())
	if dev {
		r.Use(gin.Logger())
	}

	// middlewares

	if err := r.SetTrustedProxies(nil); err != nil {
		log.Printf("Failed to set trusted proxies: %v", err)
	}

	// health check
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"name": "GoFeed", "status": "ok"})
	})

	// uploads
	r.Static("/static", "./.run/uploads")

	// User routes are split between public operations and authenticated account operations.
	sessionService := auth.NewSessionService(auth.NewSessionRepository(db))
	userCtl := user.NewController(user.NewService(user.NewRepository(db)), sessionService)

	api := r.Group("/api")
	users := api.Group("/user")
	users.POST("/register", userCtl.CreateUser)
	users.POST("/login", userCtl.Login)
	users.POST("/refresh", userCtl.Refresh)
	users.GET("", userCtl.ListUsers)
	users.GET("/:id", userCtl.GetUser)
	users.GET("/:id/profile", userCtl.GetProfile)

	protectedUsers := users.Group("/auth")
	protectedUsers.Use(jwt.Auth(sessionService))
	{
		protectedUsers.POST("/logout", userCtl.Logout)
		protectedUsers.PATCH("/name", userCtl.UpdateName)
		protectedUsers.PATCH("/password", userCtl.UpdatePassword)
		protectedUsers.PATCH("/profile", userCtl.UpdateProfile)
		protectedUsers.DELETE("", userCtl.DeleteUser)
	}

	return r
}
