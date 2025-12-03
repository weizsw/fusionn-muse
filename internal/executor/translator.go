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

	"golang.org/x/time/rate"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
)

// Translator handles subtitle translation via llm-subtrans.
type Translator struct {
	cfg     config.TranslateConfig
	limiter *rate.Limiter
}

// NewTranslator creates a new Translator executor.
func NewTranslator(cfg config.TranslateConfig) *Translator {
	t := &Translator{cfg: cfg}

	// Set up rate limiter if configured
	if cfg.RateLimitRPM > 0 {
		// Convert RPM to rate per second
		rps := float64(cfg.RateLimitRPM) / 60.0
		t.limiter = rate.NewLimiter(rate.Limit(rps), 1)
		logger.Infof("🚦 Translator rate limit: %d RPM", cfg.RateLimitRPM)
	}

	return t
}

// Translate translates a subtitle file and returns the path to the translated subtitle.
func (t *Translator) Translate(ctx context.Context, subtitlePath string) (string, error) {
	// Apply rate limiting
	if t.limiter != nil {
		if err := t.limiter.Wait(ctx); err != nil {
			return "", fmt.Errorf("rate limit: %w", err)
		}
	}

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
	provider := strings.ToLower(t.cfg.Provider)

	// Select script based on provider
	// Each provider has its own script; llm-subtrans.py defaults to OpenRouter
	script := t.getProviderScript(provider)

	args := []string{
		script,
		inputPath,
		"--target_language", t.cfg.TargetLang,
		"-o", outputPath,
	}

	if provider == "custom" {
		// Custom server endpoint
		if t.cfg.CustomServer != "" {
			args = append(args, "--server", t.cfg.CustomServer)
		}
		if t.cfg.CustomEndpoint != "" {
			args = append(args, "--endpoint", t.cfg.CustomEndpoint)
		}
		args = append(args, "--chat") // Most custom endpoints use chat format
	}

	// Model (applies to all providers)
	if t.cfg.Model != "" {
		args = append(args, "--model", t.cfg.Model)
	}

	// API key
	if t.cfg.APIKey != "" {
		args = append(args, "--apikey", t.cfg.APIKey)
	}

	// Custom instruction
	if t.cfg.Instruction != "" {
		args = append(args, "--instruction", t.cfg.Instruction)
	}

	// Additional custom args
	args = append(args, t.cfg.Args...)

	return args
}

// getProviderScript returns the appropriate llm-subtrans script for the provider.
func (t *Translator) getProviderScript(provider string) string {
	scriptMap := map[string]string{
		"openai":     "/app/llm-subtrans/scripts/gpt-subtrans.py",
		"claude":     "/app/llm-subtrans/scripts/claude-subtrans.py",
		"gemini":     "/app/llm-subtrans/scripts/gemini-subtrans.py",
		"deepseek":   "/app/llm-subtrans/scripts/deepseek-subtrans.py",
		"openrouter": "/app/llm-subtrans/scripts/llm-subtrans.py", // llm-subtrans defaults to OpenRouter
		"custom":     "/app/llm-subtrans/scripts/llm-subtrans.py", // with --server flag
	}

	if script, ok := scriptMap[provider]; ok {
		return script
	}
	// Default to llm-subtrans.py (OpenRouter)
	return "/app/llm-subtrans/scripts/llm-subtrans.py"
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

// WaitForRateLimit blocks until the rate limiter allows the next request.
// Useful for pre-checking before starting expensive operations.
func (t *Translator) WaitForRateLimit(ctx context.Context) error {
	if t.limiter == nil {
		return nil
	}
	return t.limiter.Wait(ctx)
}
