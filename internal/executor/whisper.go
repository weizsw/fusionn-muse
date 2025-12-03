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
	"sync"
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

const transcribeScript = "/app/scripts/transcribe.py"

// transcribeLocal uses faster-whisper via Python script.
func (w *Whisper) transcribeLocal(ctx context.Context, videoPath string) (string, error) {
	outputDir := filepath.Dir(videoPath)
	baseName := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	srtPath := filepath.Join(outputDir, baseName+".srt")

	model := w.cfg.Model
	if model == "" {
		model = "large-v2"
	}

	lang := w.cfg.Language
	if lang == "" {
		lang = "auto"
	}

	// Build command: python transcribe.py <input> <output> --model <model> [--language <lang>]
	args := []string{
		transcribeScript,
		videoPath,
		srtPath,
		"--model", model,
	}
	if lang != "" && lang != "auto" {
		args = append(args, "--language", lang)
	}

	logger.Infof("🎤 Transcribing (faster-whisper): %s", filepath.Base(videoPath))
	logger.Debugf("  Command: python3 %s", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, "python3", args...)

	// Stream output in real-time
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup

	wg.Add(2)
	go StreamDimmed(&wg, stdoutPipe, &stdoutBuf)
	go StreamDimmed(&wg, stderrPipe, &stderrBuf)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start transcription: %w", err)
	}

	wg.Wait()

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("transcription failed: %w\nStderr: %s", err, stderrBuf.String())
	}

	// Check stderr for error patterns
	stderrStr := stderrBuf.String()
	if strings.Contains(stderrStr, "Error:") || strings.Contains(stderrStr, "Traceback") {
		return "", fmt.Errorf("transcription reported errors:\n%s", stderrStr)
	}

	// Verify SRT file was created and has content
	info, err := os.Stat(srtPath)
	if err != nil {
		return "", fmt.Errorf("SRT file not created: %w\nOutput: %s", err, stdoutBuf.String())
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("SRT file is empty (transcription failed)\nOutput: %s", stdoutBuf.String())
	}

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
	if _, copyErr := io.Copy(part, file); copyErr != nil {
		return "", fmt.Errorf("copy file: %w", copyErr)
	}

	// Add model and format fields
	err = writer.WriteField("model", "whisper-1")
	if err != nil {
		return "", fmt.Errorf("write model field: %w", err)
	}
	err = writer.WriteField("response_format", "srt")
	if err != nil {
		return "", fmt.Errorf("write format field: %w", err)
	}
	if w.cfg.Language != "" && w.cfg.Language != "auto" {
		err = writer.WriteField("language", w.cfg.Language)
		if err != nil {
			return "", fmt.Errorf("write language field: %w", err)
		}
	}

	err = writer.Close()
	if err != nil {
		return "", fmt.Errorf("close multipart writer: %w", err)
	}

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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if jsonErr := json.Unmarshal(body, &errResp); jsonErr != nil {
			return "", fmt.Errorf("openai api error (%d): %s", resp.StatusCode, string(body))
		}
		return "", fmt.Errorf("openai api error (%d): %s", resp.StatusCode, errResp.Error.Message)
	}

	// Write SRT file
	if err := os.WriteFile(srtPath, body, 0600); err != nil {
		return "", fmt.Errorf("write srt: %w", err)
	}

	logger.Infof("✅ Transcription complete: %s", filepath.Base(srtPath))
	return srtPath, nil
}
