package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
)

func TestHostASRKeepsTheHostsOwnTextOutOfTheError(t *testing.T) {
	const canary = "CANARY_HOST_ASR_BODY"
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"error status", http.StatusInternalServerError, `{"success":false,"error":"` + canary + `"}`, "status 500"},
		{"reported failure", http.StatusOK, `{"success":false,"error":"` + canary + `"}`, "reported a failure"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			asr := NewHostASR(config.MLXQwen3ASRConfig{
				ServerURL: server.URL, HostPrefix: "/Volumes/media/data", ContainerPrefix: "/data", TimeoutMinutes: 1,
			})

			// A reader who does not ask for DEBUG never sees the host's text, and
			// the error a worker logs carries only the safe reason.
			var infoLog bytes.Buffer
			t.Setenv("LOG_LEVEL", "INFO")
			logger.Configure(logger.Config{Output: zapcore.AddSync(&infoLog)})
			_, err := asr.Transcribe(context.Background(), "/data/movie.mp4")
			if err == nil {
				t.Fatal("Transcribe returned nil error for a failed response")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), canary) || strings.Contains(infoLog.String(), canary) {
				t.Fatalf("error = %q, INFO log = %q, want neither to carry the host's text", err, infoLog.String())
			}

			// At DEBUG the host's text appears exactly once, bounded.
			var debugLog bytes.Buffer
			t.Setenv("LOG_LEVEL", "DEBUG")
			logger.Configure(logger.Config{Output: zapcore.AddSync(&debugLog)})
			_, _ = asr.Transcribe(context.Background(), "/data/movie.mp4")
			lines := strings.Split(strings.TrimSuffix(debugLog.String(), "\n"), "\n")
			carried := 0
			for _, line := range lines {
				if !strings.Contains(line, canary) {
					continue
				}
				carried++
				if !strings.Contains(line, " DEBUG ") || !strings.Contains(line, "event=asr_response_detail") {
					t.Fatalf("line %q, want the one DEBUG detail event", line)
				}
				value, ok := quotedFieldValue(line, "preview")
				if !ok {
					t.Fatalf("line %q has no preview field", line)
				}
				if len([]rune(value)) > 512 {
					t.Fatalf("preview runes = %d, want at most 512", len([]rune(value)))
				}
			}
			if carried != 1 {
				t.Fatalf("lines carrying the host's text = %d, want exactly one: %q", carried, lines)
			}
		})
	}
}

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
			AttemptID       int64  `json:"attempt_id"`
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
		if req.AttemptID != 7 {
			t.Fatalf("attempt_id = %d", req.AttemptID)
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

	ctx := logger.WithAttemptID(logger.WithAttempt(logger.WithJobID(context.Background(), "job-a"), 2), 7)
	out, err := asr.Transcribe(ctx, "/data/automation/processing/movie.mp4")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if out != "/data/automation/processing/movie.srt" {
		t.Fatalf("output path = %q", out)
	}
}
