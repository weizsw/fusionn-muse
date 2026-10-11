package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/fusionn-muse/internal/queue"
	"github.com/gin-gonic/gin"
)

func taskPageLimit(c *gin.Context) (int, bool) {
	limit := 50
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"code": "invalid_task_query", "error": "limit must be a positive integer"})
			return 0, false
		}
		limit = min(parsed, 200)
	}
	return limit, true
}

func (h *Handler) ListTasks(c *gin.Context) {
	limit, ok := taskPageLimit(c)
	if !ok {
		return
	}
	page, err := h.queue.ListTasks(c.Request.Context(), queue.TaskFilter{Kind: c.Query("kind"), Status: c.Query("status"), Cursor: c.Query("cursor"), Limit: limit})
	if err != nil {
		taskReadError(c, err)
		return
	}
	c.JSON(http.StatusOK, page)
}

func (h *Handler) TaskStats(c *gin.Context) {
	stats, err := h.queue.TaskStats(c.Request.Context(), queue.TaskFilter{Kind: c.Query("kind"), Status: c.Query("status")})
	if err != nil {
		taskReadError(c, err)
		return
	}
	c.JSON(http.StatusOK, stats)
}

func (h *Handler) TaskDetail(c *gin.Context) {
	limit, ok := taskPageLimit(c)
	if !ok {
		return
	}
	detail, err := h.queue.GetTaskDetail(c.Request.Context(), c.Param("kind"), c.Param("id"), c.Query("history_cursor"), limit)
	if err != nil {
		taskReadError(c, err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

func writeJobReferenceConflict(c *gin.Context, err error) bool {
	var conflict *queue.AmbiguousJobReferenceError
	if !errors.As(err, &conflict) {
		return false
	}
	c.JSON(http.StatusConflict, gin.H{"code": "ambiguous_job_reference", "error": err.Error(), "reference": conflict.Reference, "candidates": conflict.Candidates})
	return true
}

func taskReadError(c *gin.Context, err error) {
	if writeJobReferenceConflict(c, err) {
		return
	}
	status, code := http.StatusServiceUnavailable, "state_unavailable"
	if errors.Is(err, queue.ErrInvalidTaskQuery) {
		status, code = http.StatusBadRequest, "invalid_task_query"
	} else if errors.Is(err, queue.ErrNotFound) {
		status, code = http.StatusNotFound, "task_not_found"
	}
	c.JSON(status, gin.H{"code": code, "error": err.Error()})
}

// TaskAction starts one explicit execution; it does not edit submission input.
func (h *Handler) TaskAction(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSubmissionBodyBytes)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var request queue.TaskAction
	if err := decoder.Decode(&request); err != nil {
		writeSubmissionInputError(c, err)
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeSubmissionInputError(c, fmt.Errorf("exactly one JSON object is required"))
		return
	}
	if request.Action != "retry" && request.Action != "resume" && request.Action != "retranslate" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "action must be retry, resume or retranslate"})
		return
	}
	if request.UseCurrentSettings && h.reloadConfig != nil {
		if err := h.reloadConfig(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "current configuration is invalid", "detail": err.Error()})
			return
		}
	}
	kind, id := c.Param("kind"), c.Param("id")
	result, err := h.queue.ExecuteTaskAction(c.Request.Context(), kind, id, request)
	if err != nil {
		if writeJobReferenceConflict(c, err) {
			return
		}
		status := http.StatusServiceUnavailable
		body := gin.H{"error": err.Error()}
		switch {
		case errors.Is(err, queue.ErrNotFound), errors.Is(err, queue.ErrSubmissionNotFound):
			status = http.StatusNotFound
		case errors.Is(err, queue.ErrInvalidTaskAction):
			status = http.StatusBadRequest
		case errors.Is(err, queue.ErrInvalidAction), errors.Is(err, queue.ErrInvalidSubmissionAction), errors.Is(err, queue.ErrArtifactConflict), errors.Is(err, queue.ErrArtifactUnavailable):
			status = http.StatusConflict
			if detail, readErr := h.queue.GetTaskDetail(c.Request.Context(), kind, id, "", 1); readErr == nil {
				body["allowed_actions"] = detail.AllowedActions
				body["can_change_output_name"] = detail.CanChangeOutputName
				if detail.CanonicalTask != nil {
					body["canonical_task"] = detail.CanonicalTask
				}
			}
		}
		c.JSON(status, body)
		return
	}
	c.Header("Location", "/api/v1/tasks/"+result.Kind+"/"+result.ID)
	c.JSON(http.StatusAccepted, result)
}
