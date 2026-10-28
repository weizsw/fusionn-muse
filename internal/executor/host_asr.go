package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
)

type HostASR struct {
	cfg    config.MLXQwen3ASRConfig
	client *http.Client
}

func NewHostASR(cfg config.MLXQwen3ASRConfig) *HostASR {
	timeoutMinutes := cfg.TimeoutMinutes
	if timeoutMinutes <= 0 {
		timeoutMinutes = 180
	}
	return &HostASR{
		cfg: cfg,
		client: &http.Client{
			Timeout: time.Duration(timeoutMinutes) * time.Minute,
		},
	}
}

type hostASRRequest struct {
	VideoPath       string `json:"video_path"`
	ContainerPrefix string `json:"container_prefix"`
	HostPrefix      string `json:"host_prefix"`
	Model           string `json:"model,omitempty"`
	Language        string `json:"language,omitempty"`
	JobID           string `json:"job_id,omitempty"`
	// Attempt is the retry ordinal; AttemptID is the durable Attempt id.
	Attempt   int   `json:"attempt,omitempty"`
	AttemptID int64 `json:"attempt_id,omitempty"`
}

// hostASRDetail reports the host's own failure text once, as one bounded DEBUG
// excerpt. The render layer keeps preview fields to DEBUG and caps them, so the
// text cannot reach a line a reader sees.
func hostASRDetail(ctx context.Context, status int, text string) {
	logger.Component("host-asr").Debug(ctx, "asr_response_detail", "Host ASR reported a failure",
		zap.Int("status", status), logger.Preview(text))
}

type hostASRResponse struct {
	Success    bool   `json:"success"`
	OutputPath string `json:"output_path"`
	Error      string `json:"error"`
}

func (h *HostASR) Transcribe(ctx context.Context, videoPath string) (string, error) {
	serverURL := strings.TrimRight(h.cfg.ServerURL, "/")
	if serverURL == "" {
		serverURL = "http://host.docker.internal:8766"
	}
	containerPrefix := h.cfg.ContainerPrefix
	if containerPrefix == "" {
		containerPrefix = "/data"
	}

	body, err := json.Marshal(hostASRRequest{
		VideoPath:       videoPath,
		ContainerPrefix: containerPrefix,
		HostPrefix:      h.cfg.HostPrefix,
		Model:           h.cfg.Model,
		Language:        h.cfg.Language,
		JobID:           logger.JobID(ctx),
		Attempt:         logger.Attempt(ctx),
		AttemptID:       logger.AttemptID(ctx),
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/transcribe", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("host ASR request failed: %w", err)
	}
	defer resp.Body.Close()

	var result hostASRResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode host ASR response: %w", err)
	}
	// The host fills this field with free-form remote content: the transcriber's
	// own output and exception text. It never reaches an error, a WARN or a
	// notification, only the one bounded DEBUG excerpt below.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		hostASRDetail(ctx, resp.StatusCode, result.Error)
		return "", fmt.Errorf("host ASR failed: status %d", resp.StatusCode)
	}
	if !result.Success {
		hostASRDetail(ctx, resp.StatusCode, result.Error)
		return "", errors.New("host ASR reported a failure")
	}
	if result.OutputPath == "" {
		return "", fmt.Errorf("host ASR returned empty output path")
	}
	return result.OutputPath, nil
}
