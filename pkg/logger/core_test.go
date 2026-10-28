package logger

import (
	"bytes"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestCoreKeepsEntryStackEscaped(t *testing.T) {
	var buf bytes.Buffer
	core := newLineCore("svc", ERROR, zapcore.AddSync(&buf))
	e := zapcore.Entry{
		Time:    at(3, 456),
		Level:   zapcore.ErrorLevel,
		Message: "boom",
		Stack:   "goroutine 1 [running]:\nmain.main()\n\tmain.go:12",
	}
	fs := []zapcore.Field{zap.String("component", "c"), zap.String("event", "e")}
	if err := core.Write(e, fs); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimRight(buf.String(), "\n")
	want := `2026-07-01T14:20:03.456+08:00 ERROR component=c event=e stack="goroutine 1 [running]:\nmain.main()\n\tmain.go:12" msg="boom"`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
	if strings.Contains(got, "\n") {
		t.Error("stack must stay escaped on one physical line")
	}
}

func TestCoreExplicitStackWinsOverAuto(t *testing.T) {
	var buf bytes.Buffer
	core := newLineCore("svc", ERROR, zapcore.AddSync(&buf))
	e := zapcore.Entry{Time: at(3, 456), Level: zapcore.ErrorLevel, Message: "boom", Stack: "auto-stack"}
	fs := []zapcore.Field{
		zap.String("component", "c"),
		zap.String("event", "e"),
		zap.String("stack", "explicit-stack"),
	}
	if err := core.Write(e, fs); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, `stack="explicit-stack"`) || strings.Contains(got, "auto-stack") {
		t.Errorf("explicit stack must win: %q", got)
	}
}

func TestCoreStackBoundedAndRedacted(t *testing.T) {
	var buf bytes.Buffer
	core := newLineCore("svc", ERROR, zapcore.AddSync(&buf))
	e := zapcore.Entry{
		Time:    at(3, 456),
		Level:   zapcore.ErrorLevel,
		Message: "boom",
		Stack:   "token=sk-test123 " + strings.Repeat("f", 4200),
	}
	if err := core.Write(e, nil); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "sk-test123") {
		t.Errorf("stack not redacted: %q", got)
	}
	if !strings.Contains(got, `truncated=true`) {
		t.Errorf("stack truncation flag missing: %q", got)
	}
	start := strings.Index(got, `stack="`) + len(`stack="`)
	end := strings.Index(got[start:], `"`)
	if body := got[start : start+end]; len([]rune(body)) != 4096 {
		t.Errorf("stack body has %d runes, want 4096", len([]rune(body)))
	}
}

func TestDisplayLevel(t *testing.T) {
	for lvl, want := range map[zapcore.Level]string{
		zapcore.DebugLevel:  "DEBUG",
		zapcore.InfoLevel:   "INFO",
		zapcore.WarnLevel:   "WARN",
		zapcore.ErrorLevel:  "ERROR",
		zapcore.DPanicLevel: "ERROR",
		zapcore.PanicLevel:  "ERROR",
		zapcore.FatalLevel:  "ERROR",
	} {
		if got := displayLevel(lvl); got != want {
			t.Errorf("displayLevel(%v) = %q, want %q", lvl, got, want)
		}
	}
}
