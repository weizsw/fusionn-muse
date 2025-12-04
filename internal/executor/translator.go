package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
)

// Translator handles subtitle translation via VideoCaptioner's LLM translator.
type Translator struct {
	cfg config.TranslateConfig
}

// NewTranslator creates a new Translator executor.
func NewTranslator(cfg config.TranslateConfig) *Translator {
	return &Translator{cfg: cfg}
}

// Translate translates a subtitle file and returns the path to the translated subtitle.
func (t *Translator) Translate(ctx context.Context, subtitlePath string) (string, error) {
	dir := filepath.Dir(subtitlePath)
	base := filepath.Base(subtitlePath)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)

	langCode := t.getLangCode()
	translatedName := fmt.Sprintf("%s.%s%s", nameWithoutExt, langCode, ext)
	translatedPath := filepath.Join(dir, translatedName)

	// Build command args
	args := t.buildArgs(subtitlePath, translatedPath)

	logger.Infof("🌐 Translating: %s → %s", filepath.Base(subtitlePath), t.cfg.TargetLang)
	logger.Debugf("  Command: python3 %s", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, "python3", args...)

	// Pipe stdout and stderr for real-time logging
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

	// Stream output in dim/grey (like Docker build logs)
	wg.Add(2)
	go StreamDimmed(&wg, stdoutPipe, &stdoutBuf)
	go StreamDimmed(&wg, stderrPipe, &stderrBuf)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start cmd: %w", err)
	}

	// Wait for output streaming to complete
	wg.Wait()

	// Wait for command to finish
	if err := cmd.Wait(); err != nil {
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if stderrStr != "" {
			logger.Errorf("Script stderr: %s", stderrStr)
		}
		return "", fmt.Errorf("translator failed: %w", err)
	}

	// Check stderr for error patterns (script may exit 0 but still fail)
	stderrStr := stderrBuf.String()
	if strings.Contains(stderrStr, "Error:") || strings.Contains(stderrStr, "Traceback") {
		return "", fmt.Errorf("translator reported errors:\n%s", stderrStr)
	}

	// Verify output file was created and has content
	info, err := os.Stat(translatedPath)
	if err != nil {
		return "", fmt.Errorf("translated file not created: %w\nStderr: %s", err, stderrStr)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("translated file is empty (translation failed)\nStderr: %s", stderrStr)
	}

	logger.Infof("✅ Translation complete: %s", filepath.Base(translatedPath))
	return translatedPath, nil
}

func (t *Translator) buildArgs(inputPath, outputPath string) []string {
	// Use VideoCaptioner's translate.py script
	script := "/app/scripts/translate.py"

	args := []string{
		script,
		inputPath,
		outputPath,
		"--target", t.cfg.TargetLang,
	}

	// Model
	if t.cfg.Model != "" {
		args = append(args, "--model", t.cfg.Model)
	}

	// API key
	if t.cfg.APIKey != "" {
		args = append(args, "--api-key", t.cfg.APIKey)
	}

	// Base URL (for custom/OpenAI-compatible endpoints)
	baseURL := t.getBaseURL()
	if baseURL != "" {
		args = append(args, "--base-url", baseURL)
	}

	// Custom instruction/prompt
	if t.cfg.Instruction != "" {
		args = append(args, "--prompt", t.cfg.Instruction)
	}

	// Reflect mode for higher quality (optional)
	if t.cfg.UseReflect {
		args = append(args, "--reflect")
	}

	// Thread count (default 4)
	if t.cfg.Threads > 0 {
		args = append(args, "--threads", fmt.Sprintf("%d", t.cfg.Threads))
	}

	// Batch size (default 10)
	if t.cfg.BatchSize > 0 {
		args = append(args, "--batch-size", fmt.Sprintf("%d", t.cfg.BatchSize))
	}

	return args
}

// getBaseURL returns the OpenAI-compatible API base URL for the provider.
func (t *Translator) getBaseURL() string {
	provider := strings.ToLower(t.cfg.Provider)

	// Known provider base URLs (all OpenAI-compatible)
	baseURLs := map[string]string{
		"openai":       "https://api.openai.com",
		"deepseek":     "https://api.deepseek.com",
		"openrouter":   "https://openrouter.ai/api",
		"groq":         "https://api.groq.com/openai",
		"together":     "https://api.together.xyz",
		"fireworks":    "https://api.fireworks.ai/inference",
		"siliconcloud": "https://api.siliconflow.cn/v1",
		"siliconflow":  "https://api.siliconflow.cn/v1", // alias
	}

	// Check for custom server first
	if t.cfg.CustomServer != "" {
		return t.cfg.CustomServer
	}

	if url, ok := baseURLs[provider]; ok {
		return url
	}

	// Default to OpenAI
	return "https://api.openai.com"
}

// getLangCode returns a short language code for filename.
func (t *Translator) getLangCode() string {
	langMap := map[string]string{
		"simplified chinese":  "zh",
		"traditional chinese": "zh-tw",
		"chinese":             "zh",
		"japanese":            "ja",
		"korean":              "ko",
		"spanish":             "es",
		"french":              "fr",
		"german":              "de",
		"italian":             "it",
		"portuguese":          "pt",
		"russian":             "ru",
		"english":             "en",
		"thai":                "th",
		"vietnamese":          "vi",
		"indonesian":          "id",
		"malay":               "ms",
		"arabic":              "ar",
		"hindi":               "hi",
	}

	lower := strings.ToLower(t.cfg.TargetLang)
	if code, ok := langMap[lower]; ok {
		return code
	}

	if len(t.cfg.TargetLang) >= 2 {
		return strings.ToLower(t.cfg.TargetLang[:2])
	}
	return "xx"
}
