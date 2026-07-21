package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/manualrequeue"
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
		p.called <- job
	})
	return nil
}

func init() {
	logger.Init(true)
	gin.SetMode(gin.TestMode)
}

func TestTorrentCompleteReceiptOmitsJobUntilQueueAcceptance(t *testing.T) {
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

func TestManualRequeuesCreateNewFullJobIDs(t *testing.T) {
	root := t.TempDir()
	folders := config.FoldersConfig{
		Staging: filepath.Join(root, "staging"),
		Failed:  filepath.Join(root, "failed"),
	}
	q := queue.New(noopProcessor{}, 1, 0)
	h := &Handler{
		queue:   q,
		manual:  manualrequeue.New(q, folders, nil),
		folders: folders,
	}
	h.queue.Start()
	defer h.queue.Stop()

	ids := make(map[string]bool)
	for _, fileName := range []string{"movie-a-C.mp4", "movie-b-C.mp4"} {
		mustWriteSizedHandlerFile(t, filepath.Join(folders.Failed, fileName), 1)
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/retry/failed/"+fileName, nil)
		c.Params = gin.Params{{Key: "name", Value: fileName}}
		h.RetryOneFailed(c)

		var body struct {
			Job string `json:"job"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if _, err := uuid.Parse(body.Job); err != nil {
			t.Fatalf("job ID = %q, want full UUID: %v", body.Job, err)
		}
		if ids[body.Job] {
			t.Fatalf("duplicate manual requeue Job ID %q", body.Job)
		}
		ids[body.Job] = true
	}
}

func TestManualRequeueStagingReportsQueueRejection(t *testing.T) {
	root := t.TempDir()
	folders := config.FoldersConfig{Staging: filepath.Join(root, "staging")}
	mustWriteSizedHandlerFile(t, filepath.Join(folders.Staging, "movie-C.mp4"), mediaintake.MinVideoSize+1)
	q := queue.New(noopProcessor{}, 1, 0)
	h := &Handler{
		queue:   q,
		manual:  manualrequeue.New(q, folders, nil),
		folders: folders,
	}

	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/retry/staging", nil)
	h.RetryStaging(c)

	var body struct {
		Count  int      `json:"count"`
		Jobs   []string `json:"jobs"`
		Failed []struct {
			Staged bool `json:"staged"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusServiceUnavailable || body.Count != 0 || len(body.Jobs) != 0 || len(body.Failed) != 1 || !body.Failed[0].Staged {
		t.Fatalf("status/body = %d %+v, want staged queue rejection without accepted Jobs", response.Code, body)
	}
}

func TestTorrentCompleteReturnsAcceptedForNoValidMedia(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	mustWriteSizedHandlerFile(t, filepath.Join(folder, "movie.mp4"), mediaintake.MinVideoSize+1)
	handler := newTestHandler(root)

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
	handler.queue = queue.New(proc, 1, 0)
	handler.queue.Start()
	defer handler.queue.Stop()

	handler.resolveAndDispatchTorrent(context.Background(), TorrentCompleteRequest{
		Path: source,
		Name: "SSNI-083",
	})

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

type rejectingAccepter struct {
	reject map[string]error
}

func (a rejectingAccepter) Accept(job *queue.Job) error {
	return a.reject[job.FileName]
}

func TestManualRequeueOneFailedDistinguishesHTTPFailures(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		prepare  func(t *testing.T, folders config.FoldersConfig)
		wantCode int
		staged   bool
	}{
		{name: "invalid input", fileName: "..", wantCode: http.StatusBadRequest},
		{name: "missing media", fileName: "missing-C.mp4", wantCode: http.StatusNotFound},
		{
			name:     "conflict",
			fileName: "movie-C.mp4",
			prepare: func(t *testing.T, folders config.FoldersConfig) {
				mustWriteSizedHandlerFile(t, filepath.Join(folders.Failed, "movie-C.mp4"), 1)
				mustWriteSizedHandlerFile(t, filepath.Join(folders.Staging, "movie-C.mp4"), 1)
			},
			wantCode: http.StatusConflict,
		},
		{
			name:     "unavailable queue",
			fileName: "movie-C.mp4",
			prepare: func(t *testing.T, folders config.FoldersConfig) {
				mustWriteSizedHandlerFile(t, filepath.Join(folders.Failed, "movie-C.mp4"), 1)
			},
			wantCode: http.StatusServiceUnavailable,
			staged:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			folders := config.FoldersConfig{Staging: filepath.Join(root, "staging"), Failed: filepath.Join(root, "failed")}
			if tt.prepare != nil {
				tt.prepare(t, folders)
			}
			q := queue.New(noopProcessor{}, 1, 0)
			h := &Handler{queue: q, manual: manualrequeue.New(q, folders, nil), folders: folders}
			response := manualRequeueOneFailed(t, h, tt.fileName)
			if response.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.wantCode, response.Body.String())
			}
			var body struct {
				Job    string `json:"job"`
				Staged bool   `json:"staged"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Job != "" || body.Staged != tt.staged {
				t.Fatalf("body = %+v, want no Job and staged=%v", body, tt.staged)
			}
		})
	}
}

func TestManualRequeueStagingReturnsPartialSuccess(t *testing.T) {
	root := t.TempDir()
	folders := config.FoldersConfig{Staging: filepath.Join(root, "staging"), Failed: filepath.Join(root, "failed")}
	mustWriteSizedHandlerFile(t, filepath.Join(folders.Staging, "a-C.mp4"), 1)
	mustWriteSizedHandlerFile(t, filepath.Join(folders.Staging, "b-C.mp4"), 1)
	h := &Handler{
		manual:  manualrequeue.New(rejectingAccepter{reject: map[string]error{"a-C.mp4": queue.ErrQueueFull}}, folders, nil),
		folders: folders,
	}

	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/retry/staging", nil)
	h.RetryStaging(c)

	var body struct {
		Count  int      `json:"count"`
		Jobs   []string `json:"jobs"`
		Failed []struct {
			File string `json:"file"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusMultiStatus || body.Count != 1 || len(body.Jobs) != 1 || len(body.Failed) != 1 || body.Failed[0].File != "a-C.mp4" {
		t.Fatalf("status/body = %d %+v, want one accepted and one failed", response.Code, body)
	}
}

func TestManagedFileListRoutesRemainAvailableWhenEmpty(t *testing.T) {
	h := newTestHandler(t.TempDir())
	router := gin.New()
	h.RegisterRoutes(router)
	for _, path := range []string{"/api/v1/files/staging", "/api/v1/files/failed"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"count":0`) {
			t.Fatalf("GET %s = %d %s, want empty success", path, response.Code, response.Body.String())
		}
	}
}

func manualRequeueOneFailed(t *testing.T, h *Handler, fileName string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/retry/failed/"+fileName, nil)
	c.Params = gin.Params{{Key: "name", Value: fileName}}
	h.RetryOneFailed(c)
	return response
}

func newTestHandler(root string) *Handler {
	folders := config.FoldersConfig{
		Staging:  filepath.Join(root, "staging"),
		Process:  filepath.Join(root, "processing"),
		Scraping: filepath.Join(root, "scraping"),
		Failed:   filepath.Join(root, "failed"),
	}
	q := queue.New(noopProcessor{}, 1, 0)
	return &Handler{
		queue:   q,
		manual:  manualrequeue.New(q, folders, nil),
		folders: folders,
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
