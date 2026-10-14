package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/queue"
	"github.com/gin-gonic/gin"
)

func TestBulkTaskActionHTTPValidationEmptyResultAndUnavailableQueue(t *testing.T) {
	q := queue.New(noopProcessor{}, 1, 0)
	q.Start()
	defer q.Close()
	var reloads int
	h := New(q, func() error { reloads++; return nil })
	router := gin.New()
	h.RegisterRoutes(router)
	for _, body := range []string{
		`{`, `{}`, `null`, `[]`, `{"action":"retranslate"}`, `{"action":"cancel"}`,
		`{"action":"retry","output_name":"movie.mp4"}`, `{"action":"retry","status":"completed"}`,
		`{"action":"retry"} {}`, `{"action":"resume","use_current_settings":"yes"}`,
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/actions", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s response=%d %s", body, response.Code, response.Body.String())
		}
	}
	for _, body := range []string{`{"action":"retry"}`, `{"action":"resume","use_current_settings":true}`} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/actions", strings.NewReader(body)))
		var result queue.BulkTaskActionResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusAccepted || result.Matched != 0 || result.Accepted != 0 || result.Failed != 0 || result.Results == nil {
			t.Fatalf("empty result=%d %s", response.Code, response.Body.String())
		}
	}
	if reloads != 1 {
		t.Fatalf("config reloads=%d, want one only for current settings", reloads)
	}
	q.Stop()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/actions", strings.NewReader(`{"action":"retry"}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stopped queue=%d %s", response.Code, response.Body.String())
	}
}

func TestBulkTaskActionHTTPRejectsInvalidCurrentConfigBeforeRecovery(t *testing.T) {
	q := queue.New(noopProcessor{}, 1, 0)
	q.Start()
	defer q.Close()
	h := New(q, func() error { return errors.New("invalid request timeout") })
	router := gin.New()
	h.RegisterRoutes(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/actions", strings.NewReader(`{"action":"retry","use_current_settings":true}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "current configuration is invalid") {
		t.Fatalf("invalid config=%d %s", response.Code, response.Body.String())
	}
}

func TestBulkTaskActionHTTPRecoversFailedAndInterruptedSubmissions(t *testing.T) {
	for _, action := range []string{"retry", "resume"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			p := newWebhookTestProcessor(noopProcessor{}, handlerTestFolders(root))
			var recovered atomic.Bool
			p.prepare = func(ctx context.Context, _ *queue.Submission) (*mediaintake.ResolvedMedia, func() error, error) {
				if action == "retry" && !recovered.Load() {
					return nil, nil, errors.New("preparation failed")
				}
				<-ctx.Done()
				return nil, nil, ctx.Err()
			}
			path := filepath.Join(root, "state.db")
			q, err := queue.Open(path, p, 1, 0)
			if err != nil {
				t.Fatal(err)
			}
			q.Start()
			t.Cleanup(func() { _ = q.Close() })
			input, err := q.Submit(context.Background(), "/missing/movie.iso", "movie", "video")
			if err != nil {
				t.Fatal(err)
			}
			if action == "retry" {
				waitHandlerSubmission(t, q, input.ID, queue.SubmissionFailed)
			} else {
				waitHandlerSubmission(t, q, input.ID, queue.SubmissionPreparing)
				q.Stop()
				waitHandlerSubmission(t, q, input.ID, queue.SubmissionInterrupted)
				if err := q.Close(); err != nil {
					t.Fatal(err)
				}
				q, err = queue.Open(path, p, 1, 0)
				if err != nil {
					t.Fatal(err)
				}
				q.Start()
			}
			recovered.Store(true)
			var reloads int
			h := New(q, func() error { reloads++; return nil })
			router := gin.New()
			h.RegisterRoutes(router)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/actions", strings.NewReader(`{"action":"`+action+`","use_current_settings":true}`)))
			var result queue.BulkTaskActionResult
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusAccepted || reloads != 1 || result.Matched != 1 || result.Accepted != 1 || result.Failed != 0 || len(result.Results) != 1 || result.Results[0].Kind != "submission" || result.Results[0].ID != input.ID || result.Results[0].ExecutionID == input.RunID {
				t.Fatalf("bulk response=%d %s reloads=%d", response.Code, response.Body.String(), reloads)
			}
			if strings.Contains(response.Body.String(), "settings_snapshot") || strings.Contains(response.Body.String(), "history") {
				t.Fatalf("bulk response leaked heavy task detail: %s", response.Body.String())
			}
		})
	}
}
