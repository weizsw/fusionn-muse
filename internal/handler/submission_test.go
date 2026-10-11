package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/internal/service/processor"
	"github.com/gin-gonic/gin"
)

type webhookTestProcessor struct {
	queue.Processor
	preparer      *processor.Service
	prepare       func(context.Context, *queue.Submission) (*mediaintake.ResolvedMedia, func() error, error)
	snapshotError error
}

func newWebhookTestProcessor(p queue.Processor, folders config.FoldersConfig) *webhookTestProcessor {
	return &webhookTestProcessor{Processor: p, preparer: processor.New(nil, nil, folders, nil)}
}
func (p *webhookTestProcessor) PrepareSubmission(ctx context.Context, input *queue.Submission) (*mediaintake.ResolvedMedia, func() error, error) {
	if p.prepare != nil {
		return p.prepare(ctx, input)
	}
	return p.preparer.PrepareSubmission(ctx, input)
}
func (p *webhookTestProcessor) SnapshotSettings() (string, error) {
	return `{"model":"frozen"}`, p.snapshotError
}

func submissionReceiptID(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var receipt struct {
		ID string `json:"submission_id"`
	}
	if response.Code != http.StatusAccepted {
		t.Fatalf("receipt = %d %s", response.Code, response.Body)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil || receipt.ID == "" {
		t.Fatalf("missing durable receipt: %s, %v", response.Body, err)
	}
	return receipt.ID
}

func waitHandlerSubmission(t *testing.T, q *queue.Queue, id string, status queue.SubmissionStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		input, err := q.GetSubmission(id)
		if err == nil && input.Status == status {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("submission %s did not reach %s", id, status)
}

func TestWebhookPersistsBeforePreparingAndExposesLightListAndDetail(t *testing.T) {
	root := t.TempDir()
	h := newTestHandler(root)
	p := newWebhookTestProcessor(noopProcessor{}, handlerTestFolders(root))
	started, release := make(chan struct{}, 1), make(chan struct{})
	p.prepare = func(ctx context.Context, _ *queue.Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-release:
			return nil, nil, errors.New("image preparation failed")
		}
	}
	h.queue = queue.New(p, 1, 0)
	h.queue.Start()
	defer h.queue.Close()
	response := postTorrentComplete(t, h, `{"path":"/downloads/movie.iso","name":"movie"}`)
	id := submissionReceiptID(t, response)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("preparation never started")
	}
	r := gin.New()
	h.RegisterRoutes(r)
	list := httptest.NewRecorder()
	r.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/tasks?kind=submission&limit=1", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), id) || strings.Contains(list.Body.String(), "settings_snapshot") || strings.Contains(list.Body.String(), `"runs"`) {
		t.Fatalf("list = %d %s", list.Code, list.Body)
	}
	close(release)
	waitHandlerSubmission(t, h.queue, id, queue.SubmissionFailed)
	detail := httptest.NewRecorder()
	r.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/submission/"+id, nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "image preparation failed") || !strings.Contains(detail.Body.String(), "settings_snapshot") {
		t.Fatalf("detail = %d %s", detail.Code, detail.Body)
	}
	retry := httptest.NewRecorder()
	r.ServeHTTP(retry, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/submission/"+id+"/actions", strings.NewReader(`{"action":"retry"}`)))
	if retry.Code != http.StatusAccepted {
		t.Fatalf("retry = %d %s", retry.Code, retry.Body)
	}
}

func TestWebhookDoesNotAcknowledgeFailedSettingsCapture(t *testing.T) {
	root := t.TempDir()
	h := newTestHandler(root)
	p := newWebhookTestProcessor(noopProcessor{}, handlerTestFolders(root))
	p.snapshotError = errors.New("settings unavailable")
	h.queue = queue.New(p, 1, 0)
	h.queue.Start()
	defer h.queue.Close()
	response := postTorrentComplete(t, h, `{"path":"/missing/movie.mp4"}`)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "submission_id") {
		t.Fatalf("false acknowledgement: %d %s", response.Code, response.Body)
	}
}

func TestSubmissionPaginationFilteringAndQueryErrors(t *testing.T) {
	root := t.TempDir()
	h := newTestHandler(root)
	p := newWebhookTestProcessor(noopProcessor{}, handlerTestFolders(root))
	p.prepare = func(context.Context, *queue.Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		return nil, nil, errors.New("input unavailable")
	}
	h.queue = queue.New(p, 1, 0)
	h.queue.Start()
	defer h.queue.Close()
	ids := make(map[string]bool)
	for range 3 {
		id := submissionReceiptID(t, postTorrentComplete(t, h, `{"path":"/missing/video"}`))
		waitHandlerSubmission(t, h.queue, id, queue.SubmissionFailed)
		ids[id] = true
	}
	r := gin.New()
	h.RegisterRoutes(r)
	cursor := ""
	seen := map[string]bool{}
	for range 3 {
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks?kind=submission&limit=1&status=failed&cursor="+cursor, nil))
		var page struct {
			Submissions []queue.Submission `json:"tasks"`
			Next        string             `json:"next_cursor"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK || len(page.Submissions) != 1 {
			t.Fatalf("page = %d %s, %v", response.Code, response.Body, err)
		}
		id := page.Submissions[0].ID
		if seen[id] || !ids[id] {
			t.Fatalf("duplicate or unknown entry: %s", id)
		}
		seen[id] = true
		cursor = page.Next
	}
	if cursor != "" {
		t.Fatal("unexpected trailing page")
	}
	for _, query := range []string{"status=unknown", "limit=-1", "limit=word", "cursor=!"} {
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks?kind=submission&"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", query, response.Code, response.Body)
		}
	}
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/tasks?kind=submission&status=ignored", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"tasks":[]`) {
		t.Fatalf("empty = %d %s", response.Code, response.Body)
	}
}

func TestSubmissionRecoveryUsesCurrentSettingsOnlyWhenRequested(t *testing.T) {
	root := t.TempDir()
	h := newTestHandler(root)
	p := newWebhookTestProcessor(noopProcessor{}, handlerTestFolders(root))
	p.prepare = func(context.Context, *queue.Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		return nil, nil, errors.New("unavailable")
	}
	h.queue = queue.New(p, 1, 0)
	h.queue.Start()
	defer h.queue.Close()
	id := submissionReceiptID(t, postTorrentComplete(t, h, `{"path":"/missing/video"}`))
	waitHandlerSubmission(t, h.queue, id, queue.SubmissionFailed)
	reloads := 0
	h.reloadConfig = func() error { reloads++; return errors.New("bad current config") }
	r := gin.New()
	h.RegisterRoutes(r)
	bad := httptest.NewRecorder()
	r.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/submission/"+id+"/actions", strings.NewReader(`{"action":"retry","use_current_settings":true}`)))
	if bad.Code != http.StatusBadRequest || reloads != 1 {
		t.Fatalf("current config = %d %s, reloads=%d", bad.Code, bad.Body, reloads)
	}
	good := httptest.NewRecorder()
	r.ServeHTTP(good, httptest.NewRequest(http.MethodPost, "/api/v1/tasks/submission/"+id+"/actions", strings.NewReader(`{"action":"retry"}`)))
	if good.Code != http.StatusAccepted || reloads != 1 {
		t.Fatalf("original config = %d %s, reloads=%d", good.Code, good.Body, reloads)
	}
}

func TestSubmissionMalformedInputAndMissingRecords(t *testing.T) {
	root := t.TempDir()
	h := newTestHandler(root)
	h.queue.Start()
	defer h.queue.Close()
	r := gin.New()
	h.RegisterRoutes(r)
	for _, item := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, "/api/v1/webhook/torrent", `{`, http.StatusBadRequest},
		{http.MethodPost, "/api/v1/webhook/torrent", `{}`, http.StatusBadRequest},
		{http.MethodPost, "/api/v1/webhook/torrent", `{"path":" "}`, http.StatusBadRequest},
		{http.MethodGet, "/api/v1/tasks/submission/missing", "", http.StatusNotFound},
		{http.MethodPost, "/api/v1/tasks/submission/missing/actions", `{"action":"retry"}`, http.StatusNotFound},
		{http.MethodPost, "/api/v1/tasks/submission/missing/actions", `{`, http.StatusBadRequest},
		{http.MethodPost, "/api/v1/webhook/torrent", `{"path":"` + strings.Repeat("a", maxSubmissionBodyBytes) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(item.method, item.path, strings.NewReader(item.body))
		request.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(response, request)
		if response.Code != item.status {
			t.Fatalf("%s %s = %d %s, want %d", item.method, item.path, response.Code, response.Body, item.status)
		}
	}
}
