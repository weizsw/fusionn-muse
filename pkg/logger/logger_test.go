package logger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func capture(t *testing.T, level string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	t.Setenv("LOG_LEVEL", level)
	Configure(Config{Output: zapcore.AddSync(&buf)})
	t.Cleanup(func() { Configure(Config{Output: zapcore.AddSync(&bytes.Buffer{})}) })
	return &buf
}

var lineRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3} `)

func TestComponentEventFormat(t *testing.T) {
	buf := capture(t, "")
	log := Component("worker")
	ctx := WithJobID(context.Background(), "j1")
	log.Info(ctx, "job_started", "Job started")

	got := strings.TrimRight(buf.String(), "\n")
	want := ` INFO component=worker event=job_started job_id=j1 msg="Job started"`
	if !lineRe.MatchString(got) || !strings.HasSuffix(got, want) {
		t.Errorf("\n got: %s\nwant suffix: %s", got, want)
	}
}

func TestLogLevelEnv(t *testing.T) {
	buf := capture(t, "WARN")
	log := Component("c")
	log.Info(context.Background(), "e", "hidden")
	log.Warn(context.Background(), "e", "shown")
	if strings.Contains(buf.String(), "hidden") {
		t.Error("INFO not suppressed at LOG_LEVEL=WARN")
	}
	if !strings.Contains(buf.String(), "WARN") || !strings.Contains(buf.String(), "shown") {
		t.Errorf("WARN missing: %q", buf.String())
	}
}

func TestInvalidLogLevelDiagnostic(t *testing.T) {
	buf := capture(t, "banana")
	out := buf.String()
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "event=config_invalid") ||
		!strings.Contains(out, "log_level=banana") {
		t.Errorf("missing config_invalid diagnostic: %q", out)
	}
	// Falls back to INFO: DEBUG suppressed, INFO visible.
	log := Component("c")
	log.Debug(context.Background(), "e", "hidden")
	log.Info(context.Background(), "e", "shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "shown") {
		t.Errorf("fallback level is not INFO: %q", buf.String())
	}
}

func TestReconfigureNoDuplication(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	Configure(Config{Output: zapcore.AddSync(&buf1)})
	Configure(Config{Output: zapcore.AddSync(&buf2)})
	Component("c").Info(context.Background(), "e", "once")
	if buf1.Len() != 0 {
		t.Errorf("old core still active: %q", buf1.String())
	}
	if n := strings.Count(buf2.String(), "\n"); n != 1 {
		t.Errorf("expected exactly one line, got %d: %q", n, buf2.String())
	}
}

func TestConcurrentContextIsolation(t *testing.T) {
	buf := capture(t, "INFO")
	log := Component("worker")
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			ctx := WithJobID(context.Background(), id)
			for i := 0; i < 200; i++ {
				log.Info(ctx, "tick", "working", zap.Int("i", i))
			}
		}(fmt.Sprintf("job-%d", g))
	}
	wg.Wait()
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 400 {
		t.Fatalf("expected 400 lines, got %d", len(lines))
	}
	for _, ln := range lines {
		hasA := strings.Contains(ln, "job_id=job-0")
		hasB := strings.Contains(ln, "job_id=job-1")
		if hasA == hasB {
			t.Fatalf("line has wrong correlation: %s", ln)
		}
	}
}

func TestLegacyAPIRoutesThroughCore(t *testing.T) {
	buf := capture(t, "")
	Infof("hello %s", "world")
	Infow("pair", "k", "v", "n", 1)
	out := buf.String()
	if !strings.Contains(out, `event=log_message msg="hello world"`) {
		t.Errorf("legacy Infof not rendered via core: %q", out)
	}
	if !strings.Contains(out, `k=v n=1 msg="pair"`) {
		t.Errorf("legacy Infow fields missing: %q", out)
	}
}

func TestObserverInjectionStillWorks(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	prev := Log
	Log = zap.New(core).Sugar()
	defer func() { Log = prev }()

	Infof("observed %d", 1)
	// Component events share the same write path: they reach the observer too.
	Component("worker").Info(WithJobID(context.Background(), "j1"), "job_started", "Job started")

	if logs.Len() != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", logs.Len(), logs.All())
	}
	entries := logs.All()
	if !strings.Contains(entries[0].Message, "observed 1") {
		t.Errorf("observer missed legacy log: %v", entries[0])
	}
	if entries[1].Message != "Job started" {
		t.Errorf("observer missed Component event: %v", entries[1])
	}
	var hasComponent, hasJob bool
	for _, f := range entries[1].Context {
		if f.Key == "component" && f.String == "worker" {
			hasComponent = true
		}
		if f.Key == "job_id" && f.String == "j1" {
			hasJob = true
		}
	}
	if !hasComponent || !hasJob {
		t.Errorf("Component fields missing from observer entry: %v", entries[1].Context)
	}
}

func TestNilSafety(t *testing.T) {
	prev := Log
	Log = nil
	defer func() { Log = prev }()
	Info("no panic")
	Warnf("no panic")
	Errorw("no panic", "k", 1)
	Sync()
}

func TestIndentedLoggerCompat(t *testing.T) {
	capture(t, "")
	log := Indent()
	log.Info("test indented info")
	log.Infof("test indented %s", "info")
	log.Debug("test indented debug")
	log.Warn("test indented warn")
	log.Error("test indented error")
	if IndentPrefix != "  ├─ " {
		t.Errorf("IndentPrefix changed: %q", IndentPrefix)
	}
}

func TestInfoWriterCompat(t *testing.T) {
	w := NewInfoWriter()
	if _, err := w.Write([]byte("line one\nline two\rprogress")); err != nil {
		t.Fatal(err)
	}
}

// DPanic on a non-development logger logs instead of panicking, and displays
// as ERROR (never DPANIC). The panic/exit behavior of zap is untouched.
func TestDPanicDoesNotPanicAndDisplaysError(t *testing.T) {
	buf := capture(t, "")
	Log.DPanicw("dp", "k", 1)
	out := buf.String()
	if !strings.Contains(out, " ERROR ") || strings.Contains(out, "DPANIC") {
		t.Errorf("DPanic must display as ERROR: %q", out)
	}
	if !strings.Contains(out, `msg="dp"`) {
		t.Errorf("DPanic message missing: %q", out)
	}
}

// Fatal keeps zap's exit behavior (code 1) and still emits a unified ERROR
// line. Runs in a subprocess so the test binary itself never exits.
func TestFatalExitsWithCode1(t *testing.T) {
	if os.Getenv("LOGGER_FATAL_HELPER") == "1" {
		Configure(Config{Output: zapcore.AddSync(os.Stdout)})
		Fatalf("fatal %s", "boom")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestFatalExitsWithCode1") //nolint:gosec // re-runs this test binary
	cmd.Env = append(os.Environ(), "LOGGER_FATAL_HELPER=1")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected exit error, got %v (output: %s)", err, out)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", exitErr.ExitCode())
	}
	if !strings.Contains(string(out), " ERROR ") || !strings.Contains(string(out), "fatal boom") {
		t.Errorf("fatal line missing from output: %s", out)
	}
}

func TestAttemptIDOmittedWhenUnknown(t *testing.T) {
	buf := capture(t, "")
	log := Component("c")
	log.Info(WithAttemptID(context.Background(), 0), "e", "zero")
	log.Info(WithAttemptID(context.Background(), -3), "e", "negative")
	log.Info(WithAttemptID(context.Background(), 42), "e", "real")
	out := buf.String()
	if strings.Contains(out, "attempt_id=0") || strings.Contains(out, "attempt_id=-3") {
		t.Errorf("unknown attempt IDs must be omitted: %q", out)
	}
	if !strings.Contains(out, "attempt_id=42") {
		t.Errorf("real attempt ID missing: %q", out)
	}
}

func TestRequestIDValidation(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"abc", true},
		{"a.b_c-d", true},
		{strings.Repeat("x", 64), true},
		{"", false},
		{strings.Repeat("x", 65), false},
		{"a/b", false},
		{"a b", false},
		{"中文", false},
	}
	for _, tt := range tests {
		if got := ValidRequestID(tt.id); got != tt.want {
			t.Errorf("ValidRequestID(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
	if !ValidRequestID(NewRequestID()) {
		t.Error("NewRequestID produced an invalid ID")
	}
}
