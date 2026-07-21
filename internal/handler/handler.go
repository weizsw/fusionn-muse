package handler

import (
	"context"
	"errors"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/manualrequeue"
	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/internal/version"
	"github.com/fusionn-muse/pkg/logger"
)

// Handler handles HTTP requests.
type Handler struct {
	queue   *queue.Queue
	manual  *manualrequeue.Service
	folders config.FoldersConfig
}

// New creates a new Handler.
func New(q *queue.Queue, folders config.FoldersConfig) *Handler {
	return &Handler{
		queue:   q,
		manual:  manualrequeue.New(q, folders, nil),
		folders: folders,
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

	ctx := context.Background()
	logger.FromContext(ctx).Infof("📥 Webhook received: %s", req.Path)
	go h.resolveAndDispatchTorrent(ctx, req)

	c.JSON(http.StatusAccepted, gin.H{"message": "webhook accepted"})
}

func (h *Handler) resolveAndDispatchTorrent(ctx context.Context, req TorrentCompleteRequest) {
	log := logger.FromContext(ctx)
	resolved, err := mediaintake.ResolveMedia(mediaintake.ResolveRequest{
		Context:     ctx,
		Path:        req.Path,
		TorrentName: req.Name,
		StagingDir:  h.folders.Staging,
	})
	if err != nil {
		if errors.Is(err, mediaintake.ErrNoValidMedia) {
			log.Warnf("⚠️ %v in: %s", err, req.Path)
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			log.Warnf("⚠️ Path does not exist for webhook: %s: %v", req.Path, err)
			return
		}
		log.Errorf("❌ Failed to resolve media for webhook path %s: %v", req.Path, err)
		return
	}

	jobID := uuid.NewString()
	ctx = logger.WithJob(ctx, jobID)
	log = logger.FromContext(ctx)
	fileName := resolved.FileName
	isLight := resolved.HasChineseSubtitle

	job := queue.NewJob(jobID, resolved.SourcePath, fileName, req.Name, req.Category)
	job.IsLight = isLight
	job.StagingPath = resolved.StagingPath
	job.SubtitleDetectionReason = resolved.SubtitleDetectionReason
	job.SidecarSubtitlePath = resolved.SidecarSubtitlePath

	if err := h.queue.Accept(job); err != nil {
		log.Errorf("❌ Queue rejected job: %v", err)
		return
	}
	if isLight {
		log.Infof("⚡ Light job accepted (Chinese subtitle): %s", fileName)
	} else {
		log.Infof("📥 Heavy job accepted: %s", fileName)
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
	h.writeBulkRequeue(c, manualrequeue.Staging, "no files in staging")
}

// RetryFailed moves all failed files back to staging and queues them.
func (h *Handler) RetryFailed(c *gin.Context) {
	h.writeBulkRequeue(c, manualrequeue.Failed, "no files in failed folder")
}

// RetryOneFailed moves a single failed file back to staging and queues it.
func (h *Handler) RetryOneFailed(c *gin.Context) {
	fileName := c.Param("name")
	result := h.manual.Requeue(c.Request.Context(), manualrequeue.Request{
		Location: manualrequeue.Failed,
		FileName: &fileName,
	})
	if result.Err != nil {
		c.JSON(statusForManualError(result.Err), gin.H{"error": result.Err.Error()})
		return
	}
	if len(result.Failed) != 0 {
		failure := result.Failed[0]
		c.JSON(statusForManualError(failure.Err), gin.H{
			"error":  failure.Err.Error(),
			"file":   failure.FileName,
			"staged": failure.Staged,
		})
		return
	}

	accepted := result.Accepted[0]
	c.JSON(http.StatusAccepted, gin.H{
		"message": "file re-queued",
		"file":    accepted.FileName,
		"job":     accepted.JobID,
		"staged":  true,
	})
}

// ListStagingFiles returns all files in staging folder.
func (h *Handler) ListStagingFiles(c *gin.Context) {
	h.writeManagedFiles(c, manualrequeue.Staging)
}

// ListFailedFiles returns all files in failed folder.
func (h *Handler) ListFailedFiles(c *gin.Context) {
	h.writeManagedFiles(c, manualrequeue.Failed)
}

type manualFailureResponse struct {
	FileName string `json:"file"`
	Error    string `json:"error"`
	Staged   bool   `json:"staged"`
}

func (h *Handler) writeBulkRequeue(c *gin.Context, location manualrequeue.Location, emptyMessage string) {
	result := h.manual.Requeue(c.Request.Context(), manualrequeue.Request{Location: location})
	if result.Err != nil {
		c.JSON(statusForManualError(result.Err), gin.H{"error": result.Err.Error()})
		return
	}
	if len(result.Accepted) == 0 && len(result.Failed) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": emptyMessage, "jobs": []string{}, "count": 0})
		return
	}

	jobs := make([]string, 0, len(result.Accepted))
	for _, accepted := range result.Accepted {
		jobs = append(jobs, accepted.JobID)
	}
	failures := make([]manualFailureResponse, 0, len(result.Failed))
	errorMessages := make([]string, 0, len(result.Failed))
	for _, failure := range result.Failed {
		failures = append(failures, manualFailureResponse{
			FileName: failure.FileName,
			Error:    failure.Err.Error(),
			Staged:   failure.Staged,
		})
		errorMessages = append(errorMessages, failure.Err.Error())
	}

	status := http.StatusAccepted
	if len(result.Failed) != 0 {
		if len(result.Accepted) != 0 {
			status = http.StatusMultiStatus
		} else {
			status = statusForManualError(result.Failed[0].Err)
		}
	}
	response := gin.H{
		"message": "manual requeue processed",
		"jobs":    jobs,
		"count":   len(jobs),
	}
	if len(failures) != 0 {
		response["failed"] = failures
		response["errors"] = errorMessages
	}
	c.JSON(status, response)
}

func (h *Handler) writeManagedFiles(c *gin.Context, location manualrequeue.Location) {
	files, err := h.manual.List(location)
	if err != nil {
		c.JSON(statusForManualError(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"folder": location,
		"files":  files,
		"count":  len(files),
	})
}

func statusForManualError(err error) int {
	switch {
	case errors.Is(err, manualrequeue.ErrInvalidLocation), errors.Is(err, manualrequeue.ErrInvalidName), errors.Is(err, manualrequeue.ErrInvalidMedia):
		return http.StatusBadRequest
	case errors.Is(err, manualrequeue.ErrConflict), errors.Is(err, queue.ErrDuplicateJobID):
		return http.StatusConflict
	case errors.Is(err, manualrequeue.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return http.StatusNotFound
	case errors.Is(err, queue.ErrQueueNotRunning), errors.Is(err, queue.ErrQueueStopping), errors.Is(err, queue.ErrQueueStopped), errors.Is(err, queue.ErrQueueFull):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
