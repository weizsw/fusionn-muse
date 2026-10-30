package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fusionn-muse/internal/client/apprise"
	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/fileops"
	"github.com/fusionn-muse/internal/handler"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/internal/service/processor"
	"github.com/fusionn-muse/internal/toolrun"
	"github.com/fusionn-muse/internal/version"
	"github.com/fusionn-muse/pkg/logger"
)

func main() {
	// Initialize logger
	isDev := os.Getenv("ENV") != "production"
	logger.Init(isDev)
	defer logger.Sync()

	// The banner is the one startup exception to the single-line event rule: one
	// print per start, stating the version and the level that actually applies.
	version.PrintBanner(nil, logger.Log.Level().CapitalString())

	appLog := logger.Component("app")
	ctx := context.Background()

	// Load configuration
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config/config.yaml"
	}

	appLog.Debug(ctx, "config_loading", "Loading configuration", zap.String("path", configPath))
	cfgMgr, err := config.NewManager(configPath)
	if err != nil {
		fatal("config_unavailable", "Configuration could not be loaded", err)
	}
	defer cfgMgr.Stop()
	cfg := cfgMgr.Get()
	logger.SetConfigLevel(cfg.LogLevel)
	cfgMgr.OnChange(func(_, updated *config.Config) { logger.SetConfigLevel(updated.LogLevel) })

	// Get hardcoded folders
	folders := config.Folders()

	// Ensure required directories exist
	if err := ensureDirectories(folders); err != nil {
		fatal("folders_unavailable", "Data folders could not be prepared", err)
	}
	appLog.Info(ctx, "folders_ready", "Data folders are ready",
		zap.String("input", folders.Input),
		zap.String("scraping", folders.Scraping),
		zap.String("subtitles", folders.Subtitles))

	// Initialize Apprise client
	var appriseClient *apprise.Client
	if cfg.Apprise.Enabled {
		appriseClient = apprise.NewClient(cfg.Apprise)
	}
	appLog.Info(ctx, "notifications_configured", "Notification delivery configured",
		zap.Bool("enabled", cfg.Apprise.Enabled))

	// Initialize processor service
	proc := processor.New(cfgMgr, appriseClient, folders, toolrun.ExecRunner{})

	// Initialize durable job queue
	jobQueue, err := queue.Open(cfg.Queue.DBPath, proc, cfg.Queue.MaxRetries, cfg.Queue.RetryDelayMs)
	if err != nil {
		fatal("queue_unavailable", "Durable job queue could not be opened", err)
	}
	jobQueue.Start()
	defer func() { _ = jobQueue.Close() }()

	// Initialize HTTP server
	if !isDev {
		gin.SetMode(gin.ReleaseMode)
	}
	// Gin writes its route table and other diagnostics straight to stdout,
	// which bypasses the shared format; they become DEBUG events instead.
	gin.DefaultWriter = io.Discard
	gin.DebugPrintRouteFunc = func(method, route, _ string, _ int) {
		logger.Component("http").Debug(ctx, "route_registered", "Route registered",
			zap.String("method", method), zap.String("route", route))
	}

	router := gin.New()
	router.Use(handler.RequestContext())
	// The summary wraps Recovery, so every request leaves its one summary line
	// even when a handler panics.
	router.Use(handler.RequestSummary())
	router.Use(handler.Recovery())

	// Register routes
	h := handler.New(jobQueue, cfgMgr.Reload)
	h.RegisterRoutes(router)

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			appLog.Error(ctx, "service_failed", "HTTP server stopped unexpectedly", zap.String("stage", "listen"), zap.Error(err))
			os.Exit(1)
		}
	}()

	provider := cfg.Pipeline.Provider
	if provider == "" {
		provider = "videocaptioner (unsupported; configure mlx_qwen3_asr)"
	}
	appLog.Info(ctx, "service_started", "Service started",
		zap.String("version", version.Version),
		zap.Int("port", cfg.Server.Port),
		zap.String("transcription_provider", provider),
		zap.String("translate_provider", cfg.Translate.Provider),
		zap.String("target_language", cfg.Translate.TargetLang),
		zap.Bool("notifications", cfg.Apprise.Enabled))

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	appLog.Info(ctx, "shutdown_started", "Shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		appLog.Error(shutdownCtx, "shutdown_failed", "HTTP server shutdown failed", zap.Error(err))
	}

	appLog.Info(ctx, "service_stopped", "Service stopped")
}

// fatal records a startup failure and stops the process with a non-zero exit
// status. It is a function of its own because os.Exit skips deferred cleanup.
func fatal(event, msg string, err error) {
	logger.Component("app").Error(context.Background(), event, msg, zap.Error(err))
	logger.Sync()
	os.Exit(1)
}

func ensureDirectories(folders config.FoldersConfig) error {
	dirs := []string{
		folders.Input,
		folders.Staging,
		folders.Process,
		folders.Scraping,
		folders.Subtitles,
		folders.Transcriptions,
		folders.Meta,
		folders.Failed,
	}

	for _, dir := range dirs {
		if err := fileops.EnsureDir(dir); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	return nil
}
