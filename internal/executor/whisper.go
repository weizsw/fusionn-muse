package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/toolrun"
	"github.com/fusionn-muse/pkg/logger"
)

// Whisper handles transcription via faster-whisper.
type Whisper struct {
	cfg          config.WhisperConfig
	translateCfg config.TranslateConfig // For LLM post-processing
}

// NewWhisper creates a new Whisper executor.
func NewWhisper(cfg config.WhisperConfig, translateCfg config.TranslateConfig) *Whisper {
	return &Whisper{cfg: cfg, translateCfg: translateCfg}
}

const transcribeScript = "/app/scripts/transcribe.py"

// Transcribe transcribes a video file and returns the path to the generated subtitle.
func (w *Whisper) Transcribe(ctx context.Context, videoPath string) (string, error) {
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

	// Build command: python transcribe.py <input> <output> [options]
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

	// Device (cuda/cpu/auto)
	device := w.cfg.Device
	if device == "" {
		device = "auto"
	}
	args = append(args, "--device", device)

	// VAD settings
	if w.cfg.VADFilter != nil && !*w.cfg.VADFilter {
		args = append(args, "--no-vad")
	}
	if w.cfg.VADThreshold > 0 {
		args = append(args, "--vad-threshold", fmt.Sprintf("%.2f", w.cfg.VADThreshold))
	}

	// Word timestamps (required for LLM sentence splitting)
	if w.cfg.SplitSentences {
		args = append(args, "--word-timestamps")
	}

	logger.Infof("🎤 Transcribing: %s", filepath.Base(videoPath))
	logger.Debugf("  Command: python3 %s", strings.Join(args, " "))

	stdoutStr, stderrStr, err := toolrun.ExecRunner{}.Stream(ctx, "python3", args...)
	if err != nil {
		return "", fmt.Errorf("transcription failed: %w\nStderr: %s", err, stderrStr)
	}

	// Check stderr for error patterns
	if strings.Contains(stderrStr, "Error:") || strings.Contains(stderrStr, "Traceback") {
		return "", fmt.Errorf("transcription reported errors:\n%s", stderrStr)
	}

	// Verify SRT file was created and has content
	info, err := os.Stat(srtPath)
	if err != nil {
		return "", fmt.Errorf("SRT file not created: %w\nOutput: %s", err, stdoutStr)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("SRT file is empty (transcription failed)\nOutput: %s", stdoutStr)
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

		// Use translator's base URL helper
		baseURL := getBaseURL(w.translateCfg.Provider, w.translateCfg.CustomServer)
		args = append(args, "--base-url", baseURL)

		// Model
		model := w.translateCfg.Model
		if model == "" {
			model = "gpt-4o-mini"
		}
		args = append(args, "--model", model)

		// Threads and batch size
		threads := w.translateCfg.Threads
		if threads <= 0 {
			threads = 4
		}
		args = append(args, "--threads", fmt.Sprintf("%d", threads))

		batchSize := w.translateCfg.BatchSize
		if batchSize <= 0 {
			batchSize = 10
		}
		args = append(args, "--batch-size", fmt.Sprintf("%d", batchSize))
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

	_, stderrStr, err := toolrun.ExecRunner{}.Stream(ctx, "python3", args...)
	if err != nil {
		return "", fmt.Errorf("post-processing failed: %w\nStderr: %s", err, stderrStr)
	}

	logger.Infof("✅ Post-processing complete")
	return outputPath, nil
}

// getBaseURL returns the OpenAI-compatible API base URL for the provider.
func getBaseURL(provider, customServer string) string {
	provider = strings.ToLower(provider)

	baseURLs := map[string]string{
		"openai":       "https://api.openai.com",
		"deepseek":     "https://api.deepseek.com",
		"openrouter":   "https://openrouter.ai/api",
		"groq":         "https://api.groq.com/openai",
		"together":     "https://api.together.xyz",
		"fireworks":    "https://api.fireworks.ai/inference",
		"siliconcloud": "https://api.siliconflow.cn/v1",
		"siliconflow":  "https://api.siliconflow.cn/v1",
	}

	if customServer != "" {
		return customServer
	}

	if url, ok := baseURLs[provider]; ok {
		return url
	}

	return "https://api.openai.com"
}
