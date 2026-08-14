package router

import (
	"context"
	"errors"
	"log"
	"net/http"

	"gofeed/internal/auth"
	"gofeed/internal/middleware/jwt"
	"gofeed/internal/user"
	"gofeed/internal/video"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Options 收纳路由装配时可注入的依赖，便于集成测试替换真实实现
type Options struct {
	// UploadDir 本地媒体存储与 /static 静态服务的根目录
	// 留空时回退到生产默认值 "./.run/uploads"
	UploadDir string
}

func New(db *gorm.DB, dev bool, opts Options) *gin.Engine {
	if dev {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	uploadDir := opts.UploadDir
	if uploadDir == "" {
		uploadDir = "./.run/uploads"
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
	r.Static("/static", uploadDir)

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

	// Video routes: 公开读接口在 /api/video，写操作统一挂在 /api/video/auth
	videoCtl := video.NewController(
		video.NewService(video.NewRepository(db), &userAuthorReader{repo: user.NewRepository(db)}),
		video.NewLocalStorage(uploadDir),
	)
	videos := api.Group("/video")
	videos.GET("", videoCtl.ListVideos)
	videos.GET("/:id", videoCtl.GetVideo)

	protectedVideos := videos.Group("/auth")
	protectedVideos.Use(jwt.Auth(sessionService))
	{
		protectedVideos.POST("/upload/video", videoCtl.UploadVideo)
		protectedVideos.POST("/upload/cover", videoCtl.UploadCover)
		protectedVideos.POST("/publish", videoCtl.Publish)
		protectedVideos.GET("/mine", videoCtl.Mine)
		protectedVideos.DELETE("/:id", videoCtl.Delete)
	}

	return r
}

// userAuthorReader 将 user 仓储的按 ID 查询包装成视频服务需要的作者读取接口，
// 避免 video 包直接依赖 user 包内部仓储
type userAuthorReader struct {
	repo *user.Repository
}

func (r *userAuthorReader) GetPublicAuthor(ctx context.Context, id uint) (video.Author, error) {
	u, err := r.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 账号已注销或不存在时返回占位作者，保证历史视频仍可正常展示
			return video.Author{ID: id, Username: "已注销用户"}, nil
		}
		return video.Author{}, err
	}
	return video.Author{ID: u.ID, Username: u.Username, AvatarURL: u.AvatarURL}, nil
}
