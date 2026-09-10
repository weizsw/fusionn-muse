package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

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
	queue        *queue.Queue
	manual       *manualrequeue.Service
	folders      config.FoldersConfig
	reloadConfig func() error
}

// New creates a new Handler.
func New(q *queue.Queue, folders config.FoldersConfig, reloadConfig func() error) *Handler {
	return &Handler{
		queue:        q,
		manual:       manualrequeue.New(q, folders, nil),
		folders:      folders,
		reloadConfig: reloadConfig,
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
		api.GET("/jobs", h.ListJobs)
		api.GET("/jobs/events", h.JobEvents)
		api.GET("/jobs/:id", h.GetJobDetail)
		api.POST("/jobs/:id/retry", h.RetryJob)
		api.POST("/jobs/:id/resume", h.ResumeJob)
		api.POST("/jobs/:id/retranslate", h.RetranslateJob)

		// Retry endpoints
		api.POST("/retry/staging", h.RetryStaging)
		api.POST("/retry/failed", h.RetryFailed)
		api.POST("/retry/failed/:name", h.RetryOneFailed)

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
	if err := h.resolveAndDispatchTorrent(ctx, req); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "queue admission failed"})
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"message": "webhook accepted"})
}

func (h *Handler) resolveAndDispatchTorrent(ctx context.Context, req TorrentCompleteRequest) error {
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
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			log.Warnf("⚠️ Path does not exist for webhook: %s: %v", req.Path, err)
			return nil
		}
		log.Errorf("❌ Failed to resolve media for webhook path %s: %v", req.Path, err)
		return nil
	}

	jobID := uuid.NewString()
	ctx = logger.WithJob(ctx, jobID)
	log = logger.FromContext(ctx)
	fileName := resolved.FileName
	isLight := resolved.HasChineseSubtitle

	job := queue.NewJob(jobID, resolved.SourcePath, fileName, req.Name, req.Category)
	job.IsLight = isLight
	job.StagingPath = resolved.StagingPath
	if job.StagingPath == "" {
		job.StagingPath = filepath.Join(h.folders.Staging, fileName)
	}
	job.SubtitleDetectionReason = resolved.SubtitleDetectionReason
	job.SidecarSubtitlePath = resolved.SidecarSubtitlePath

	if err := h.queue.Accept(job); err != nil {
		var conflict *queue.ConflictError
		if errors.As(err, &conflict) {
			log.Infof("⏭️ Duplicate webhook ignored: job=%s status=%s", conflict.JobID, conflict.Status)
			return nil
		}
		log.Errorf("❌ Queue rejected job: %v", err)
		return fmt.Errorf("accept queue job: %w", err)
	}
	if isLight {
		log.Infof("⚡ Light job accepted (Chinese subtitle): %s", fileName)
	} else {
		log.Infof("📥 Heavy job accepted: %s", fileName)
	}
	return nil
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

func (h *Handler) ListJobs(c *gin.Context) {
	limit := 0
	if value := c.Query("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be a positive integer"})
			return
		}
	}
	jobs, next, err := h.queue.ListJobs(c.Query("cursor"), limit)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"jobs": jobs, "next_cursor": next})
}

// JobEvents streams persisted transitions for each Job's latest Attempt.
func (h *Handler) JobEvents(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	_, _ = fmt.Fprint(c.Writer, "retry: 2000\n\n")
	c.Writer.Flush()

	seen := make(map[int64]queue.AttemptStatus)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for _, job := range h.queue.GetAllJobs() {
			if job.AttemptID == 0 || seen[job.AttemptID] == job.AttemptStatus {
				continue
			}
			seen[job.AttemptID] = job.AttemptStatus
			c.SSEvent("attempt_status", gin.H{
				"job_id": job.ID, "attempt_id": job.AttemptID, "status": job.AttemptStatus,
				"stage": job.AttemptStage, "failure_stage": job.AttemptFailureStage, "error": job.AttemptError,
			})
			c.Writer.Flush()
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) GetJobDetail(c *gin.Context) {
	detail := h.queue.GetJobDetail(c.Param("id"))
	if detail == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	c.JSON(http.StatusOK, detail)
}

func (h *Handler) RetryJob(c *gin.Context) {
	id := c.Param("id")
	if path := h.retryStagingMatch(id); path != "" {
		h.writeJobAction(c, func(string) (*queue.Attempt, error) {
			return h.queue.RetryFromAttempt(id, path)
		})
		return
	}
	h.writeJobAction(c, h.queue.RetryAttempt)
}

func (h *Handler) retryStagingMatch(id string) string {
	job := h.queue.GetJob(id)
	if job == nil || job.Status != queue.StatusFailed || readableFile(recordedMediaPath(job)) {
		return ""
	}
	files, err := h.manual.List(manualrequeue.Staging)
	if err != nil {
		return ""
	}
	matches := make([]string, 0, 1)
	for _, name := range files {
		path := filepath.Join(h.folders.Staging, name)
		if readableFile(path) {
			if match := h.queue.FindJobByMedia(path, name); match != nil && match.ID == id {
				matches = append(matches, path)
			}
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

func recordedMediaPath(job *queue.Job) string {
	switch job.Checkpoint {
	case "":
		return job.SourcePath
	case queue.StagePrepared:
		return job.StagingPath
	default:
		return job.ProcessingPath
	}
}

func readableFile(path string) bool {
	if path == "" {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	return file.Close() == nil
}
func (h *Handler) ResumeJob(c *gin.Context) { h.writeJobAction(c, h.queue.ResumeAttempt) }
func (h *Handler) RetranslateJob(c *gin.Context) {
	if h.reloadConfig != nil {
		if err := h.reloadConfig(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	h.writeJobAction(c, h.queue.RetranslateAttempt)
}

func (h *Handler) writeJobAction(c *gin.Context, action func(string) (*queue.Attempt, error)) {
	id := c.Param("id")
	attempt, err := action(id)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, queue.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, queue.ErrQueueNotRunning), errors.Is(err, queue.ErrQueueStopping), errors.Is(err, queue.ErrQueueStopped):
			status = http.StatusServiceUnavailable
		case errors.Is(err, queue.ErrInvalidAction):
			job := h.queue.GetJob(id)
			actions := allowedJobActions(job)
			actionURL := ""
			if len(actions) > 0 {
				actionURL = "/api/v1/jobs/" + url.PathEscape(id) + "/" + actions[0]
			}
			c.JSON(http.StatusConflict, gin.H{"code": "invalid_job_action", "error": err.Error(), "job_id": id, "status": job.Status, "allowed_actions": actions, "action_url": actionURL})
			return
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"job": id, "attempt": attempt})
}

func allowedJobActions(job *queue.Job) []string {
	if job == nil {
		return nil
	}
	switch job.Status {
	case queue.StatusFailed:
		actions := []string{"retry"}
		if job.SubtitlePath != "" {
			actions = append(actions, "retranslate")
		}
		return actions
	case queue.StatusInterrupted:
		return []string{"resume"}
	case queue.StatusCompleted:
		if job.SubtitlePath != "" {
			return []string{"retranslate"}
		}
	}
	return []string{}
}

// RetryStaging re-queues untracked staging files and reports tracked duplicates as skipped.
func (h *Handler) RetryStaging(c *gin.Context) {
	h.writeBulkRequeue(c, manualrequeue.Staging, "no files in staging")
}

// RetryFailed reports failed-folder files without moving or implicitly retrying them.
func (h *Handler) RetryFailed(c *gin.Context) {
	h.writeBulkRequeue(c, manualrequeue.Failed, "no files in failed folder")
}

// RetryOneFailed reports one failed-folder file without moving or implicitly retrying it.
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
		c.JSON(statusForManualError(failure.Err), gin.H{"outcome": "failed", "file": failure.FileName, "error": failure.Err.Error(), "staged": failure.Staged})
		return
	}
	if len(result.Skipped) != 0 {
		skipped := result.Skipped[0]
		c.JSON(http.StatusOK, gin.H{"outcome": "skipped", "file": skipped.FileName, "job": skipped.JobID, "staged": skipped.Staged})
		return
	}
	accepted := result.Accepted[0]
	c.JSON(http.StatusAccepted, gin.H{"outcome": "accepted", "file": accepted.FileName, "job": accepted.JobID, "staged": accepted.Staged})
}

// ListStagingFiles returns all files in staging folder.
func (h *Handler) ListStagingFiles(c *gin.Context) {
	h.writeManagedFiles(c, manualrequeue.Staging)
}

// ListFailedFiles returns all files in failed folder.
func (h *Handler) ListFailedFiles(c *gin.Context) {
	h.writeManagedFiles(c, manualrequeue.Failed)
}

type manualOutcomeResponse struct {
	FileName string `json:"file"`
	JobID    string `json:"job,omitempty"`
	Staged   bool   `json:"staged"`
	Error    string `json:"error,omitempty"`
}

func (h *Handler) writeBulkRequeue(c *gin.Context, location manualrequeue.Location, emptyMessage string) {
	result := h.manual.Requeue(c.Request.Context(), manualrequeue.Request{Location: location})
	if result.Err != nil {
		c.JSON(statusForManualError(result.Err), gin.H{"error": result.Err.Error()})
		return
	}
	accepted := make([]manualOutcomeResponse, 0, len(result.Accepted))
	jobs := make([]string, 0, len(result.Accepted))
	for _, outcome := range result.Accepted {
		accepted = append(accepted, manualOutcomeResponse{FileName: outcome.FileName, JobID: outcome.JobID, Staged: outcome.Staged})
		jobs = append(jobs, outcome.JobID)
	}
	skipped := make([]manualOutcomeResponse, 0, len(result.Skipped))
	for _, outcome := range result.Skipped {
		skipped = append(skipped, manualOutcomeResponse{FileName: outcome.FileName, JobID: outcome.JobID, Staged: outcome.Staged})
	}
	failed := make([]manualOutcomeResponse, 0, len(result.Failed))
	for _, outcome := range result.Failed {
		failed = append(failed, manualOutcomeResponse{FileName: outcome.FileName, Staged: outcome.Staged, Error: outcome.Err.Error()})
	}
	status := http.StatusAccepted
	switch {
	case len(result.Failed) != 0 && (len(result.Accepted) != 0 || len(result.Skipped) != 0):
		status = http.StatusMultiStatus
	case len(result.Failed) != 0:
		status = statusForManualError(result.Failed[0].Err)
	case len(result.Accepted) == 0:
		status = http.StatusOK
	}
	message := "manual requeue processed"
	if len(result.Accepted) == 0 && len(result.Skipped) == 0 && len(result.Failed) == 0 {
		message = emptyMessage
	}
	c.JSON(status, gin.H{
		"message": message, "jobs": jobs, "count": len(jobs),
		"accepted": accepted, "skipped": skipped, "failed": failed,
	})
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
	case errors.Is(err, manualrequeue.ErrConflict), errors.Is(err, queue.ErrDuplicateJobID), errors.Is(err, queue.ErrDuplicateMedia):
		return http.StatusConflict
	case errors.Is(err, manualrequeue.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return http.StatusNotFound
	case errors.Is(err, queue.ErrQueueNotRunning), errors.Is(err, queue.ErrQueueStopping), errors.Is(err, queue.ErrQueueStopped), errors.Is(err, queue.ErrQueueFull):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
