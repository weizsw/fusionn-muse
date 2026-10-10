package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
)

func TestLLMSubtransFailuresDoNotReplayProcessLogs(t *testing.T) {
	logger.Init(true)
	processErr := errors.New("exit status 1")
	tests := []struct {
		name        string
		processErr  error
		stderr      string
		emptyOutput bool
		want        string
	}{
		{name: "process failed", processErr: processErr, want: "llm-subtrans failed"},
		{name: "script reported errors", stderr: "Traceback: script failure", want: "llm-subtrans reported errors"},
		{name: "missing output", want: "translated file not created"},
		{name: "empty output", emptyOutput: true, want: "translated file is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			processLogs := "INFO: HTTP Request: POST http://example.test/v1/chat/completions 200 OK\nINFO: Summary: a greeting\n" + tt.stderr
			runner := &fakeRunner{onStream: func(_ string, _ ...string) (string, string, error) {
				if tt.emptyOutput {
					if err := os.WriteFile(filepath.Join(root, "movie.zh.srt"), nil, 0644); err != nil {
						t.Fatal(err)
					}
				}
				return "", processLogs, tt.processErr
			}}
			translator := NewLLMSubtrans(config.LLMSubtransConfig{}, config.TranslateConfig{TargetLang: "Simplified Chinese"}, runner)
			output, err := translator.Translate(context.Background(), filepath.Join(root, "movie.srt"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Translate error = %v, want %q", err, tt.want)
			}
			if output != "" {
				t.Fatalf("output = %q on failure", output)
			}
			if tt.processErr != nil && !errors.Is(err, tt.processErr) {
				t.Fatalf("process error not preserved: %v", err)
			}
			for _, replayed := range []string{"INFO:", "HTTP Request:", "Summary:", "Traceback:", "Stderr:", "\n"} {
				if strings.Contains(err.Error(), replayed) {
					t.Fatalf("error replays process logs: %q", err)
				}
			}
		})
	}
}

func TestLLMSubtransReportsContextFailure(t *testing.T) {
	logger.Init(true)
	for _, tt := range []struct {
		name string
		want error
	}{
		{name: "timed out", want: context.DeadlineExceeded},
		{name: "canceled", want: context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if errors.Is(tt.want, context.DeadlineExceeded) {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else {
				cancel()
			}
			defer cancel()
			runner := &fakeRunner{onStream: func(_ string, _ ...string) (string, string, error) {
				return "", "INFO: old request succeeded", errors.New("signal: killed")
			}}
			translator := NewLLMSubtrans(config.LLMSubtransConfig{TimeoutMinutes: 180}, config.TranslateConfig{}, runner)
			_, err := translator.Translate(ctx, filepath.Join(t.TempDir(), "movie.srt"))
			if !errors.Is(err, tt.want) || !strings.Contains(err.Error(), "llm-subtrans "+tt.name) {
				t.Fatalf("error = %v, want %q with %v", err, tt.name, tt.want)
			}
			if strings.Contains(err.Error(), "signal: killed") || strings.Contains(err.Error(), "INFO:") {
				t.Fatalf("error hides context failure or replays logs: %v", err)
			}
		})
	}
}

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
