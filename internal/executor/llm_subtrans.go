package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/toolrun"
	"github.com/fusionn-muse/pkg/logger"
)

const llmSubtransScript = "/app/scripts/llm_subtrans_translate.py"

// scriptComponent is the stable module name of the translation script's events.
var scriptComponent = logger.Component("llm-subtrans")

// ScriptDiagnostics is the safe part of the translation script's failure report:
// what kind of failure it was, any structural fact about it, and where it
// happened. It carries no message, so the caller can attach it to the one error
// a reader sees without bringing content back.
type ScriptDiagnostics struct {
	ErrorType string
	Module    string
	Errno     int
	Strerror  string
	Frames    string
	Truncated bool

	// cause is the process error the script's own report belongs to, so a reader
	// sees the script's facts and the real exit status together.
	cause error
}

// withCause attaches the process error that ended the script.
func (d ScriptDiagnostics) withCause(err error) ScriptDiagnostics {
	d.cause = err
	return d
}

// Unwrap exposes the process error, so errors.Is and a real exit status keep
// working next to the script's own facts.
func (d ScriptDiagnostics) Unwrap() error { return d.cause }

func (d ScriptDiagnostics) Error() string {
	reason := d.ErrorType
	if reason == "" {
		reason = "script failure"
	}
	if d.Module != "" {
		reason += " (module " + d.Module + ")"
	}
	if d.cause != nil {
		return "llm-subtrans failed: " + reason + ": " + d.cause.Error()
	}
	return "llm-subtrans failed: " + reason
}

// Fields renders the diagnostics as fields for the failure report.
func (d ScriptDiagnostics) Fields() []zap.Field {
	fields := make([]zap.Field, 0, 6)
	if d.ErrorType != "" {
		fields = append(fields, zap.String("error_type", d.ErrorType))
	}
	if d.Module != "" {
		fields = append(fields, zap.String("module", d.Module))
	}
	if d.Errno != 0 {
		fields = append(fields, zap.Int("errno", d.Errno))
	}
	if d.Strerror != "" {
		fields = append(fields, zap.String("strerror", d.Strerror))
	}
	if d.Frames != "" {
		fields = append(fields, zap.String("stack", d.Frames))
	}
	if d.Truncated {
		fields = append(fields, zap.Bool("truncated", true))
	}
	return fields
}

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

	log := scriptComponent
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
		log.Info(ctx, "translation_pacing", "Translation request pacing configured",
			zap.Float64("requests_per_minute", t.cfg.RateLimit))
	}

	log.Info(ctx, "tool_started", "Translation tool started",
		zap.String("tool", "llm-subtrans"), zap.String("file", filepath.Base(subtitlePath)),
		zap.String("target_language", t.translateCfg.TargetLang))
	// The command line carries the instruction, so it is only ever a bounded
	// DEBUG excerpt.
	log.Debug(ctx, "tool_invocation", "Translation tool invocation",
		zap.String("tool", "llm-subtrans"), logger.Preview(formatCommandLog("python3", args)))

	capture := &scriptCapture{}

	// The script writes the unified format itself, so this process never reads
	// its standard output, and it consumes the script's standard error instead of
	// replaying it: the verdict, the safe failure facts and bounded counters are
	// taken here, and a reader sees the summary events re-emitted below.
	streamErr := t.runner.Stream(ctx, "python3", io.Discard, capture, args...)
	capture.close()
	capture.report(ctx, scriptComponent)

	if streamErr != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("llm-subtrans timed out: %w", ctx.Err())
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("llm-subtrans canceled: %w", ctx.Err())
		}
		if diagnostics := capture.safeDiagnostics(); diagnostics.ErrorType != "" {
			return "", diagnostics.withCause(streamErr)
		}
		return "", fmt.Errorf("llm-subtrans failed: %w", streamErr)
	}
	if capture.verdict() {
		diagnostics := capture.safeDiagnostics()
		if diagnostics.ErrorType == "" {
			return "", errors.New("llm-subtrans reported errors; see the logged summary")
		}
		return "", diagnostics
	}

	info, err := os.Stat(translatedPath)
	if err != nil {
		return "", fmt.Errorf("translated file not created: %w", err)
	}
	if info.Size() == 0 {
		return "", errors.New("translated file is empty; see preceding process logs")
	}

	log.Info(ctx, "tool_completed", "Translation completed",
		zap.String("tool", "llm-subtrans"), zap.String("file", filepath.Base(translatedPath)))
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
