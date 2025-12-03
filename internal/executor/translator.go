package executor

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
	"golang.org/x/time/rate"
)

// Translator handles subtitle translation via llm-subtrans.
type Translator struct {
	cfg     config.TranslateConfig
	limiter *rate.Limiter
	mu      sync.Mutex
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

	// Set timeout for long translations
	done := make(chan error, 1)
	go func() {
		output, err := cmd.CombinedOutput()
		if err != nil {
			done <- fmt.Errorf("translator failed: %w\nOutput: %s", err, string(output))
			return
		}
		done <- nil
	}()

	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return "", ctx.Err()
	case err := <-done:
		if err != nil {
			return "", err
		}
	}

	logger.Infof("✅ Translation complete: %s", filepath.Base(translatedPath))
	return translatedPath, nil
}

func (t *Translator) buildArgs(inputPath, outputPath string) []string {
	// Base: /app/llm-subtrans/gpt-subtrans.py <input> --target_language <lang>
	args := []string{
		"/app/llm-subtrans/gpt-subtrans.py",
		inputPath,
		"--target_language", t.cfg.TargetLang,
		"-o", outputPath,
	}

	provider := strings.ToLower(t.cfg.Provider)

	if provider == "custom" {
		// Custom server endpoint
		args = append(args, "--provider", "custom")
		if t.cfg.CustomServer != "" {
			args = append(args, "--server", t.cfg.CustomServer)
		}
		if t.cfg.CustomEndpoint != "" {
			args = append(args, "--endpoint", t.cfg.CustomEndpoint)
		}
		args = append(args, "--chat") // Most custom endpoints use chat format
	} else {
		// Standard providers: openai, claude, gemini, openrouter
		args = append(args, "--provider", provider)
		if t.cfg.Model != "" {
			args = append(args, "--model", t.cfg.Model)
		}
	}

	// API key
	if t.cfg.APIKey != "" {
		args = append(args, "--apikey", t.cfg.APIKey)
	}

	// Additional custom args
	args = append(args, t.cfg.Args...)

	return args
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
