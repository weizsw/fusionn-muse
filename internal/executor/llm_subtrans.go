package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/toolrun"
	"github.com/fusionn-muse/pkg/logger"
)

const llmSubtransScript = "/app/scripts/llm_subtrans_translate.py"

type LLMSubtrans struct {
	cfg          config.LLMSubtransConfig
	translateCfg config.TranslateConfig
	runner       toolrun.Runner
}

func NewLLMSubtrans(cfg config.LLMSubtransConfig, translateCfg config.TranslateConfig, runner toolrun.Runner) *LLMSubtrans {
	return &LLMSubtrans{cfg: cfg, translateCfg: translateCfg, runner: runner}
}

func (t *LLMSubtrans) Translate(ctx context.Context, subtitlePath string) (string, error) {
	if t.cfg.TimeoutMinutes > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(t.cfg.TimeoutMinutes)*time.Minute)
		defer cancel()
	}

	log := logger.FromContext(ctx)
	dir := filepath.Dir(subtitlePath)
	ext := filepath.Ext(subtitlePath)
	nameWithoutExt := strings.TrimSuffix(filepath.Base(subtitlePath), ext)
	translatedPath := filepath.Join(dir, fmt.Sprintf("%s.%s%s", nameWithoutExt, t.getLangCode(), ext))

	args := []string{
		llmSubtransScript,
		"--input", subtitlePath,
		"--output", translatedPath,
		"--target", t.translateCfg.TargetLang,
	}
	if t.translateCfg.APIKey != "" {
		args = append(args, "--api-key", t.translateCfg.APIKey)
	}
	baseURL := getBaseURL(t.translateCfg.Provider, t.translateCfg.CustomServer)
	if baseURL != "" {
		args = append(args, "--base-url", baseURL)
	}
	model := t.translateCfg.Model
	if model == "" {
		model = "gpt-4o-mini"
	}
	args = append(args, "--model", model)
	if t.translateCfg.Instruction != "" {
		args = append(args, "--instruction", t.translateCfg.Instruction)
	}
	if t.cfg.MaxRetries > 0 {
		args = append(args, "--max-retries", strconv.Itoa(t.cfg.MaxRetries))
	}
	if t.cfg.RequestTimeoutSeconds > 0 {
		args = append(args, "--request-timeout", strconv.Itoa(t.cfg.RequestTimeoutSeconds))
	}
	if t.cfg.RetryBackoffSeconds != nil {
		args = append(args, "--retry-backoff", strconv.FormatFloat(*t.cfg.RetryBackoffSeconds, 'f', -1, 64))
	}
	if t.cfg.MinBatchSize > 0 {
		args = append(args, "--min-batch-size", strconv.Itoa(t.cfg.MinBatchSize))
	}
	if t.cfg.MaxBatchSize > 0 {
		args = append(args, "--max-batch-size", strconv.Itoa(t.cfg.MaxBatchSize))
	}
	if t.cfg.RateLimit > 0 {
		args = append(args, "--ratelimit", strconv.FormatFloat(t.cfg.RateLimit, 'f', -1, 64))
		log.Infof("⏱️ PySubtrans batch pacing: %g requests/minute", t.cfg.RateLimit)
	}

	log.Infof("🌐 Translating with PySubtrans: %s → %s", filepath.Base(subtitlePath), t.translateCfg.TargetLang)
	log.Debugf("  Command: %s", formatCommandLog("python3", args))

	_, stderrStr, err := t.runner.Stream(ctx, "python3", args...)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("llm-subtrans timed out: %w", ctx.Err())
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("llm-subtrans canceled: %w", ctx.Err())
		}
		return "", fmt.Errorf("llm-subtrans failed: %w (see preceding process logs)", err)
	}
	if strings.Contains(stderrStr, "Error:") || strings.Contains(stderrStr, "Traceback") {
		return "", errors.New("llm-subtrans reported errors; see preceding process logs")
	}

	info, err := os.Stat(translatedPath)
	if err != nil {
		return "", fmt.Errorf("translated file not created: %w", err)
	}
	if info.Size() == 0 {
		return "", errors.New("translated file is empty; see preceding process logs")
	}

	log.Infof("✅ PySubtrans translation complete: %s", filepath.Base(translatedPath))
	return translatedPath, nil
}

// formatCommandLog constructs a debug log string representing the executed command,
// redacting sensitive credentials and quoting arguments that contain spaces or newlines.
func formatCommandLog(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, command)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if i > 0 && args[i-1] == "--api-key" {
			parts = append(parts, "***")
			continue
		}
		if strings.ContainsAny(arg, " \t\n\"'") {
			parts = append(parts, strconv.Quote(arg))
		} else {
			parts = append(parts, arg)
		}
	}
	return strings.Join(parts, " ")
}

// getLangCode returns a short language code for filename.
func (t *LLMSubtrans) getLangCode() string {
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

	lower := strings.ToLower(t.translateCfg.TargetLang)
	if code, ok := langMap[lower]; ok {
		return code
	}

	if len(t.translateCfg.TargetLang) >= 2 {
		return strings.ToLower(t.translateCfg.TargetLang[:2])
	}
	return "xx"
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
