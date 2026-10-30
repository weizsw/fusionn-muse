package executor

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/toolrun"
	"github.com/fusionn-muse/pkg/logger"
)

// stubScriptRunner keeps every production piece of the stream path — the real
// ExecRunner, a real child process, the capture and the logger — and replaces
// only the container script path with a stub.
type stubScriptRunner struct{ script string }

func (stubScriptRunner) Run(context.Context, string, ...string) error { return nil }

func (stubScriptRunner) Output(context.Context, string, ...string) ([]byte, error) { return nil, nil }

func (r stubScriptRunner) Stream(ctx context.Context, name string, stdout, stderr io.Writer, args ...string) error {
	args = append([]string{r.script}, args[1:]...)
	return toolrun.ExecRunner{}.Stream(ctx, name, stdout, stderr, args...)
}

// stubScript writes the stub translation script and returns its path. The stub
// writes the real unified lines and the real machine result: failure with a
// safe exception, or a successful translation with ordinary library errors.
func stubScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "llm_subtrans_stub.py")
	if err := os.WriteFile(path, []byte(stubScriptSource), 0o755); err != nil {
		t.Fatalf("write stub script: %v", err)
	}
	return path
}

const stubScriptSource = `import json, os, sys

args = sys.argv[1:]
output = args[args.index("--output") + 1]
mode = os.environ.get("MUSE_STUB_MODE", "fail")


def line(level, event, fields=""):
    stamp = "2026-07-01T14:20:09.456"
    service = "service=fusionn-muse " if os.environ.get("MUSE_STUB_LEGACY") == "1" else ""
    suffix = " " + fields if fields else ""
    return "%s %s %scomponent=llm-subtrans event=%s%s msg=\"failed\"" % (
        stamp, level, service, event, suffix)


if mode == "succeed":
    sys.stderr.write(line("INFO", "subtitle_sanitized") + "\n")
    sys.stderr.write(line("INFO", "log", "logger=PySubtrans") + "\n")
    sys.stderr.write(line("ERROR", "log", "logger=httpx") + "\n")
    with open(output, "w", encoding="utf-8") as handle:
        handle.write("1\n00:00:00,000 --> 00:00:01,000\n你好\n")
    raise SystemExit(0)

if mode == "crash":
    # A CR progress fragment immediately followed by the control event, with no
    # LF between them. The previous release judged this a failure too.
    sys.stderr.write("progress 100%\r")
    sys.stderr.write(line("ERROR", "script_failure_detected") + "\n")
    raise SystemExit(0)

if mode == "unseparated":
    # The script's own adapter starts its records on a fresh line, so a partial
    # line from a library cannot hide the control record. The run exits 0 with a
    # valid translation, so only that record can fail it.
    sys.stderr.write("progress 100%")
    sys.stderr.write("\n" + line("ERROR", "script_failure_detected") + "\n")
    with open(output, "w", encoding="utf-8") as handle:
        handle.write("1\n00:00:00,000 --> 00:00:01,000\n你好\n")
    raise SystemExit(0)

frames = "  File \"a b.py\", line 7, in f\n  File \"c\\d.py\", line 8, in g\n"
sys.stderr.write(line("ERROR", "translation_failed",
    "error=\"ModuleNotFoundError\" errno=2 module=PySubtrans stack=%s" % json.dumps(frames)) + "\n")
sys.stderr.write(line("ERROR", "script_failure_detected") + "\n")
raise SystemExit(1)
`

const stubFrames = "  File \"a b.py\", line 7, in f\n  File \"c\\d.py\", line 8, in g\n"

func newStubTranslator(t *testing.T) *LLMSubtrans {
	t.Helper()
	logger.Init(true)
	return NewLLMSubtrans(config.LLMSubtransConfig{}, config.TranslateConfig{TargetLang: "Simplified Chinese"},
		stubScriptRunner{script: stubScript(t)})
}

func TestLLMSubtransStubFailureReachesTheWorkerFacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		legacy bool
	}{
		{"current header", false},
		{"legacy header", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("MUSE_STUB_MODE", "fail")
			if test.legacy {
				t.Setenv("MUSE_STUB_LEGACY", "1")
			}
			translator := newStubTranslator(t)

			var translateErr error
			lines := captureLogOutput(t, func() {
				_, translateErr = translator.Translate(context.Background(), filepath.Join(root, "movie.srt"))
			})

			var diagnostics ScriptDiagnostics
			if !errors.As(translateErr, &diagnostics) {
				t.Fatalf("error = %v, want the script's safe diagnostics", translateErr)
			}
			if diagnostics.ErrorType != "ModuleNotFoundError" || diagnostics.Module != "PySubtrans" || diagnostics.Errno != 2 {
				t.Fatalf("diagnostics = %#v, want the script's structural facts", diagnostics)
			}
			if diagnostics.Frames != stubFrames {
				t.Fatalf("frames = %q, want %q", diagnostics.Frames, stubFrames)
			}
			if !strings.Contains(translateErr.Error(), "exit status 1") {
				t.Fatalf("error = %v, want the real exit status", translateErr)
			}
			for _, line := range lines {
				for _, consumed := range []string{"event=translation_failed", "event=script_failure_detected"} {
					if strings.Contains(line, consumed) {
						t.Fatalf("line %q displays the script's consumed record", line)
					}
				}
			}
		})
	}
}

func TestLLMSubtransStubControlEventAfterCRProgressStillFails(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MUSE_STUB_MODE", "crash")
	translator := newStubTranslator(t)

	_, err := translator.Translate(context.Background(), filepath.Join(root, "movie.srt"))
	if err == nil || !strings.Contains(err.Error(), "reported errors") {
		t.Fatalf("error = %v, want the control event to fail the translation", err)
	}
}

func TestLLMSubtransStubControlEventAfterUnseparatedProgressStillFails(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MUSE_STUB_MODE", "unseparated")
	translator := newStubTranslator(t)

	_, err := translator.Translate(context.Background(), filepath.Join(root, "movie.srt"))
	if err == nil || !strings.Contains(err.Error(), "reported errors") {
		t.Fatalf("error = %v, want the control event to fail the translation", err)
	}
}

func TestLLMSubtransStubSuccessWithLibraryErrorsStillSucceeds(t *testing.T) {
	root := t.TempDir()
	t.Setenv("MUSE_STUB_MODE", "succeed")
	translator := newStubTranslator(t)

	var translated string
	var translateErr error
	lines := captureLogOutput(t, func() {
		translated, translateErr = translator.Translate(context.Background(), filepath.Join(root, "movie.srt"))
	})

	if translateErr != nil {
		t.Fatalf("Translate returned error: %v", translateErr)
	}
	want := filepath.Join(root, "movie.zh.srt")
	if translated != want {
		t.Fatalf("translated = %q, want %q", translated, want)
	}
	if info, err := os.Stat(want); err != nil || info.Size() == 0 {
		t.Fatalf("translated file missing or empty: %v", err)
	}

	sanitized, summary := 0, ""
	for _, line := range lines {
		if strings.Contains(line, "event=subtitle_sanitized") {
			sanitized++
		}
		if strings.Contains(line, "event=tool_output_summary") {
			summary = line
		}
		for _, replayed := range []string{"logger=PySubtrans", "logger=httpx", "你好"} {
			if strings.Contains(line, replayed) {
				t.Fatalf("line %q replays the script's own output", line)
			}
		}
	}
	if sanitized != 1 {
		t.Fatalf("subtitle_sanitized lines = %d, want exactly one re-emitted summary", sanitized)
	}
	if !strings.Contains(summary, " WARN ") || !strings.Contains(summary, "log_lines=2") {
		t.Fatalf("summary line = %q, want the counted library records at WARN", summary)
	}
	t.Logf("re-emitted summary: %s", summary)
}
