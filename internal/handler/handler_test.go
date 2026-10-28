package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/pkg/logger"
)

type noopProcessor struct{}

func (noopProcessor) Process(context.Context, *queue.Job) error {
	return nil
}

type recordingProcessor struct {
	called chan *queue.Job
	once   sync.Once
}

func newRecordingProcessor() *recordingProcessor {
	return &recordingProcessor{called: make(chan *queue.Job, 1)}
}

func (p *recordingProcessor) Process(_ context.Context, job *queue.Job) error {
	p.once.Do(func() {
		snapshot := *job
		p.called <- &snapshot
	})
	return nil
}

func init() {
	logger.Init(true)
	gin.SetMode(gin.TestMode)
}

func TestTorrentCompleteAcknowledgesAfterSubmissionPersistence(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "downloads", "SSNI-083-C.mp4")
	mustWriteSizedHandlerFile(t, source, mediaintake.MinVideoSize+1)

	handler := newTestHandler(root)
	handler.queue.Start()
	defer handler.queue.Stop()

	response := postTorrentComplete(t, handler, `{"path":"`+source+`","name":"SSNI-083"}`)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := body["job"]; ok {
		t.Fatalf("receipt contains premature job field: %s", response.Body.String())
	}

	jobs := waitForAcceptedJobs(t, handler.queue, 1)
	if _, err := uuid.Parse(jobs[0].ID); err != nil {
		t.Fatalf("accepted Job ID = %q, want full UUID: %v", jobs[0].ID, err)
	}
}

func TestTorrentCompleteLogsTheRequestAndSubmissionIDs(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "downloads", "SSNI-083-C.mp4")
	mustWriteSizedHandlerFile(t, source, mediaintake.MinVideoSize+1)

	handler := newTestHandler(root)
	handler.queue.Start()
	defer handler.queue.Stop()

	router := newMiddlewareRouter(func(r *gin.Engine) { handler.RegisterRoutes(r) })

	var buf bytes.Buffer
	t.Setenv("LOG_LEVEL", "INFO")
	logger.Configure(logger.Config{Output: zapcore.AddSync(&buf)})

	request := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/torrent",
		bytes.NewBufferString(`{"path":"`+source+`","name":"SSNI-083"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(RequestIDHeader, "webhook-request-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", response.Code, response.Body.String())
	}
	var body struct {
		SubmissionID string `json:"submission_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.SubmissionID == "" {
		t.Fatalf("response carries no submission_id: %s", response.Body.String())
	}

	// The queue worker still writes, so it must be stopped before the capture is
	// read; the record under test was written before the response.
	handler.queue.Stop()
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	records := linesContaining(lines, "event=submission_received")
	if len(records) != 1 {
		t.Fatalf("submission_received lines = %d, want one: %q", len(records), lines)
	}
	for _, want := range []string{" INFO ", "submission_id=" + body.SubmissionID, "request_id=webhook-request-1"} {
		if !strings.Contains(records[0], want) {
			t.Fatalf("record = %q, want %q", records[0], want)
		}
	}
}

func TestTorrentCompleteDoesNotAcknowledgeQueueRejection(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "downloads", "SSNI-083-C.mp4")
	mustWriteSizedHandlerFile(t, source, mediaintake.MinVideoSize+1)
	handler := newTestHandler(root)

	response := postTorrentComplete(t, handler, `{"path":"`+source+`","name":"SSNI-083"}`)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
}

func TestTorrentCompleteReturnsAcceptedForNoValidMedia(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	mustWriteSizedHandlerFile(t, filepath.Join(folder, "movie.mp4"), mediaintake.MinVideoSize+1)
	handler := newTestHandler(root)
	handler.queue.Start()
	defer handler.queue.Close()

	response := postTorrentComplete(t, handler, `{"path":"`+folder+`","name":"no code here"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "webhook accepted") {
		t.Fatalf("body = %s, want accepted response", response.Body.String())
	}
}

func TestTorrentCompleteReturnsAcceptedForMediaPreparationFailure(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "media-extract", "SSNI-083-image", "disc.iso")
	mustWriteSizedHandlerFile(t, image, 1024)
	handler := newTestHandler(root)
	p := newWebhookTestProcessor(noopProcessor{}, handlerTestFolders(root))
	p.prepare = func(context.Context, *queue.Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		return nil, nil, errors.New("preparation failed")
	}
	handler.queue = queue.New(p, 1, 0)
	handler.queue.Start()
	defer handler.queue.Close()

	response := postTorrentComplete(t, handler, `{"path":"`+image+`","name":"SSNI-083"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "webhook accepted") {
		t.Fatalf("body = %s, want accepted response", response.Body.String())
	}
}

func TestLightTorrentUsesProcessorLifecycle(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "downloads", "SSNI-083-C.mp4")
	mustWriteSizedHandlerFile(t, source, mediaintake.MinVideoSize+1)

	proc := newRecordingProcessor()
	handler := newTestHandler(root)
	handler.queue = queue.New(newWebhookTestProcessor(proc, handlerTestFolders(root)), 1, 0)
	handler.queue.Start()
	defer handler.queue.Stop()

	response := postTorrentComplete(t, handler, `{"path":"`+source+`","name":"SSNI-083"}`)
	submissionReceiptID(t, response)

	select {
	case job := <-proc.called:
		if !job.IsLight {
			t.Fatal("job.IsLight = false, want true")
		}
		if _, err := uuid.Parse(job.ID); err != nil {
			t.Fatalf("job.ID = %q, want full UUID: %v", job.ID, err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("light job did not use processor lifecycle")
	}
}

func newTestHandler(root string) *Handler {
	q := queue.New(newWebhookTestProcessor(noopProcessor{}, handlerTestFolders(root)), 1, 0)
	return New(q, nil)
}

func handlerTestFolders(root string) config.FoldersConfig {
	return config.FoldersConfig{
		Staging: filepath.Join(root, "staging"), Process: filepath.Join(root, "processing"),
		Scraping: filepath.Join(root, "scraping"), Failed: filepath.Join(root, "failed"),
	}
}

func waitForAcceptedJobs(t *testing.T, q *queue.Queue, want int) []*queue.Job {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if jobs := q.GetAllJobs(); len(jobs) == want {
			return jobs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("accepted jobs = %d, want %d", len(q.GetAllJobs()), want)
	return nil
}

func postTorrentComplete(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhook/torrent", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = req
	h.TorrentComplete(c)
	return response
}

func mustWriteSizedHandlerFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatalf("truncate %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}
