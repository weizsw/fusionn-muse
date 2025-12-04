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
	cfg          config.WhisperConfig
	translateCfg config.TranslateConfig // For LLM post-processing
}

// NewWhisper creates a new Whisper executor.
func NewWhisper(cfg config.WhisperConfig, translateCfg config.TranslateConfig) *Whisper {
	return &Whisper{cfg: cfg, translateCfg: translateCfg}
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

	// Build command: python transcribe.py <input> <output> --model <model> [--language <lang>] [--prompt <prompt>]
	args := []string{
		transcribeScript,
		videoPath,
		srtPath,
		"--model", model,
	}
	if lang != "" && lang != "auto" {
		args = append(args, "--language", lang)
	}
	if w.cfg.Prompt != "" {
		args = append(args, "--prompt", w.cfg.Prompt)
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

	// Post-process with LLM if enabled
	if w.cfg.OptimizeSubtitles || w.cfg.SplitSentences || w.cfg.RemovePunctuation {
		processedPath, err := w.postProcessSubtitles(ctx, srtPath)
		if err != nil {
			logger.Warnf("⚠️ Post-processing failed (using original): %v", err)
			return srtPath, nil
		}
		return processedPath, nil
	}

	return srtPath, nil
}

const subtitleProcessorScript = "/app/scripts/subtitle_processor.py"

// postProcessSubtitles uses LLM to optimize and split subtitles.
func (w *Whisper) postProcessSubtitles(ctx context.Context, srtPath string) (string, error) {
	// Output to same path (overwrite)
	outputPath := srtPath

	args := []string{
		subtitleProcessorScript,
		srtPath,
		outputPath,
	}

	// LLM settings from translate config
	if w.cfg.OptimizeSubtitles || w.cfg.SplitSentences {
		if w.translateCfg.APIKey == "" {
			return "", fmt.Errorf("translate API key required for subtitle post-processing")
		}

		args = append(args, "--api-key", w.translateCfg.APIKey)

		// Determine base URL
		baseURL := "https://api.openai.com"
		switch strings.ToLower(w.translateCfg.Provider) {
		case "openai":
			baseURL = "https://api.openai.com"
		case "openrouter":
			baseURL = "https://openrouter.ai/api"
		case "custom":
			if w.translateCfg.CustomServer != "" {
				baseURL = w.translateCfg.CustomServer
			}
		}
		args = append(args, "--base-url", baseURL)

		// Model
		model := w.translateCfg.Model
		if model == "" {
			model = "gpt-4o-mini"
		}
		args = append(args, "--model", model)
	}

	if w.cfg.OptimizeSubtitles {
		args = append(args, "--optimize")
		if w.cfg.Prompt != "" {
			args = append(args, "--reference", w.cfg.Prompt)
		}
	}

	if w.cfg.SplitSentences {
		args = append(args, "--split")
		maxCJK := w.cfg.MaxCJKChars
		if maxCJK <= 0 {
			maxCJK = 25
		}
		maxEnglish := w.cfg.MaxEnglishWords
		if maxEnglish <= 0 {
			maxEnglish = 18 // VideoCaptioner default
		}
		args = append(args, "--max-cjk", fmt.Sprintf("%d", maxCJK), "--max-english", fmt.Sprintf("%d", maxEnglish))
	}

	if w.cfg.RemovePunctuation {
		args = append(args, "--remove-punctuation")
	}

	logger.Infof("📝 Post-processing subtitles...")
	logger.Debugf("  Command: python3 %s", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, "python3", args...)

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}

	wg.Add(2)
	go StreamDimmed(&wg, stdoutPipe, &stdoutBuf)
	go StreamDimmed(&wg, stderrPipe, &stderrBuf)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start post-processing: %w", err)
	}

	wg.Wait()

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("post-processing failed: %w\nStderr: %s", err, stderrBuf.String())
	}

	logger.Infof("✅ Post-processing complete")
	return outputPath, nil
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
