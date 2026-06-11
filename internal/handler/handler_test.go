package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/fileops"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/pkg/logger"
)

type noopProcessor struct{}

func (noopProcessor) Process(context.Context, *queue.Job) error {
	return nil
}

func init() {
	logger.Init(true)
	gin.SetMode(gin.TestMode)
}

func TestTorrentCompleteReturnsOKForNoValidMedia(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	mustWriteSizedHandlerFile(t, filepath.Join(folder, "movie.mp4"), fileops.MinVideoSize+1)
	handler := newTestHandler(root)

	response := postTorrentComplete(t, handler, `{"path":"`+folder+`","name":"no code here"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "no valid video files found") {
		t.Fatalf("body = %s, want no valid video response", response.Body.String())
	}
}

func TestTorrentCompleteReturnsServerErrorForMediaPreparationFailure(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "media-extract", "SSNI-083-image", "disc.iso")
	mustWriteSizedHandlerFile(t, image, 1024)
	handler := newTestHandler(root)

	response := postTorrentComplete(t, handler, `{"path":"`+image+`","name":"SSNI-083"}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusInternalServerError, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "image extraction dir overlaps source path") {
		t.Fatalf("body = %s, want preparation failure response", response.Body.String())
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
