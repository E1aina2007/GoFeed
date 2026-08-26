package router

import (
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

	// 注册通用中间件

	if err := r.SetTrustedProxies(nil); err != nil {
		log.Printf("Failed to set trusted proxies: %v", err)
	}

	// 注册健康检查接口
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"name": "GoFeed", "status": "ok"})
	})

	// 注册静态资源服务
	r.Static("/static", uploadDir)

	// 用户路由分为公开操作和需要认证的账户操作
	sessionService := auth.NewSessionService(auth.NewSessionRepository(db))
	userRepo := user.NewRepository(db)
	videoRepo := video.NewRepository(db)
	mediaStorage := video.NewLocalStorage(uploadDir)
	userCtl := user.NewController(user.NewService(userRepo, videoRepo), sessionService, mediaStorage)

	api := r.Group("/api")
	users := api.Group("/user")
	users.POST("/register", userCtl.CreateUser)
	users.POST("/login", userCtl.Login)
	users.POST("/refresh", userCtl.UpdateRefreshToken)
	users.GET("", userCtl.GetUserList)
	users.GET("/:id", userCtl.GetUser)
	users.GET("/:id/profile", userCtl.GetProfile)

	protectedUsers := users.Group("/auth")
	protectedUsers.Use(jwt.Auth(sessionService))
	{
		protectedUsers.POST("/logout", userCtl.UpdateSessionRevocation)
		protectedUsers.PATCH("/name", userCtl.UpdateName)
		protectedUsers.PATCH("/password", userCtl.UpdatePassword)
		protectedUsers.POST("/avatar", userCtl.UpdateAvatar)
		protectedUsers.PATCH("/profile", userCtl.UpdateProfile)
		protectedUsers.DELETE("", userCtl.DeleteUser)
	}

	// 视频路由的公开读取和认证写入操作使用不同分组
	videoCtl := video.NewController(
		video.NewService(videoRepo, video.NewUserAuthorReader(userRepo)),
		mediaStorage,
	)
	videos := api.Group("/video")
	videos.GET("", videoCtl.GetVideoList)
	videos.GET("/:id", videoCtl.GetVideo)

	protectedVideos := videos.Group("/auth")
	protectedVideos.Use(jwt.Auth(sessionService))
	{
		protectedVideos.POST("/drafts", videoCtl.CreateDraft)
		protectedVideos.POST("/drafts/:id/play", videoCtl.UpdateDraftVideo)
		protectedVideos.POST("/drafts/:id/cover", videoCtl.UpdateDraftCover)
		protectedVideos.POST("/drafts/:id/publish", videoCtl.UpdateDraftPublication)
		protectedVideos.GET("/mine", videoCtl.GetMyVideoList)
		protectedVideos.DELETE("/:id", videoCtl.DeleteVideo)
	}

	return r
}
