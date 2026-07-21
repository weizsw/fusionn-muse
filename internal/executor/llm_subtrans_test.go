package executor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
)

func TestLLMSubtransPassesOpenAICompatibleSettingsAndInstruction(t *testing.T) {
	logger.Init(true)

	root := t.TempDir()
	input := filepath.Join(root, "movie.srt")
	if err := os.WriteFile(input, []byte("1\n00:00:00,000 --> 00:00:01,000\nhello\n"), 0644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	output := filepath.Join(root, "movie.zh.srt")
	runner := &fakeRunner{onStream: func(_ string, _ ...string) (string, string, error) {
		if err := os.WriteFile(output, []byte("translated\n"), 0644); err != nil {
			t.Fatalf("write translation: %v", err)
		}
		return "", "", nil
	}}
	translator := NewLLMSubtrans(config.LLMSubtransConfig{}, config.TranslateConfig{
		CustomServer: "http://127.0.0.1:8317/v1",
		APIKey:       "test-key",
		Model:        "gpt-5.4-mini",
		TargetLang:   "Simplified Chinese",
		Instruction:  "Use natural spoken Chinese.",
	}, runner)

	out, err := translator.Translate(context.Background(), input)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if out != filepath.Join(root, "movie.zh.srt") {
		t.Fatalf("output = %q", out)
	}

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
	wantCalls := []commandCall{{name: "python3", args: want}}
	if !reflect.DeepEqual(runner.streamCalls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", runner.streamCalls, wantCalls)
	}
}
