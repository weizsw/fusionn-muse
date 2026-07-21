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
	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/internal/service/processor"
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
	h := &Handler{
		queue:     queue.New(noopProcessor{}, 1, 0),
		processor: processor.New(nil, nil, folders),
		folders:   folders,
	}
	h.queue.Start()
	defer h.queue.Stop()

	ids := make(map[string]bool)
	for _, fileName := range []string{"movie-a.mp4", "movie-b.mp4"} {
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

func TestRetryStagingReportsQueueRejection(t *testing.T) {
	root := t.TempDir()
	folders := config.FoldersConfig{Staging: filepath.Join(root, "staging")}
	mustWriteSizedHandlerFile(t, filepath.Join(folders.Staging, "movie.mp4"), mediaintake.MinVideoSize+1)
	h := &Handler{
		queue:     queue.New(noopProcessor{}, 1, 0),
		processor: processor.New(nil, nil, folders),
		folders:   folders,
	}

	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/retry/staging", nil)
	h.RetryStaging(c)

	var body struct {
		Count  int      `json:"count"`
		Jobs   []string `json:"jobs"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Count != 0 || len(body.Jobs) != 0 || len(body.Errors) != 1 {
		t.Fatalf("response = %+v, want no accepted Jobs and one error", body)
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

func newTestHandler(root string) *Handler {
	return &Handler{
		queue: queue.New(noopProcessor{}, 1, 0),
		folders: config.FoldersConfig{
			Staging:  filepath.Join(root, "staging"),
			Process:  filepath.Join(root, "processing"),
			Scraping: filepath.Join(root, "scraping"),
			Failed:   filepath.Join(root, "failed"),
		},
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
