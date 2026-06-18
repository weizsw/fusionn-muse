package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
)

func TestLLMSubtransPassesOpenAICompatibleSettingsAndInstruction(t *testing.T) {
	logger.Init(true)

	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	capturePath := filepath.Join(root, "args.txt")
	python := filepath.Join(bin, "python3")
	if err := os.WriteFile(python, []byte(`#!/bin/sh
printf '%s\n' "$@" > "`+capturePath+`"
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output" ]; then
    shift
    out="$1"
  fi
  shift
done
printf 'translated\n' > "$out"
`), 0755); err != nil {
		t.Fatalf("write fake python: %v", err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	input := filepath.Join(root, "movie.srt")
	if err := os.WriteFile(input, []byte("1\n00:00:00,000 --> 00:00:01,000\nhello\n"), 0644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	translator := NewLLMSubtrans(config.LLMSubtransConfig{}, config.TranslateConfig{
		CustomServer: "http://127.0.0.1:8317/v1",
		APIKey:       "test-key",
		Model:        "gpt-5.4-mini",
		TargetLang:   "Simplified Chinese",
		Instruction:  "Use natural spoken Chinese.",
	})

	out, err := translator.Translate(context.Background(), input)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if out != filepath.Join(root, "movie.zh.srt") {
		t.Fatalf("output = %q", out)
	}

	raw, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{
		llmSubtransScript,
		"--input", input,
		"--output", filepath.Join(root, "movie.zh.srt"),
		"--target", "Simplified Chinese",
		"--api-key", "test-key",
		"--base-url", "http://127.0.0.1:8317/v1",
		"--model", "gpt-5.4-mini",
		"--instruction", "Use natural spoken Chinese.",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}
