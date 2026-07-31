package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"github.com/brainart16/brenox/internal/attachments"
	"github.com/brainart16/brenox/internal/database"
	db "github.com/brainart16/brenox/internal/db"
	"github.com/brainart16/brenox/internal/health"
	"github.com/brainart16/brenox/internal/metrics"
	"github.com/brainart16/brenox/internal/notifications"
	"github.com/brainart16/brenox/internal/origins"
	"github.com/brainart16/brenox/internal/presence"
	redisutil "github.com/brainart16/brenox/internal/redis"
	"github.com/brainart16/brenox/internal/sandbox"
	"github.com/brainart16/brenox/internal/storage"
	"github.com/brainart16/brenox/internal/version"

	appsHandler "github.com/brainart16/brenox/internal/apps"
	authHandler "github.com/brainart16/brenox/internal/auth"
	"github.com/brainart16/brenox/internal/authz"
	"github.com/brainart16/brenox/internal/billing"
	callsHandler "github.com/brainart16/brenox/internal/calls"
	channelsHandler "github.com/brainart16/brenox/internal/channels"
	chatHandler "github.com/brainart16/brenox/internal/chat"
	"github.com/brainart16/brenox/internal/developerapi"
	middleware "github.com/brainart16/brenox/internal/middleware"
	"github.com/brainart16/brenox/internal/platformadmin"
	"github.com/brainart16/brenox/internal/ratelimit"
	realtimeHandler "github.com/brainart16/brenox/internal/realtime"
	usersHandler "github.com/brainart16/brenox/internal/users"
	"github.com/brainart16/brenox/internal/webhooks"
	workspacesHandler "github.com/brainart16/brenox/internal/workspaces"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := godotenv.Load(); err != nil {
		slog.Warn("no .env file loaded, using process environment", "error", err)
	}

	pool, err := database.NewPostgresPool()
	if err != nil {
		log.Fatal(err)
	}

	if migrationStatus, err := database.CheckMigrations(context.Background(), pool); err != nil {
		slog.Error("migration check failed", "error", err)
	} else if !migrationStatus.OK {
		slog.Error("database migrations are out of date", "version", migrationStatus.Version, "message", migrationStatus.Message)
	}

	redisClient, err := redisutil.NewClient()
	if err != nil {
		slog.Warn("redis unavailable, running in local-only realtime mode", "error", err)
	}

	s3Config := storage.LoadConfig()
	var objectStore attachments.ObjectStore
	if s3Config.Enabled() {
		client, err := storage.NewClient(s3Config)
		if err != nil {
			slog.Warn("object storage unavailable, file uploads disabled", "error", err)
		} else {
			objectStore = client
		}
	}

	queries := db.New(pool)
	authzService := authz.NewService(queries)
	wsConfig := realtimeHandler.LoadConfig()

	hub := realtimeHandler.NewHub(wsConfig)
	hub.SetSequencer(realtimeHandler.NewSequencer(redisClient))
	broker := realtimeHandler.NewBroker(redisClient, hub)
	hub.SetBroker(broker)
	go hub.Run()
	broker.Start()

	presenceService := presence.NewService(redisClient, queries, realtimeHandler.NewHubBroadcaster(hub))
	hub.SetPresenceTracker(presenceService)
	presenceHandler := presence.NewHandler(presenceService)

	notificationService := notifications.NewService(
		queries,
		realtimeHandler.NewNotificationDeliverer(hub),
		notifications.NewNoopPushSender(),
		notifications.NewNoopEmailSender(),
	)
	notificationHandler := notifications.NewHandler(notificationService)

	authService := authHandler.NewService(queries)
	authHandlerInstance := authHandler.NewHandler(authService)

	usersService := usersHandler.NewService(queries)
	usersHandlerInstance := usersHandler.NewHandler(usersService)

	platformAdminService := platformadmin.NewService(queries)
	platformAdminService.SyncBootstrapAdminsFromEnv(context.Background())
	platformAdminHandler := platformadmin.NewHandler(platformAdminService)

	authHandlerInstance.SetAdminBootstrap(platformAdminService)
	usersHandlerInstance.SetPlatformAdminService(platformAdminService)

	workspacesService := workspacesHandler.NewService(queries, authzService)
	workspacesService.SetInviteNotifier(notificationService)
	workspacesHandlerInstance := workspacesHandler.NewHandler(workspacesService)

	channelsService := channelsHandler.NewService(queries, authzService)
	channelsHandlerInstance := channelsHandler.NewHandler(channelsService, hub)

	chatService := chatHandler.NewService(queries, authzService)
	chatService.SetNotifier(notificationService)
	chatService.SetBroadcaster(realtimeHandler.NewChatBroadcaster(hub))
	chatHandlerInstance := chatHandler.NewHandler(chatService)

	attachmentService := attachments.NewService(
		queries,
		objectStore,
		attachments.NewNoopVirusScanner(),
		realtimeHandler.NewMessageBroadcaster(hub),
		chatService,
	)
	attachmentHandler := attachments.NewHandler(attachmentService)
	chatService.SetAttachmentAttacher(attachments.NewChatAttacher(attachmentService))

	callsService := callsHandler.NewService(
		queries,
		realtimeHandler.NewCallBroadcaster(hub),
		notificationService,
		channelsService,
		callsHandler.LoadConfig(),
	)
	callsHandlerInstance := callsHandler.NewHandler(callsService)

	billingService := billing.NewService(queries, billing.LoadConfig())
	billingHandler := billing.NewHandler(billingService)

	originChecker := origins.NewChecker(queries)

	appsService := appsHandler.NewService(queries)
	appsService.SetBilling(billingService)
	appsService.SetOriginChecker(originChecker)
	appsHandlerInstance := appsHandler.NewHandler(appsService)
	webhookDispatcher := webhooks.NewDispatcher(queries)
	devAPIService := developerapi.NewService(queries, realtimeHandler.NewChatBroadcaster(hub), webhookDispatcher)
	devAPIService.SetBilling(billingService)
	devAPIHandler := developerapi.NewHandler(devAPIService)
	chatService.SetBilling(billingService)
	callsService.SetBilling(billingService)
	apiRateLimiter := ratelimit.NewLimiter(redisClient, ratelimit.LoadConfig())
	sandboxRateLimiter := ratelimit.NewLimiter(redisClient, ratelimit.LoadSandboxConfig())
	ipRateLimiter := ratelimit.NewLimiter(redisClient, ratelimit.IPConfigToConfig(ratelimit.LoadIPConfig()))
	auditRecorder := middleware.NewAuditRecorder(queries)
	sandboxCleaner := sandbox.NewCleaner(queries, sandbox.LoadConfig())

	wsHandler := realtimeHandler.NewHandler(hub, chatService, channelsService, callsService, originChecker, wsConfig)
	healthHandler := health.NewHandler(pool, redisClient)
	versionHandler := version.NewHandler()

	router := gin.Default()
	router.Use(middleware.SecurityHeadersMiddleware())
	router.Use(middleware.CORSMiddleware(originChecker))
	router.Use(middleware.RequestSizeLimitMiddleware(middleware.LoadMaxBodyBytes()))
	router.Use(middleware.IPRateLimitMiddleware(ipRateLimiter))
	router.Use(middleware.AuditMiddleware(auditRecorder))
	router.Use(metrics.Middleware())
	router.Use(middleware.MaintenanceMiddleware(billingService))

	router.GET("/health", healthHandler.Check)
	router.GET("/version", versionHandler.Get)
	router.GET("/metrics", metrics.Handler())
	router.GET("/api/plans", billingHandler.ListPlans)
	router.GET("/api/platform/status", billingHandler.GetPlatformStatus)
	router.POST("/webhooks/stripe", billingHandler.StripeWebhook)

	authRouter := router.Group("/auth")
	authRouter.POST("/register", authHandlerInstance.Register)
	authRouter.POST("/login", authHandlerInstance.Login)
	authRouter.POST("/refresh", authHandlerInstance.Refresh)

	api := router.Group("/api")
	api.Use(middleware.AuthMiddleware(authService))

	api.POST("/uploads", attachmentHandler.CreateUpload)
	api.GET("/notifications", notificationHandler.List)
	api.PATCH("/notifications/:id/read", notificationHandler.MarkRead)
	api.POST("/notifications/read-all", notificationHandler.MarkAllRead)
	api.POST("/calls/:id/join", callsHandlerInstance.JoinCall)
	api.POST("/calls/:id/leave", callsHandlerInstance.LeaveCall)

	api.POST("/workspaces", workspacesHandlerInstance.CreateWorkspace)
	api.GET("/workspaces", workspacesHandlerInstance.ListWorkspaces)
	api.GET("/workspaces/:workspace_id", workspacesHandlerInstance.GetWorkspace)

	workspaceAPI := api.Group("/workspaces/:workspace_id")
	workspaceAPI.Use(middleware.EmbedWorkspaceGuard(queries))
	workspaceAPI.GET("/members", workspacesHandlerInstance.ListMembers)
	workspaceAPI.POST("/members", workspacesHandlerInstance.AddMember)
	workspaceAPI.DELETE("/members/:user_id", workspacesHandlerInstance.RemoveMember)
	workspaceAPI.PATCH("/members/:user_id", workspacesHandlerInstance.UpdateMemberRole)
	workspaceAPI.GET("/presence", presenceHandler.GetWorkspacePresence)
	workspaceAPI.POST("/channels", channelsHandlerInstance.CreateChannel)
	workspaceAPI.GET("/channels", channelsHandlerInstance.GetChannels)
	workspaceAPI.POST("/channels/:id/join", channelsHandlerInstance.JoinChannel)
	workspaceAPI.POST("/channels/:id/leave", channelsHandlerInstance.LeaveChannel)
	workspaceAPI.POST("/channels/:id/messages", chatHandlerInstance.CreateMessage)
	workspaceAPI.GET("/channels/:id/messages", chatHandlerInstance.GetMessages)
	workspaceAPI.POST("/channels/:id/messages/:message_id/attachments", attachmentHandler.AttachToMessage)
	workspaceAPI.GET("/channels/:id/messages/:message_id/attachments", attachmentHandler.ListByMessage)
	workspaceAPI.GET("/channels/:id/messages/:message_id/attachments/:attachment_id/content", attachmentHandler.DownloadContent)
	workspaceAPI.POST("/channels/:id/calls", callsHandlerInstance.InitiateCall)

	api.GET("/presence", presenceHandler.GetGlobalPresence)
	api.GET("/users/me", usersHandlerInstance.GetMe)
	api.PATCH("/users/me", usersHandlerInstance.UpdateMe)
	api.PATCH("/users/me/password", usersHandlerInstance.ChangePassword)
	api.GET("/users/me/status", presenceHandler.GetMyStatus)
	api.PATCH("/users/me/status", presenceHandler.UpdateMyStatus)
	api.GET("/ws", middleware.EmbedWorkspaceQueryGuard(queries), wsHandler.HandleWebSocket)

	api.POST("/apps", appsHandlerInstance.CreateApp)
	api.GET("/apps", appsHandlerInstance.ListApps)
	api.GET("/apps/:app_id", appsHandlerInstance.GetApp)
	api.PATCH("/apps/:app_id/origins", appsHandlerInstance.UpdateAllowedOrigins)
	api.POST("/apps/:app_id/keys", appsHandlerInstance.CreateAPIKey)
	api.GET("/apps/:app_id/keys", appsHandlerInstance.ListAPIKeys)
	api.DELETE("/apps/:app_id/keys/:key_id", appsHandlerInstance.RevokeAPIKey)
	api.POST("/apps/:app_id/webhooks", appsHandlerInstance.CreateWebhook)
	api.GET("/apps/:app_id/webhooks", appsHandlerInstance.ListWebhooks)
	api.DELETE("/apps/:app_id/webhooks/:webhook_id", appsHandlerInstance.DeleteWebhook)
	api.GET("/apps/:app_id/billing", billingHandler.GetAppBilling)
	api.POST("/apps/:app_id/billing/checkout", billingHandler.CreateCheckout)

	admin := api.Group("/admin")
	admin.Use(middleware.PlatformUserMiddleware(queries))
	admin.Use(middleware.RequirePlatformRole(platformadmin.RoleSupport))
	admin.GET("/overview", platformAdminHandler.GetOverview)
	admin.GET("/users", platformAdminHandler.ListUsers)
	admin.GET("/users/:id", platformAdminHandler.GetUser)
	admin.PATCH("/users/:id", middleware.RequirePlatformWrite(), platformAdminHandler.UpdateUser)
	admin.GET("/workspaces", platformAdminHandler.ListWorkspaces)
	admin.GET("/workspaces/:id", platformAdminHandler.GetWorkspace)
	admin.GET("/workspaces/:id/members", platformAdminHandler.ListWorkspaceMembers)
	admin.GET("/apps", platformAdminHandler.ListApps)
	admin.GET("/apps/:app_id", platformAdminHandler.GetApp)
	admin.GET("/apps/:app_id/keys", platformAdminHandler.ListAppKeys)
	admin.DELETE("/apps/:app_id/keys/:key_id", middleware.RequirePlatformWrite(), platformAdminHandler.RevokeAppKey)
	admin.GET("/audit-logs", platformAdminHandler.ListAuditLogs)
	admin.GET("/billing/overview", billingHandler.AdminGetBillingOverview)
	admin.GET("/billing/subscriptions", billingHandler.AdminListSubscriptions)
	admin.GET("/apps/:app_id/billing", billingHandler.AdminGetAppBilling)
	admin.PATCH("/apps/:app_id/subscription", middleware.RequirePlatformWrite(), billingHandler.AdminUpdateAppSubscription)
	admin.GET("/platform-settings", billingHandler.AdminGetPlatformSettings)
	admin.PATCH("/platform-settings", middleware.RequirePlatformWrite(), billingHandler.AdminUpdatePlatformSettings)
	admin.GET("/plans", billingHandler.AdminListPlans)
	admin.POST("/plans", middleware.RequirePlatformWrite(), billingHandler.AdminCreatePlan)
	admin.PATCH("/plans/:slug", middleware.RequirePlatformWrite(), billingHandler.AdminUpdatePlan)
	admin.DELETE("/plans/:slug", middleware.RequirePlatformWrite(), billingHandler.AdminDeletePlan)

	v1 := router.Group("/v1")
	v1.Use(middleware.APIKeyMiddleware(appsService))
	v1.Use(middleware.RateLimitMiddleware(apiRateLimiter, sandboxRateLimiter))
	v1.Use(middleware.IdempotencyMiddleware(devAPIService))
	v1.POST("/users", devAPIHandler.ProvisionUser)
	v1.POST("/sessions", devAPIHandler.CreateSession)
	v1.POST("/channels", devAPIHandler.CreateChannel)
	v1.POST("/messages", devAPIHandler.SendMessage)
	v1.GET("/messages", devAPIHandler.ListMessages)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	server := &http.Server{
		Addr:    ":" + port,
		Handler: router,
	}

	serverCtx, stopServerCtx := context.WithCancel(context.Background())
	defer stopServerCtx()
	sandboxCleaner.Start(serverCtx)

	go func() {
		slog.Info("http server listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server")
	broker.Close()
	hub.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		slog.Error("server shutdown failed", "error", err)
	}
}
