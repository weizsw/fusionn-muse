package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
)

// Whisper handles transcription via local whisper.cpp or OpenAI API.
type Whisper struct {
	cfg config.WhisperConfig
}

// NewWhisper creates a new Whisper executor.
func NewWhisper(cfg config.WhisperConfig) *Whisper {
	return &Whisper{cfg: cfg}
}

// Transcribe transcribes a video file and returns the path to the generated subtitle.
func (w *Whisper) Transcribe(ctx context.Context, videoPath string) (string, error) {
	switch strings.ToLower(w.cfg.Provider) {
	case "openai":
		return w.transcribeOpenAI(ctx, videoPath)
	default:
		return w.transcribeLocal(ctx, videoPath)
	}
}

// transcribeLocal uses whisper.cpp binary.
func (w *Whisper) transcribeLocal(ctx context.Context, videoPath string) (string, error) {
	outputDir := filepath.Dir(videoPath)
	baseName := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))

	model := w.cfg.Model
	if model == "" {
		model = "medium"
	}

	lang := w.cfg.Language
	if lang == "" {
		lang = "auto"
	}

	// whisper.cpp CLI: whisper-cpp -m <model> -l <lang> -osrt -of <output> <input>
	outputBase := filepath.Join(outputDir, baseName)
	args := []string{
		"-m", fmt.Sprintf("/app/models/ggml-%s.bin", model),
		"-osrt",
		"-of", outputBase,
	}
	if lang != "auto" {
		args = append(args, "-l", lang)
	}
	args = append(args, videoPath)

	logger.Infof("🎤 Transcribing (local): %s", filepath.Base(videoPath))
	logger.Debugf("  Command: whisper-cpp %s", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, "whisper-cpp", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("whisper-cpp failed: %w\nOutput: %s", err, string(output))
	}

	srtPath := outputBase + ".srt"
	logger.Infof("✅ Transcription complete: %s", filepath.Base(srtPath))
	return srtPath, nil
}

// transcribeOpenAI uses OpenAI Whisper API.
func (w *Whisper) transcribeOpenAI(ctx context.Context, videoPath string) (string, error) {
	outputDir := filepath.Dir(videoPath)
	baseName := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	srtPath := filepath.Join(outputDir, baseName+".srt")

	logger.Infof("🎤 Transcribing (OpenAI API): %s", filepath.Base(videoPath))

	// Open video file
	file, err := os.Open(videoPath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	// Create multipart form
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file
	part, err := writer.CreateFormFile("file", filepath.Base(videoPath))
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("copy file: %w", err)
	}

	// Add model
	_ = writer.WriteField("model", "whisper-1")
	_ = writer.WriteField("response_format", "srt")

	if w.cfg.Language != "" && w.cfg.Language != "auto" {
		_ = writer.WriteField("language", w.cfg.Language)
	}

	writer.Close()

	// Create request
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/audio/transcriptions", &buf)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+w.cfg.APIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Send request
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("api request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &errResp)
		return "", fmt.Errorf("openai api error (%d): %s", resp.StatusCode, errResp.Error.Message)
	}

	// Write SRT file
	if err := os.WriteFile(srtPath, body, 0644); err != nil {
		return "", fmt.Errorf("write srt: %w", err)
	}

	logger.Infof("✅ Transcription complete: %s", filepath.Base(srtPath))
	return srtPath, nil
}
