package router

import (
	"log"

	applicationaccount "gofeed/internal/application/account"
	applicationfeed "gofeed/internal/application/feed"
	applicationinteraction "gofeed/internal/application/interaction"
	applicationrelation "gofeed/internal/application/relation"
	applicationvideo "gofeed/internal/application/video"
	infrajwt "gofeed/internal/infra/jwt"
	infraaccount "gofeed/internal/infra/persistence/account"
	infrafeed "gofeed/internal/infra/persistence/feed"
	infrainteraction "gofeed/internal/infra/persistence/interaction"
	infrarelation "gofeed/internal/infra/persistence/relation"
	infravideo "gofeed/internal/infra/persistence/video"
	inframedia "gofeed/internal/infra/storage/media"
	interfaceshttpaccount "gofeed/internal/interfaces/http/account"
	interfaceshttpauth "gofeed/internal/interfaces/http/auth"
	interfaceshttpfeed "gofeed/internal/interfaces/http/feed"
	interfaceshttpinteraction "gofeed/internal/interfaces/http/interaction"
	interfaceshttprelation "gofeed/internal/interfaces/http/relation"
	interfaceshttpvideo "gofeed/internal/interfaces/http/video"
	"gofeed/internal/middleware/ratelimit"
	"gofeed/internal/observability"
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
	// RateLimitCache 为注册与登录限流提供脚本执行能力
	RateLimitCache ratelimit.Cache
	FeedPageCache  applicationfeed.PageCache
	FeedCardCache  applicationfeed.CardCache
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
	sessionRepo := infraaccount.NewSessionRepository(db)
	sessionService := applicationaccount.NewSessionLifecycle(sessionRepo, sessionRepo,
		infrajwt.RefreshTokenGenerator{}, infrajwt.RefreshTokenHasher{}, infrajwt.AccessTokenIssuer{})
	userRepo := infraaccount.NewRepository(db)
	videoRepo := video.NewRepository(db)
	relationRepo := infrarelation.New(db)
	relationHandler := interfaceshttprelation.New(applicationrelation.New(relationRepo, relationRepo))
	interactionRepo := infrainteraction.New(db, true)
	interactionHandler := interfaceshttpinteraction.New(applicationinteraction.New(interactionRepo, interactionRepo))
	engagementReader := infrainteraction.NewEngagementReader(interactionRepo)
	profileMetricsReader := infrainteraction.NewProfileMetricsReader(interactionRepo, relationRepo)
	mediaStorage := inframedia.NewLocalStorage(uploadDir)
	accountHandler := interfaceshttpaccount.New(applicationaccount.New(infraaccount.NewReader(userRepo),
		infraaccount.NewPublishedVideoCounter(videoRepo), infraaccount.NewProfileMetricsReader(profileMetricsReader)))
	registrationHandler := interfaceshttpaccount.NewRegistration(applicationaccount.NewRegistration(
		infraaccount.NewCreator(userRepo), infraaccount.BcryptPasswordHasher{}))
	sessionHandler := interfaceshttpaccount.NewSessions(applicationaccount.NewSessions(
		infraaccount.NewCredentialReader(userRepo), infraaccount.NewReader(userRepo), infraaccount.BcryptPasswordVerifier{},
		sessionService, infrajwt.AccessTokenIssuer{}))
	securityHandler := interfaceshttpaccount.NewAccountSecurity(applicationaccount.NewAccountSecurity(
		infraaccount.NewCredentialReader(userRepo), infraaccount.BcryptPasswordVerifier{}, infraaccount.BcryptPasswordHasher{},
		infraaccount.NewAccountSecurityWriter(db)))
	profileHandler := interfaceshttpaccount.NewProfile(applicationaccount.NewProfile(
		infraaccount.NewReader(userRepo), infraaccount.NewProfileWriter(userRepo), infraaccount.NewAvatarStorage(mediaStorage)))

	api := r.Group("/api")
	users := api.Group("/user")
	users.POST("/register", ratelimit.Limit(opts.RateLimitCache, ratelimit.RegisterAction, ratelimit.RegisterMaxRequests, ratelimit.RegisterWindow), registrationHandler.CreateUser)
	users.POST("/login", ratelimit.Limit(opts.RateLimitCache, ratelimit.LoginAction, ratelimit.LoginMaxRequests, ratelimit.LoginWindow), sessionHandler.Login)
	users.POST("/refresh", sessionHandler.UpdateRefreshToken)
	users.GET("", accountHandler.GetUserList)
	users.GET("/:id", accountHandler.GetUser)
	users.GET("/:id/profile", accountHandler.GetProfile)
	users.GET("/:id/followers", relationHandler.GetFollowerList)
	users.GET("/:id/following", relationHandler.GetFollowingList)

	protectedUsers := users.Group("/auth")
	protectedUsers.Use(interfaceshttpauth.Auth(sessionService))
	{
		protectedUsers.POST("/logout", sessionHandler.UpdateSessionRevocation)
		protectedUsers.PATCH("/name", profileHandler.UpdateName)
		protectedUsers.PATCH("/password", securityHandler.UpdatePassword)
		protectedUsers.POST("/avatar", profileHandler.UpdateAvatar)
		protectedUsers.PATCH("/profile", profileHandler.UpdateProfile)
		protectedUsers.GET("/:id/follow", relationHandler.GetFollowState)
		protectedUsers.PUT("/:id/follow", relationHandler.CreateFollow)
		protectedUsers.DELETE("/:id/follow", relationHandler.RemoveFollow)
		protectedUsers.DELETE("", securityHandler.DeleteUser)
	}

	// 视频路由的公开读取和认证写入操作使用不同分组
	authorReader := infraaccount.NewAuthorReader(infraaccount.NewReader(userRepo))
	videoService := video.NewService(videoRepo)
	videoCtl := video.NewController(videoService, infravideo.NewMediaStorage(mediaStorage))
	publicVideoHandler := interfaceshttpvideo.New(applicationvideo.New(infravideo.NewReader(videoRepo),
		infravideo.NewAuthorReader(authorReader), infravideo.NewEngagementReader(engagementReader)))
	myVideoHandler := interfaceshttpvideo.New(applicationvideo.NewMyVideoList(infravideo.NewAuthorVideoListReader(videoRepo),
		infravideo.NewAuthorReader(authorReader), infravideo.NewEngagementReader(engagementReader)))
	processingStatusHandler := interfaceshttpvideo.NewProcessingStatus(applicationvideo.NewProcessingStatus(
		infravideo.NewProcessingStatusReader(videoRepo)))
	draftHandler := interfaceshttpvideo.NewDrafts(applicationvideo.NewDrafts(
		infravideo.NewDraftCreator(videoRepo), infravideo.NewDraftReader(videoRepo)))
	feedRepo := infrafeed.New(videoRepo, authorReader, engagementReader)
	feedService := applicationfeed.New(feedRepo,
		applicationfeed.WithFollowingReader(infrafeed.NewFollowingReader(videoRepo, relationRepo)),
		applicationfeed.WithPageCache(opts.FeedPageCache, infrafeed.NewCardReader(videoRepo), func(observation applicationfeed.CacheObservation) {
			log.Printf("event=feed_page_cache result=%s duration_ms=%d", observation.Result, observation.Duration.Milliseconds())
		}),
		applicationfeed.WithCardCache(opts.FeedCardCache, infrafeed.NewPublicStateReader(videoRepo), func(observation applicationfeed.CardCacheObservation) {
			log.Printf("event=feed_card_cache result=%s count=%d duration_ms=%d", observation.Result, observation.Count, observation.Duration.Milliseconds())
		}),
	)
	feedHandler := interfaceshttpfeed.New(feedService, interfaceshttpfeed.WithFollowingAuth(interfaceshttpauth.Auth(sessionService)))
	api.GET("/feed", feedHandler.GetFeed)
	videos := api.Group("/video")
	videos.GET("", publicVideoHandler.GetVideoList)
	videos.GET("/:id", publicVideoHandler.GetVideo)
	videos.GET("/:id/comments", interactionHandler.GetCommentList)

	protectedVideos := videos.Group("/auth")
	protectedVideos.Use(interfaceshttpauth.Auth(sessionService))
	{
		protectedVideos.POST("/drafts", draftHandler.CreateDraft)
		protectedVideos.GET("/drafts/:id", draftHandler.GetDraft)
		protectedVideos.POST("/drafts/:id/play", videoCtl.UpdateDraftVideo)
		protectedVideos.POST("/drafts/:id/cover", videoCtl.UpdateDraftCover)
		protectedVideos.POST("/drafts/:id/publish", videoCtl.UpdateDraftPublication)
		protectedVideos.DELETE("/drafts/:id", videoCtl.DiscardDraft)
		protectedVideos.GET("/mine", myVideoHandler.GetMyVideoList)
		protectedVideos.GET("/:id/status", processingStatusHandler.GetVideoStatus)
		protectedVideos.GET("/:id/like", interactionHandler.GetLikeState)
		protectedVideos.PUT("/:id/like", interactionHandler.CreateLike)
		protectedVideos.DELETE("/:id/like", interactionHandler.RemoveLike)
		protectedVideos.POST("/:id/comments", interactionHandler.CreateComment)
		protectedVideos.DELETE("/:id/comments/:commentID", interactionHandler.DeleteComment)
		protectedVideos.DELETE("/:id", videoCtl.DeleteVideo)
	}

	return r
}
