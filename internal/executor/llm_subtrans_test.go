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

func TestLLMSubtransEndpointAndLanguageCodesSurviveLegacyRemoval(t *testing.T) {
	for _, tc := range []struct {
		provider, custom, want string
	}{
		{"OPENAI", "", "https://api.openai.com"},
		{"deepseek", "", "https://api.deepseek.com"},
		{"siliconcloud", "", "https://api.siliconflow.cn/v1"},
		{"siliconflow", "", "https://api.siliconflow.cn/v1"},
		{"openrouter", "", "https://openrouter.ai/api"},
		{"groq", "", "https://api.groq.com/openai"},
		{"together", "", "https://api.together.xyz"},
		{"fireworks", "", "https://api.fireworks.ai/inference"},
		{"openai", "https://proxy.example/path/v1", "https://proxy.example/path/v1"},
		{"unknown", "", "https://api.openai.com"},
	} {
		if got := getBaseURL(tc.provider, tc.custom); got != tc.want {
			t.Fatalf("provider=%q custom=%q URL=%q want=%q", tc.provider, tc.custom, got, tc.want)
		}
	}
	for _, tc := range []struct{ language, want string }{
		{"Simplified Chinese", "zh"}, {"Traditional Chinese", "zh-tw"},
		{"JAPANESE", "ja"}, {"French", "fr"}, {"Korean", "ko"},
		{"Esperanto", "es"}, {"", "xx"}, {"x", "xx"},
	} {
		translator := NewLLMSubtrans(config.LLMSubtransConfig{}, config.TranslateConfig{TargetLang: tc.language}, nil)
		if got := translator.getLangCode(); got != tc.want {
			t.Fatalf("language=%q code=%q want=%q", tc.language, got, tc.want)
		}
	}
}

func TestLLMSubtransPassesConfiguredRetryBackoff(t *testing.T) {
	val0 := 0.0
	val15 := 1.5
	val3 := 3.0
	for _, item := range []struct {
		name    string
		backoff *float64
		want    string
	}{
		{"omitted", nil, ""},
		{"zero delay", &val0, "0"},
		{"fractional delay", &val15, "1.5"},
		{"standard delay", &val3, "3"},
	} {
		t.Run(item.name, func(t *testing.T) {
			root := t.TempDir()
			runner := &fakeRunner{onStream: func(_ string, args ...string) (string, string, error) {
				got := ""
				for i, arg := range args {
					if arg == "--retry-backoff" {
						got = args[i+1]
					}
				}
				if got != item.want {
					t.Fatalf("retry-backoff=%q want=%q", got, item.want)
				}
				if err := os.WriteFile(filepath.Join(root, "movie.zh.srt"), []byte("translation"), 0644); err != nil {
					t.Fatal(err)
				}
				return "", "", nil
			}}
			translator := NewLLMSubtrans(config.LLMSubtransConfig{RetryBackoffSeconds: item.backoff}, config.TranslateConfig{TargetLang: "Simplified Chinese"}, runner)
			if _, err := translator.Translate(context.Background(), filepath.Join(root, "movie.srt")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLLMSubtransPassesConfiguredRequestTimeout(t *testing.T) {
	for _, item := range []struct {
		name    string
		timeout int
		want    string
	}{
		{"wrapper default", 0, ""},
		{"explicit default", 180, "180"},
		{"custom timeout", 300, "300"},
	} {
		t.Run(item.name, func(t *testing.T) {
			root := t.TempDir()
			runner := &fakeRunner{onStream: func(_ string, args ...string) (string, string, error) {
				got := ""
				for i, arg := range args {
					if arg == "--request-timeout" {
						got = args[i+1]
					}
				}
				if got != item.want {
					t.Fatalf("request-timeout=%q want=%q", got, item.want)
				}
				if err := os.WriteFile(filepath.Join(root, "movie.zh.srt"), []byte("translation"), 0644); err != nil {
					t.Fatal(err)
				}
				return "", "", nil
			}}
			translator := NewLLMSubtrans(config.LLMSubtransConfig{RequestTimeoutSeconds: item.timeout}, config.TranslateConfig{TargetLang: "Simplified Chinese"}, runner)
			if _, err := translator.Translate(context.Background(), filepath.Join(root, "movie.srt")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLLMSubtransPassesConfiguredMaxRetries(t *testing.T) {
	for _, item := range []struct {
		name    string
		retries int
		want    string
	}{
		{"wrapper default", 0, ""},
		{"one retry", 1, "1"},
		{"five retries", 5, "5"},
	} {
		t.Run(item.name, func(t *testing.T) {
			root := t.TempDir()
			runner := &fakeRunner{onStream: func(_ string, args ...string) (string, string, error) {
				got := ""
				for i, arg := range args {
					if arg == "--max-retries" {
						got = args[i+1]
					}
				}
				if got != item.want {
					t.Fatalf("max-retries=%q want=%q", got, item.want)
				}
				if err := os.WriteFile(filepath.Join(root, "movie.zh.srt"), []byte("translation"), 0644); err != nil {
					t.Fatal(err)
				}
				return "", "", nil
			}}
			translator := NewLLMSubtrans(config.LLMSubtransConfig{MaxRetries: item.retries}, config.TranslateConfig{TargetLang: "Simplified Chinese"}, runner)
			if _, err := translator.Translate(context.Background(), filepath.Join(root, "movie.srt")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLLMSubtransPassesConfiguredRateLimit(t *testing.T) {
	for _, item := range []struct {
		name string
		rate float64
		want string
	}{
		{"unlimited", 0, ""},
		{"integer RPM", 80, "80"},
		{"fractional RPM", 0.5, "0.5"},
	} {
		t.Run(item.name, func(t *testing.T) {
			root := t.TempDir()
			input := filepath.Join(root, "movie.srt")
			runner := &fakeRunner{onStream: func(_ string, args ...string) (string, string, error) {
				got := ""
				for i, arg := range args {
					if arg == "--ratelimit" {
						got = args[i+1]
					}
				}
				if got != item.want {
					t.Fatalf("ratelimit=%q want=%q", got, item.want)
				}
				if err := os.WriteFile(filepath.Join(root, "movie.zh.srt"), []byte("translation"), 0644); err != nil {
					t.Fatal(err)
				}
				return "", "", nil
			}}
			translator := NewLLMSubtrans(config.LLMSubtransConfig{RateLimit: item.rate}, config.TranslateConfig{TargetLang: "Simplified Chinese"}, runner)
			if _, err := translator.Translate(context.Background(), input); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFormatCommandLog(t *testing.T) {
	args := []string{
		"/app/scripts/llm_subtrans_translate.py",
		"--input", "/data/sub.srt",
		"--output", "/data/sub.zh.srt",
		"--target", "Simplified Chinese",
		"--api-key", "secret-key-12345",
		"--base-url", "http://192.168.1.1:3425/v1",
		"--model", "model-name",
		"--instruction", "Translate faithfully.\nLine 2.",
		"--max-retries", "10",
		"--request-timeout", "300",
		"--ratelimit", "60",
	}

	got := formatCommandLog("python3", args)
	want := `python3 /app/scripts/llm_subtrans_translate.py --input /data/sub.srt --output /data/sub.zh.srt --target "Simplified Chinese" --api-key *** --base-url http://192.168.1.1:3425/v1 --model model-name --instruction "Translate faithfully.\nLine 2." --max-retries 10 --request-timeout 300 --ratelimit 60`

	if got != want {
		t.Fatalf("formatCommandLog() =\n%s\nwant:\n%s", got, want)
	}
}
