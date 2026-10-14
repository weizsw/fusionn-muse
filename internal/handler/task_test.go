package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"strings"
	"testing"

	"github.com/fusionn-muse/internal/queue"
	"github.com/gin-gonic/gin"
)

func TestTaskCodeHTTPDetailAndActionReturnCanonicalID(t *testing.T) {
	root := t.TempDir()
	q := queue.New(noopProcessor{}, 1, 0)
	q.Start()
	defer q.Close()
	h := New(q, nil)
	router := gin.New()
	h.RegisterRoutes(router)
	job := queue.NewJob("canonical-job-id", "/missing/SNOS-411.mp4", "SNOS-411.mp4", "", "")
	job.SubtitlePath = filepath.Join(root, "source.srt")
	mustWriteSizedHandlerFile(t, job.SubtitlePath, 1)
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for q.GetJob(job.ID).Status != queue.StatusCompleted {
		if time.Now().After(deadline) {
			t.Fatal("job did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/job/snos-411", nil))
	var detail queue.TaskDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || detail.ID != job.ID || len(detail.AllowedActions) != 1 || detail.AllowedActions[0] != "retranslate" {
		t.Fatalf("detail=%d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/job/SNOS-411/actions", strings.NewReader(`{"action":"retranslate"}`)))
	var result queue.TaskActionResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusAccepted || result.ID != job.ID || result.Attempt == nil || result.Attempt.JobID != job.ID || response.Header().Get("Location") != "/api/v1/tasks/job/"+job.ID {
		t.Fatalf("action=%d %s Location=%s", response.Code, response.Body.String(), response.Header().Get("Location"))
	}
}

func TestTaskCodeConflictReturnsCandidatesWithoutStartingExecution(t *testing.T) {
	root := t.TempDir()
	q := queue.New(noopProcessor{}, 1, 0)
	q.Start()
	defer q.Close()
	h := New(q, nil)
	router := gin.New()
	h.RegisterRoutes(router)
	for _, item := range []struct{ id, name string }{{"first-job", "SNOS-411.mp4"}, {"second-job", "SNOS-411.mkv"}} {
		source := filepath.Join(root, item.id)
		if err := os.WriteFile(source, []byte(item.id), 0644); err != nil {
			t.Fatal(err)
		}
		if err := q.Accept(queue.NewJob(item.id, source, item.name, "", "")); err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/api/v1/tasks/job/snos-411"
		if method == http.MethodPost {
			path += "/actions"
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(`{"action":"retranslate"}`)))
		var conflict struct {
			Code       string `json:"code"`
			Reference  string `json:"reference"`
			Candidates []struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &conflict); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusConflict || conflict.Code != "ambiguous_job_reference" || conflict.Reference != "SNOS-411" || len(conflict.Candidates) != 2 {
			t.Fatalf("%s conflict=%d %s", method, response.Code, response.Body.String())
		}
		if conflict.Candidates[0].ID != "first-job" || conflict.Candidates[1].ID != "second-job" || conflict.Candidates[0].Kind != "job" || strings.Contains(response.Body.String(), "settings_snapshot") {
			t.Fatalf("candidates=%s", response.Body.String())
		}
	}
	for _, id := range []string{"first-job", "second-job"} {
		detail, err := q.GetTaskDetail(context.Background(), "job", id, "", 50)
		if err != nil || len(detail.History) != 1 {
			t.Fatalf("ambiguous action changed history=%+v error=%v", detail, err)
		}
	}
}

func TestTaskHTTPReadsAndStorageFailure(t *testing.T) {
	h := newTestHandler(t.TempDir())
	h.queue.Start()
	router := gin.New()
	h.RegisterRoutes(router)
	defer h.queue.Close()
	for _, path := range []string{"/api/v1/tasks", "/api/v1/tasks/stats"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s=%d %s", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "settings_snapshot") {
			t.Fatalf("list leaked settings: %s", response.Body.String())
		}
	}
	for _, path := range []string{"/api/v1/tasks?status=resolved", "/api/v1/tasks?kind=unknown", "/api/v1/tasks?limit=0", "/api/v1/tasks?limit=bad", "/api/v1/tasks?cursor=bad", "/api/v1/tasks/stats?kind=unknown", "/api/v1/tasks/stats?status=resolved", "/api/v1/tasks/job/absent?history_cursor=bad"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		want := http.StatusBadRequest
		if strings.Contains(path, "/absent?") {
			want = http.StatusNotFound
		}
		if response.Code != want {
			t.Fatalf("GET %s=%d %s, want %d", path, response.Code, response.Body.String(), want)
		}
	}
	if err := h.queue.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/tasks", "/api/v1/tasks/stats", "/api/v1/tasks/job/absent", "/api/v1/tasks/job/SNOS-411", "/api/v1/tasks/submission/absent"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("closed storage GET %s=%d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestWebhookLocationUsesUnifiedTaskDetail(t *testing.T) {
	h := newTestHandler(t.TempDir())
	h.queue.Start()
	defer h.queue.Close()
	response := postTorrentComplete(t, h, `{"path":"/missing.iso","name":"missing"}`)
	var receipt struct {
		ID string `json:"submission_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusAccepted || response.Header().Get("Location") != "/api/v1/tasks/submission/"+receipt.ID {
		t.Fatalf("receipt=%d %s Location=%s", response.Code, response.Body.String(), response.Header().Get("Location"))
	}
	response = httptest.NewRecorder()
	router := gin.New()
	h.RegisterRoutes(router)
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/submission/"+receipt.ID, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"submission"`) {
		t.Fatalf("receipt detail=%d %s", response.Code, response.Body.String())
	}
}

func TestTaskActionHTTPValidationAndSuggestions(t *testing.T) {
	h := newTestHandler(t.TempDir())
	h.queue.Start()
	defer h.queue.Close()
	router := gin.New()
	h.RegisterRoutes(router)
	for _, item := range []struct {
		kind, id, body string
		status         int
	}{
		{"job", "missing", `{"action":"retry"}`, 404},
		{"submission", "missing", `{"action":"resume"}`, 404},
		{"job", "missing", `{`, 400},
		{"job", "missing", `{}`, 400},
		{"job", "missing", `{"action":"retry","path":"/injected"}`, 400},
		{"job", "missing", `{"action":"retry"} {}`, 400},
		{"submission", "missing", `{"action":"retranslate"}`, 400},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/"+item.kind+"/"+item.id+"/actions", strings.NewReader(item.body)))
		if response.Code != item.status {
			t.Fatalf("%s/%s %s=%d %s", item.kind, item.id, item.body, response.Code, response.Body.String())
		}
	}
	job := queue.NewJob("completed", "/missing/movie.mp4", "movie.mp4", "", "")
	job.SubtitlePath = filepath.Join(t.TempDir(), "source.srt")
	mustWriteSizedHandlerFile(t, job.SubtitlePath, 1)
	if err := h.queue.Accept(job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for h.queue.GetJob(job.ID).Status != queue.StatusCompleted {
		if time.Now().After(deadline) {
			t.Fatal("job did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/job/completed/actions", strings.NewReader(`{"action":"retry"}`)))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"allowed_actions":["retranslate"]`) {
		t.Fatalf("suggestion=%d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/job/completed/actions", strings.NewReader(`{"action":"retranslate"}`)))
	if response.Code != http.StatusAccepted || response.Header().Get("Location") != "/api/v1/tasks/job/completed" {
		t.Fatalf("accepted=%d %s", response.Code, response.Body.String())
	}
}

func TestOnlyUnifiedOperatorRoutesRemain(t *testing.T) {
	h := newTestHandler(t.TempDir())
	defer h.queue.Close()
	router := gin.New()
	h.RegisterRoutes(router)
	allowed := map[string]bool{
		"GET /api/v1/health": true, "GET /api/v1/version": true, "POST /api/v1/webhook/torrent": true,
		"GET /api/v1/tasks": true, "GET /api/v1/tasks/stats": true, "GET /api/v1/tasks/:kind/:id": true,
		"POST /api/v1/tasks/:kind/:id/actions": true, "POST /api/v1/tasks/actions": true,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if !allowed[key] {
			t.Fatalf("old or unexpected route remains: %s", key)
		}
		delete(allowed, key)
	}
	if len(allowed) != 0 {
		t.Fatalf("missing routes: %v", allowed)
	}
}

func TestOnlyUnifiedTaskRoutesAreRegistered(t *testing.T) {
	h := newTestHandler(t.TempDir())
	defer h.queue.Close()
	router := gin.New()
	h.RegisterRoutes(router)
	wanted := map[string]bool{
		"GET /api/v1/health": true, "GET /api/v1/version": true, "POST /api/v1/webhook/torrent": true,
		"GET /api/v1/tasks": true, "GET /api/v1/tasks/stats": true, "GET /api/v1/tasks/:kind/:id": true,
		"POST /api/v1/tasks/:kind/:id/actions": true, "POST /api/v1/tasks/actions": true,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if !wanted[key] {
			t.Fatalf("unexpected route %s", key)
		}
		delete(wanted, key)
	}
	if len(wanted) != 0 {
		t.Fatalf("missing routes %v", wanted)
	}
	for _, old := range []string{"/queue", "/queue/stats", "/queue/id", "/jobs", "/jobs/events", "/jobs/id", "/submissions", "/submissions/id", "/files/failed", "/files/staging", "/retry/staging", "/retry/failed", "/retry/failed/movie.mp4", "/jobs/id/retry", "/jobs/id/resume", "/jobs/id/retranslate", "/submissions/id/retry"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(method, "/api/v1"+old, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("old %s %s = %d", method, old, response.Code)
			}
		}
	}
}
