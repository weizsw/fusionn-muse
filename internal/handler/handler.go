package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/internal/version"
	"github.com/fusionn-muse/pkg/logger"
)

// Handler handles HTTP requests.
type Handler struct {
	queue        *queue.Queue
	reloadConfig func() error
}

// New creates a new Handler.
func New(q *queue.Queue, reloadConfig func() error) *Handler {
	return &Handler{
		queue:        q,
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
		api.GET("/tasks", h.ListTasks)
		api.GET("/tasks/stats", h.TaskStats)
		api.POST("/tasks/actions", h.BulkTaskAction)
		api.GET("/tasks/:kind/:id", h.TaskDetail)
		api.POST("/tasks/:kind/:id/actions", h.TaskAction)
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
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSubmissionBodyBytes)
	var req TorrentCompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeSubmissionInputError(c, err)
		return
	}
	ctx := c.Request.Context()
	logger.Component("http").Info(ctx, "webhook_received", "Torrent completion received",
		zap.String("path", req.Path))
	submission, err := h.queue.Submit(ctx, req.Path, req.Name, req.Category)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, queue.ErrInvalidSubmission) {
			status = http.StatusBadRequest
			logger.Component("http").Warn(ctx, "submission_rejected", "Submission was rejected",
				zap.Error(err), zap.Int("status", status))
		} else {
			logger.Component("http").Error(ctx, "submission_rejected", "Submission admission failed",
				zap.Error(err), zap.Int("status", status))
			markErrorReported(c)
		}
		c.JSON(status, gin.H{"error": "submission admission failed"})
		return
	}
	// The request that carried the webhook and the Submission it created are one
	// chain: keep the request id and attach the Submission id to it.
	logger.Component("http").Info(logger.WithSubmissionID(ctx, submission.ID), "submission_received",
		"Submission accepted", zap.String("path", req.Path))
	c.Header("Location", "/api/v1/tasks/submission/"+submission.ID)
	c.JSON(http.StatusAccepted, gin.H{"message": "webhook accepted", "submission_id": submission.ID, "status": submission.Status})
}
