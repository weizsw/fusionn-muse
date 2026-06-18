package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fusionn-muse/internal/config"
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("host ASR failed: %s", result.Error)
	}
	if !result.Success {
		return "", fmt.Errorf("host ASR failed: %s", result.Error)
	}
	if result.OutputPath == "" {
		return "", fmt.Errorf("host ASR returned empty output path")
	}
	return result.OutputPath, nil
}
