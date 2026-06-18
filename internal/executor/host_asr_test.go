package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fusionn-muse/internal/config"
)

func TestHostASRTranscribesByContainerPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.Path != "/transcribe" {
			t.Fatalf("path = %s", r.URL.Path)
		}

		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["video_path"] != "/data/automation/processing/movie.mp4" {
			t.Fatalf("video_path = %q", req["video_path"])
		}
		if req["container_prefix"] != "/data" {
			t.Fatalf("container_prefix = %q", req["container_prefix"])
		}
		if req["host_prefix"] != "/Volumes/media/data" {
			t.Fatalf("host_prefix = %q", req["host_prefix"])
		}
		if req["model"] != "Qwen/Qwen3-ASR-1.7B" {
			t.Fatalf("model = %q", req["model"])
		}
		if req["language"] != "ja" {
			t.Fatalf("language = %q", req["language"])
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

	out, err := asr.Transcribe(context.Background(), "/data/automation/processing/movie.mp4")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if out != "/data/automation/processing/movie.srt" {
		t.Fatalf("output path = %q", out)
	}
}
