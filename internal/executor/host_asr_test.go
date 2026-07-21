package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
)

func TestHostASRTranscribesByContainerPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.Path != "/transcribe" {
			t.Fatalf("path = %s", r.URL.Path)
		}

		var req struct {
			VideoPath       string `json:"video_path"`
			ContainerPrefix string `json:"container_prefix"`
			HostPrefix      string `json:"host_prefix"`
			Model           string `json:"model"`
			Language        string `json:"language"`
			JobID           string `json:"job_id"`
			Attempt         int    `json:"attempt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.VideoPath != "/data/automation/processing/movie.mp4" {
			t.Fatalf("video_path = %q", req.VideoPath)
		}
		if req.ContainerPrefix != "/data" {
			t.Fatalf("container_prefix = %q", req.ContainerPrefix)
		}
		if req.HostPrefix != "/Volumes/media/data" {
			t.Fatalf("host_prefix = %q", req.HostPrefix)
		}
		if req.Model != "Qwen/Qwen3-ASR-1.7B" {
			t.Fatalf("model = %q", req.Model)
		}
		if req.Language != "ja" {
			t.Fatalf("language = %q", req.Language)
		}
		if req.JobID != "job-a" {
			t.Fatalf("job_id = %q", req.JobID)
		}
		if req.Attempt != 2 {
			t.Fatalf("attempt = %d", req.Attempt)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"output_path":"/data/automation/processing/movie.srt"}`))
	}))
	defer server.Close()

	asr := NewHostASR(config.MLXQwen3ASRConfig{
		ServerURL:       server.URL,
		HostPrefix:      "/Volumes/media/data",
		ContainerPrefix: "/data",
		Model:           "Qwen/Qwen3-ASR-1.7B",
		Language:        "ja",
		TimeoutMinutes:  1,
	})

	ctx := logger.WithAttempt(logger.WithJob(context.Background(), "job-a"), 2)
	out, err := asr.Transcribe(ctx, "/data/automation/processing/movie.mp4")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if out != "/data/automation/processing/movie.srt" {
		t.Fatalf("output path = %q", out)
	}
}
