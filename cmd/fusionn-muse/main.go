package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fusionn-muse/internal/client/apprise"
	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/fileops"
	"github.com/fusionn-muse/internal/handler"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/internal/service/processor"
	"github.com/fusionn-muse/internal/version"
	"github.com/fusionn-muse/pkg/logger"
)

func main() {
	// Initialize logger
	isDev := os.Getenv("ENV") != "production"
	logger.Init(isDev)
	defer logger.Sync()

	version.PrintBanner(nil)

	// Load configuration
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config/config.yaml"
	}

	logger.Infof("📁 Loading config: %s", configPath)
	cfgMgr, err := config.NewManager(configPath)
	if err != nil {
		logger.Fatalf("❌ Config error: %v", err)
	}
	defer cfgMgr.Stop()
	cfg := cfgMgr.Get()

	// Get hardcoded folders
	folders := config.Folders()

	// Ensure required directories exist
	if err := ensureDirectories(folders); err != nil {
		logger.Fatalf("❌ Directory setup error: %v", err)
	}

	// Initialize Apprise client
	var appriseClient *apprise.Client
	if cfg.Apprise.Enabled {
		appriseClient = apprise.NewClient(cfg.Apprise)
		logger.Infof("🔔 Notifications: enabled (key=%s)", cfg.Apprise.Key)
	} else {
		logger.Info("🔔 Notifications: disabled")
	}

	// Initialize processor service
	proc := processor.New(cfg, appriseClient)

	// Initialize job queue
	jobQueue := queue.New(proc, cfg.Queue.MaxRetries, cfg.Queue.RetryDelayMs)
	jobQueue.Start()
	defer jobQueue.Stop()

	// Initialize HTTP server
	if !isDev {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(requestLogger())

	// Register routes
	h := handler.New(jobQueue, proc)
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
			logger.Fatalf("❌ Server error: %v", err)
		}
	}()

	// Print startup info
	logger.Info("")
	logger.Infof("📂 Data folders (mount these in Docker):")
	logger.Infof("   /data/input     → Torrent downloads (read)")
	logger.Infof("   /data/finished  → Processed videos (write)")
	logger.Infof("   /data/subtitles → Translated subtitles (write)")
	logger.Info("")
	logger.Infof("🎤 Whisper: %s (model: %s)", cfg.Whisper.Provider, cfg.Whisper.Model)
	logger.Infof("🌐 Translate: %s → %s", cfg.Translate.Provider, cfg.Translate.TargetLang)
	if cfg.Translate.RateLimitRPM > 0 {
		logger.Infof("🚦 Rate limit: %d RPM", cfg.Translate.RateLimitRPM)
	}
	logger.Info("")
	logger.Infof("🌐 API server: http://localhost:%d", cfg.Server.Port)
	logger.Infof("   POST /api/v1/webhook/torrent  - qBittorrent callback")
	logger.Infof("   POST /api/v1/retry/staging    - Re-queue staging files")
	logger.Infof("   POST /api/v1/retry/failed     - Re-queue failed files")
	logger.Info("")
	logger.Info("────────────────────────────────────────────────────────────────")
	logger.Info("✅  Ready! Waiting for torrent completion webhooks...")
	logger.Info("────────────────────────────────────────────────────────────────")

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("")
	logger.Info("🛑 Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Errorf("❌ Shutdown error: %v", err)
	}

	logger.Info("👋 Goodbye!")
}

func ensureDirectories(folders config.FoldersConfig) error {
	dirs := []string{
		folders.Input,
		folders.Staging,
		folders.Process,
		folders.Finished,
		folders.Subtitles,
		folders.Failed,
	}

	for _, dir := range dirs {
		if err := fileops.EnsureDir(dir); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	return nil
}

// requestLogger returns a gin middleware for logging HTTP requests
func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		status := c.Writer.Status()
		if path != "/api/v1/health" || status >= 400 {
			latency := time.Since(start)
			logger.Debugf("HTTP %s %s → %d (%v)", c.Request.Method, path, status, latency)
		}
	}
}
