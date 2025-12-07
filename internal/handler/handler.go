package handler

import (
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/fileops"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/internal/service/processor"
	"github.com/fusionn-muse/internal/version"
	"github.com/fusionn-muse/pkg/logger"
)

// Handler handles HTTP requests.
type Handler struct {
	queue     *queue.Queue
	processor *processor.Service
	folders   config.FoldersConfig
}

// New creates a new Handler.
func New(q *queue.Queue, proc *processor.Service) *Handler {
	return &Handler{
		queue:     q,
		processor: proc,
		folders:   config.Folders(),
	}
}

// RegisterRoutes registers all API routes.
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	api := r.Group("/api/v1")
	{
		api.GET("/health", h.Health)
		api.GET("/version", h.Version)

		// Webhook endpoint for qBittorrent
		api.POST("/webhook/torrent", h.TorrentComplete)

		// Queue management
		api.GET("/queue", h.GetQueue)
		api.GET("/queue/stats", h.GetQueueStats)
		api.GET("/queue/:id", h.GetJob)

		// Retry endpoints
		api.POST("/retry/staging", h.RetryStaging)        // Re-queue all staging files
		api.POST("/retry/failed", h.RetryFailed)          // Move all failed → staging and queue
		api.POST("/retry/failed/:name", h.RetryOneFailed) // Move one failed file → staging

		// File listing
		api.GET("/files/staging", h.ListStagingFiles)
		api.GET("/files/failed", h.ListFailedFiles)
	}
}

// Health returns service health status.
func (h *Handler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Version returns service version.
func (h *Handler) Version(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"version": version.Version})
}

// TorrentCompleteRequest is the request body from qBittorrent webhook.
type TorrentCompleteRequest struct {
	Path     string `json:"path" binding:"required"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

// TorrentComplete handles the webhook when a torrent finishes downloading.
func (h *Handler) TorrentComplete(c *gin.Context) {
	var req TorrentCompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	logger.Infof("📥 Webhook received: %s", req.Path)

	var videoPath string

	if fileops.IsVideoFile(req.Path) {
		// Direct file path: process without filtering (preserve existing behavior)
		videoPath = req.Path
	} else if fileops.Exists(req.Path) {
		// Folder path: find single valid video (filter by code pattern + size)
		validPath, err := fileops.FindValidVideoFile(req.Path)
		if err != nil {
			logger.Warnf("⚠️ %v in: %s", err, req.Path)
			c.JSON(http.StatusOK, gin.H{
				"message": "no valid video files found",
				"jobs":    []string{},
			})
			return
		}
		videoPath = validPath
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path does not exist"})
		return
	}

	jobID := uuid.New().String()[:8]
	fileName := filepath.Base(videoPath)

	// Detect if this is a "light" job (Chinese subtitle detected)
	isLight := fileops.HasChineseSubtitle(fileName)

	job := queue.NewJob(jobID, videoPath, fileName, req.Name, req.Category)
	job.IsLight = isLight

	if isLight {
		// Light job: process immediately in background (no queue wait)
		logger.Infof("⚡ Light job detected (Chinese subtitle): %s (job: %s)", fileName, jobID)
		h.queue.RegisterLightJob(job) // Register for tracking, but don't queue
		go h.processLightJob(job)

		c.JSON(http.StatusAccepted, gin.H{
			"message":  "light job started (skip transcribe/translate)",
			"job":      jobID,
			"job_type": "light",
		})
	} else {
		// Heavy job: queue for sequential processing (transcribe + translate)
		h.queue.Enqueue(job)
		logger.Infof("📥 Heavy job queued: %s (job: %s)", fileName, jobID)

		c.JSON(http.StatusAccepted, gin.H{
			"message":  "heavy job queued (transcribe + translate)",
			"job":      jobID,
			"job_type": "heavy",
		})
	}
}

// processLightJob handles light jobs that bypass the queue.
// Light jobs have Chinese subtitle detected, so they skip transcription/translation.
func (h *Handler) processLightJob(job *queue.Job) {
	startTime := time.Now()

	// Update job status
	job.Status = queue.StatusProcessing
	job.StartedAt = startTime

	logger.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	logger.Infof("⚡ Light job started: %s", job.FileName)
	logger.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// Step 1: Stage file (hardlink/copy)
	stagingPath := filepath.Join(h.folders.Staging, job.FileName)
	logger.Infof("📥 Step 1: Staging file...")

	if err := fileops.HardlinkOrCopy(job.SourcePath, stagingPath); err != nil {
		h.handleLightJobError(job, "staging", err)
		return
	}
	job.StagingPath = stagingPath

	// Step 2: Clean filename
	cleanedName := fileops.CleanVideoFilename(job.FileName)
	if cleanedName != job.FileName {
		logger.Infof("📝 Cleaned filename: %s → %s", job.FileName, cleanedName)
		job.FileName = cleanedName
	}

	// Step 3: Move directly to scraping (skip processing folder, no transcribe/translate)
	scrapingPath := filepath.Join(h.folders.Scraping, job.FileName)
	logger.Infof("📦 Step 2: Moving to scraping (skip transcribe/translate)...")

	if err := fileops.Move(stagingPath, scrapingPath); err != nil {
		h.handleLightJobError(job, "move to scraping", err)
		return
	}

	// Mark completed
	job.Status = queue.StatusCompleted
	job.CompletedAt = time.Now()
	h.queue.MarkLightJobCompleted()

	elapsed := time.Since(startTime)
	logger.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	logger.Infof("✅ Light job completed: %s", job.FileName)
	logger.Infof("⏱️  Total time: %v", elapsed)
	logger.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
}

// handleLightJobError handles errors during light job processing.
func (h *Handler) handleLightJobError(job *queue.Job, step string, err error) {
	logger.Errorf("❌ Light job %s failed at %s: %v", job.ID, step, err)

	job.Status = queue.StatusFailed
	job.Error = fmt.Sprintf("%s: %v", step, err)
	job.CompletedAt = time.Now()
	h.queue.MarkLightJobFailed()

	// Try to move to failed folder if staging path exists
	if job.StagingPath != "" && fileops.Exists(job.StagingPath) {
		failedPath := filepath.Join(h.folders.Failed, job.FileName)
		if moveErr := fileops.Move(job.StagingPath, failedPath); moveErr != nil {
			logger.Warnf("⚠️ Failed to move to failed folder: %v", moveErr)
		} else {
			logger.Infof("📁 Moved to failed folder: %s", failedPath)
		}
	}
}

// GetQueue returns all jobs in the queue.
func (h *Handler) GetQueue(c *gin.Context) {
	jobs := h.queue.GetAllJobs()
	c.JSON(http.StatusOK, jobs)
}

// GetQueueStats returns queue statistics.
func (h *Handler) GetQueueStats(c *gin.Context) {
	stats := h.queue.GetQueueStats()
	c.JSON(http.StatusOK, stats)
}

// GetJob returns a specific job by ID.
func (h *Handler) GetJob(c *gin.Context) {
	id := c.Param("id")
	job := h.queue.GetJob(id)

	if job == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}

	c.JSON(http.StatusOK, job)
}

// RetryStaging re-queues all video files currently in staging folder.
func (h *Handler) RetryStaging(c *gin.Context) {
	files, err := h.processor.GetStagingFiles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if len(files) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"message": "no files in staging",
			"count":   0,
		})
		return
	}

	jobIDs := make([]string, 0, len(files))
	for _, filePath := range files {
		jobID := uuid.New().String()[:8]
		fileName := filepath.Base(filePath)

		// For staging files, source path is the staging path itself
		job := queue.NewJob(jobID, filePath, fileName, "", "")
		job.StagingPath = filePath // Already in staging
		h.queue.Enqueue(job)
		jobIDs = append(jobIDs, jobID)

		logger.Infof("📥 Re-queued from staging: %s (job: %s)", fileName, jobID)
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "staging files re-queued",
		"jobs":    jobIDs,
		"count":   len(jobIDs),
	})
}

// RetryFailed moves all failed files back to staging and queues them.
func (h *Handler) RetryFailed(c *gin.Context) {
	files, err := h.processor.GetFailedFiles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if len(files) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"message": "no files in failed folder",
			"count":   0,
		})
		return
	}

	jobIDs := make([]string, 0, len(files))
	errors := make([]string, 0, len(files))

	for _, filePath := range files {
		fileName := filepath.Base(filePath)

		// Move from failed to staging
		if err := h.processor.MoveToStagingForRetry(fileName); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", fileName, err))
			continue
		}

		// Queue the job
		jobID := uuid.New().String()[:8]
		stagingPath := filepath.Join(h.folders.Staging, fileName)
		job := queue.NewJob(jobID, stagingPath, fileName, "", "")
		job.StagingPath = stagingPath
		h.queue.Enqueue(job)
		jobIDs = append(jobIDs, jobID)

		logger.Infof("📥 Re-queued from failed: %s (job: %s)", fileName, jobID)
	}

	response := gin.H{
		"message": "failed files re-queued",
		"jobs":    jobIDs,
		"count":   len(jobIDs),
	}

	if len(errors) > 0 {
		response["errors"] = errors
	}

	c.JSON(http.StatusAccepted, response)
}

// RetryOneFailed moves a single failed file back to staging and queues it.
func (h *Handler) RetryOneFailed(c *gin.Context) {
	fileName := c.Param("name")
	if fileName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file name required"})
		return
	}

	// Move from failed to staging
	if err := h.processor.MoveToStagingForRetry(fileName); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	// Queue the job
	jobID := uuid.New().String()[:8]
	stagingPath := filepath.Join(h.folders.Staging, fileName)
	job := queue.NewJob(jobID, stagingPath, fileName, "", "")
	job.StagingPath = stagingPath
	h.queue.Enqueue(job)

	logger.Infof("📥 Re-queued from failed: %s (job: %s)", fileName, jobID)

	c.JSON(http.StatusAccepted, gin.H{
		"message": "file re-queued",
		"job":     jobID,
	})
}

// ListStagingFiles returns all files in staging folder.
func (h *Handler) ListStagingFiles(c *gin.Context) {
	files, err := h.processor.GetStagingFiles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	fileNames := make([]string, 0, len(files))
	for _, f := range files {
		fileNames = append(fileNames, filepath.Base(f))
	}

	c.JSON(http.StatusOK, gin.H{
		"folder": "staging",
		"files":  fileNames,
		"count":  len(fileNames),
	})
}

// ListFailedFiles returns all files in failed folder.
func (h *Handler) ListFailedFiles(c *gin.Context) {
	files, err := h.processor.GetFailedFiles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	fileNames := make([]string, 0, len(files))
	for _, f := range files {
		fileNames = append(fileNames, filepath.Base(f))
	}

	c.JSON(http.StatusOK, gin.H{
		"folder": "failed",
		"files":  fileNames,
		"count":  len(fileNames),
	})
}
