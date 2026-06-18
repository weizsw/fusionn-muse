package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
}

func NewLLMSubtrans(cfg config.LLMSubtransConfig, translateCfg config.TranslateConfig) *LLMSubtrans {
	return &LLMSubtrans{cfg: cfg, translateCfg: translateCfg}
}

func (t *LLMSubtrans) Translate(ctx context.Context, subtitlePath string) (string, error) {
	if t.cfg.TimeoutMinutes > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(t.cfg.TimeoutMinutes)*time.Minute)
		defer cancel()
	}

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

	logger.Infof("🌐 Translating with PySubtrans: %s → %s", filepath.Base(subtitlePath), t.translateCfg.TargetLang)
	logger.Debugf("  Command: python3 %s --input %s --output %s --target %s --base-url %s --model %s",
		llmSubtransScript, subtitlePath, translatedPath, t.translateCfg.TargetLang, baseURL, model)

	_, stderrStr, err := toolrun.ExecRunner{}.Stream(ctx, "python3", args...)
	if err != nil {
		return "", fmt.Errorf("llm-subtrans failed: %w\nStderr: %s", err, stderrStr)
	}
	if strings.Contains(stderrStr, "Error:") || strings.Contains(stderrStr, "Traceback") {
		return "", fmt.Errorf("llm-subtrans reported errors:\n%s", stderrStr)
	}

	info, err := os.Stat(translatedPath)
	if err != nil {
		return "", fmt.Errorf("translated file not created: %w\nStderr: %s", err, stderrStr)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("translated file is empty\nStderr: %s", stderrStr)
	}

	logger.Infof("✅ PySubtrans translation complete: %s", filepath.Base(translatedPath))
	return translatedPath, nil
}

func (t *LLMSubtrans) getLangCode() string {
	return (&Translator{cfg: t.translateCfg}).getLangCode()
}
