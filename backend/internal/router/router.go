package router

import (
	"log"

	"gofeed/internal/auth"
	"gofeed/internal/middleware/jwt"
	"gofeed/internal/observability"
	"gofeed/internal/social"
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
	// ReadinessCheck 覆盖默认的 MySQL 就绪检查，主要用于隔离路由测试
	ReadinessCheck observability.ReadinessCheck
	// Middlewares 附加的全局中间件，在请求日志与恢复中间件之后注册
	// 主要用于测试注入查询计数等观测探针
	Middlewares []gin.HandlerFunc
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
	r.Use(observability.RequestLogger(), gin.Recovery())
	for _, middleware := range opts.Middlewares {
		r.Use(middleware)
	}

	// 注册通用中间件

	if err := r.SetTrustedProxies(nil); err != nil {
		log.Printf("Failed to set trusted proxies: %v", err)
	}

	// 注册存活和就绪检查接口
	r.GET("/health", observability.LivenessHandler)
	readinessCheck := opts.ReadinessCheck
	if readinessCheck == nil {
		readinessCheck = observability.DatabaseReadiness(db)
	}
	r.GET("/ready", observability.ReadinessHandler(readinessCheck))

	// 注册静态资源服务
	r.Static("/static", uploadDir)

	// 用户路由分为公开操作和需要认证的账户操作
	sessionService := auth.NewSessionService(auth.NewSessionRepository(db))
	userRepo := user.NewRepository(db)
	videoRepo := video.NewRepository(db)
	socialRepo := social.NewRepository(db)
	socialCtl := social.NewController(social.NewService(socialRepo))
	mediaStorage := video.NewLocalStorage(uploadDir)
	userCtl := user.NewController(user.NewService(userRepo, videoRepo, socialRepo), sessionService, mediaStorage)

	api := r.Group("/api")
	users := api.Group("/user")
	users.POST("/register", userCtl.CreateUser)
	users.POST("/login", userCtl.Login)
	users.POST("/refresh", userCtl.UpdateRefreshToken)
	users.GET("", userCtl.GetUserList)
	users.GET("/:id", userCtl.GetUser)
	users.GET("/:id/profile", userCtl.GetProfile)
	users.GET("/:id/followers", socialCtl.GetFollowerList)
	users.GET("/:id/following", socialCtl.GetFollowingList)

	protectedUsers := users.Group("/auth")
	protectedUsers.Use(jwt.Auth(sessionService))
	{
		protectedUsers.POST("/logout", userCtl.UpdateSessionRevocation)
		protectedUsers.PATCH("/name", userCtl.UpdateName)
		protectedUsers.PATCH("/password", userCtl.UpdatePassword)
		protectedUsers.POST("/avatar", userCtl.UpdateAvatar)
		protectedUsers.PATCH("/profile", userCtl.UpdateProfile)
		protectedUsers.GET("/:id/follow", socialCtl.GetFollowState)
		protectedUsers.PUT("/:id/follow", socialCtl.CreateFollow)
		protectedUsers.DELETE("/:id/follow", socialCtl.RemoveFollow)
		protectedUsers.DELETE("", userCtl.DeleteUser)
	}

	// 视频路由的公开读取和认证写入操作使用不同分组
	videoCtl := video.NewController(
		video.NewService(videoRepo, video.NewUserAuthorReader(userRepo), socialRepo),
		mediaStorage,
	)
	videos := api.Group("/video")
	videos.GET("", videoCtl.GetVideoList)
	videos.GET("/:id", videoCtl.GetVideo)
	videos.GET("/:id/comments", socialCtl.GetCommentList)

	protectedVideos := videos.Group("/auth")
	protectedVideos.Use(jwt.Auth(sessionService))
	{
		protectedVideos.POST("/drafts", videoCtl.CreateDraft)
		protectedVideos.GET("/drafts/:id", videoCtl.GetDraft)
		protectedVideos.POST("/drafts/:id/play", videoCtl.UpdateDraftVideo)
		protectedVideos.POST("/drafts/:id/cover", videoCtl.UpdateDraftCover)
		protectedVideos.POST("/drafts/:id/publish", videoCtl.UpdateDraftPublication)
		protectedVideos.DELETE("/drafts/:id", videoCtl.DiscardDraft)
		protectedVideos.GET("/mine", videoCtl.GetMyVideoList)
		protectedVideos.GET("/:id/status", videoCtl.GetVideoStatus)
		protectedVideos.GET("/:id/like", socialCtl.GetLikeState)
		protectedVideos.PUT("/:id/like", socialCtl.CreateLike)
		protectedVideos.DELETE("/:id/like", socialCtl.RemoveLike)
		protectedVideos.POST("/:id/comments", socialCtl.CreateComment)
		protectedVideos.DELETE("/:id/comments/:commentID", socialCtl.DeleteComment)
		protectedVideos.DELETE("/:id", videoCtl.DeleteVideo)
	}

	return r
}
