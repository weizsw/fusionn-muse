package logger

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func detailOf(t *testing.T, f zap.Field) string {
	t.Helper()
	if f.Type == zapcore.SkipType {
		return ""
	}
	if f.Key != "detail" || f.Type != zapcore.StringType {
		t.Fatalf("field = %+v, want a detail string", f)
	}
	return f.String
}

func TestFailureDetailKeepsTheLastErrorLines(t *testing.T) {
	output := strings.Join([]string{
		"ffmpeg version 7.1 Copyright (c) 2000-2024",
		"  configuration: --enable-gpl",
		"[matroska @ 0x1] Error 1: first",
		"frame=  100 fps=0.0 q=-1.0 size=N/A time=00:00:04.00",
		"[matroska @ 0x1] Error 2: second",
		"[matroska @ 0x1] Error 3: third",
		"Error opening input: Invalid data found when processing input",
		"Exiting normally, received signal 2.",
		"",
	}, "\n")
	got := detailOf(t, FailureDetail(output))
	want := "[matroska @ 0x1] Error 2: second | [matroska @ 0x1] Error 3: third | " +
		"Error opening input: Invalid data found when processing input"
	if got != want {
		t.Errorf("detail =\n %q\nwant\n %q", got, want)
	}
}

func TestFailureDetailSkipsTracebackFrames(t *testing.T) {
	cases := map[string]string{
		"frame with source": "Traceback (most recent call last):\n" +
			"  File \"/app/errors.py\", line 3, in <module>\n" +
			"    raise RuntimeError(\"wrapped\")\n" +
			"    ~~~~~^^^^^^^^^^^^^\n" +
			"ModuleNotFoundError: No module named 'pysubtrans'\n",
		// A frame without a source line must not hide the exception line.
		"frame without source": "Traceback (most recent call last):\n" +
			"  File \"<string>\", line 1, in <module>\n" +
			"ModuleNotFoundError: No module named 'pysubtrans'\n",
	}
	for name, output := range cases {
		if got, want := detailOf(t, FailureDetail(output)), "ModuleNotFoundError: No module named 'pysubtrans'"; got != want {
			t.Errorf("%s: detail = %q, want %q", name, got, want)
		}
	}
}

func TestFailureDetailWithoutErrorLinesIsSkipped(t *testing.T) {
	output := "Translating scene 1 batch 2\n1\n00:00:01,000 --> 00:00:02,000\nHello there\n"
	if got := detailOf(t, FailureDetail(output)); got != "" {
		t.Errorf("detail = %q, want no field for output without error lines", got)
	}
	if got := detailOf(t, FailureDetail("")); got != "" {
		t.Errorf("detail of empty output = %q", got)
	}
}

func TestFailureDetailCleansLines(t *testing.T) {
	output := "frame=1\rframe=2\r\x1b[31mError while decoding stream #0:1\x1b[0m\n" +
		"Error while decoding stream #0:1\n" +
		"fatal: auth rejected for key OPAQUE-KEY-123\n"
	got := detailOf(t, FailureDetail(output, "OPAQUE-KEY-123", ""))
	if want := "Error while decoding stream #0:1 | fatal: auth rejected for key ***"; got != want {
		t.Errorf("detail = %q, want %q", got, want)
	}
}

// The newest error line is the one closest to the failure, so it is always
// kept; older lines are added only while the total stays within the cap.
func TestFailureDetailKeepsTheNewestLineWithinTheCap(t *testing.T) {
	older := "Error: older " + strings.Repeat("o", 150)
	newest := "Error: newest " + strings.Repeat("n", 400)
	got := detailOf(t, FailureDetail(older+"\n"+newest+"\n"))
	if !strings.HasPrefix(got, "Error: newest ") || !strings.HasSuffix(got, "…") || strings.Contains(got, "older") {
		t.Errorf("detail = %q, want only the newest line, cut with an ellipsis", got)
	}
	if n := utf8.RuneCountInString(got); n > detailMaxRunes {
		t.Errorf("detail has %d runes, want at most %d", n, detailMaxRunes)
	}
}

// detail is a failure excerpt for non-DEBUG levels: unlike preview it shows at
// every level, and the renderer quotes, redacts and caps it whatever the
// caller passed.
func TestDetailRendersAtEveryLevelQuotedRedactedAndCapped(t *testing.T) {
	buf := capture(t, "WARN")
	log := Component("executor")
	log.Warn(context.Background(), "tool_completed", "Tool failed", zap.String("detail", "Killed"))
	log.Warn(context.Background(), "tool_completed", "Tool failed",
		zap.String("detail", "Error: token=abc123 "+strings.Repeat("x", 400)))

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %q", buf.String())
	}
	if !strings.Contains(lines[0], ` detail="Killed" `) {
		t.Errorf("detail is not quoted: %s", lines[0])
	}
	if strings.Contains(lines[1], "abc123") || !strings.Contains(lines[1], ` detail="Error: token=*** `) {
		t.Errorf("detail is not redacted: %s", lines[1])
	}
	if !strings.Contains(lines[1], " truncated=true ") {
		t.Errorf("capped detail lacks truncated=true: %s", lines[1])
	}
	start := strings.Index(lines[1], ` detail="`) + len(` detail="`)
	value := lines[1][start : start+strings.Index(lines[1][start:], `"`)]
	if n := utf8.RuneCountInString(value); n != detailMaxRunes {
		t.Errorf("rendered detail has %d runes, want the %d cap", n, detailMaxRunes)
	}
}
