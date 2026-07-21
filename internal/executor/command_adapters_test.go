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

func init() {
	logger.Init(true)
}

type commandCall struct {
	name string
	args []string
}

type fakeRunner struct {
	streamCalls []commandCall
	onStream    func(name string, args ...string) (string, string, error)
}

func (*fakeRunner) Run(context.Context, string, ...string) error { return nil }

func (*fakeRunner) Output(context.Context, string, ...string) ([]byte, error) { return nil, nil }

func (r *fakeRunner) Stream(_ context.Context, name string, args ...string) (string, string, error) {
	r.streamCalls = append(r.streamCalls, commandCall{name: name, args: append([]string(nil), args...)})
	if r.onStream != nil {
		return r.onStream(name, args...)
	}
	return "", "", nil
}

func TestWhisperUsesInjectedRunnerForTranscriptionAndPostProcessing(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "movie.mp4")
	subtitle := filepath.Join(root, "movie.srt")
	runner := &fakeRunner{onStream: func(_ string, args ...string) (string, string, error) {
		if args[0] == transcribeScript {
			if err := os.WriteFile(subtitle, []byte("subtitle\n"), 0644); err != nil {
				t.Fatalf("write subtitle: %v", err)
			}
		}
		return "", "", nil
	}}
	whisper := NewWhisper(config.WhisperConfig{RemovePunctuation: true}, config.TranslateConfig{}, runner)

	got, err := whisper.Transcribe(context.Background(), video)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != subtitle {
		t.Fatalf("subtitle = %q, want %q", got, subtitle)
	}
	want := []commandCall{
		{name: "python3", args: []string{transcribeScript, video, subtitle, "--model", "large-v2", "--device", "auto"}},
		{name: "python3", args: []string{subtitleProcessorScript, subtitle, subtitle, "--remove-punctuation"}},
	}
	if !reflect.DeepEqual(runner.streamCalls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.streamCalls, want)
	}
}

func TestTranslatorUsesInjectedRunner(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "movie.srt")
	output := filepath.Join(root, "movie.zh.srt")
	runner := &fakeRunner{onStream: func(_ string, _ ...string) (string, string, error) {
		if err := os.WriteFile(output, []byte("translated\n"), 0644); err != nil {
			t.Fatalf("write translation: %v", err)
		}
		return "", "", nil
	}}
	translator := NewTranslator(config.TranslateConfig{TargetLang: "Simplified Chinese"}, runner)

	got, err := translator.Translate(context.Background(), input)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if got != output {
		t.Fatalf("output = %q, want %q", got, output)
	}
	want := []commandCall{{
		name: "python3",
		args: []string{"/app/scripts/translate.py", input, output, "--target", "Simplified Chinese", "--base-url", "https://api.openai.com"},
	}}
	if !reflect.DeepEqual(runner.streamCalls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.streamCalls, want)
	}
}
