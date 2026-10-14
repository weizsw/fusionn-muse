package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/fusionn-muse/internal/queue"
	"github.com/gin-gonic/gin"
)

// BulkTaskAction admits retry or resume for all tasks in the matching state.
func (h *Handler) BulkTaskAction(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSubmissionBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var request queue.BulkTaskAction
	if err := decoder.Decode(&request); err != nil {
		writeSubmissionInputError(c, err)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeSubmissionInputError(c, fmt.Errorf("exactly one JSON object is required"))
		return
	}
	if request.Action != "retry" && request.Action != "resume" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bulk action must be retry or resume"})
		return
	}
	if request.UseCurrentSettings && h.reloadConfig != nil {
		if err := h.reloadConfig(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "current configuration is invalid", "detail": err.Error()})
			return
		}
	}
	result, err := h.queue.ExecuteBulkTaskAction(c.Request.Context(), request)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, result)
}
