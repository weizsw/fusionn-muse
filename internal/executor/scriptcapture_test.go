package executor

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/fusionn-muse/internal/toolrun"
	"github.com/fusionn-muse/pkg/logger"
)

// scriptLine renders one line of the unified format, the way the translation
// script writes it today: the service field is gone, so the component follows
// the level directly.
func scriptLine(level, event, fields string) string {
	line := "2026-07-01T14:20:09.456 " + level +
		" component=llm-subtrans event=" + event
	if fields != "" {
		line += " " + fields
	}
	return line + ` msg="failed"`
}

func captureText(text string) *scriptCapture {
	capture := &scriptCapture{}
	_, _ = io.WriteString(capture, text)
	capture.close()
	return capture
}

func capturePieces(pieces ...string) *scriptCapture {
	capture := &scriptCapture{}
	for _, piece := range pieces {
		_, _ = io.WriteString(capture, piece)
	}
	capture.close()
	return capture
}

// captureLogOutput replaces the process logger with a buffer for the duration
// of fn and returns the rendered lines.
func captureLogOutput(t *testing.T, fn func()) []string {
	t.Helper()
	var buf bytes.Buffer
	t.Setenv("LOG_LEVEL", "DEBUG")
	logger.Configure(logger.Config{Output: zapcore.AddSync(&buf)})
	fn()
	rendered := strings.TrimSuffix(buf.String(), "\n")
	if rendered == "" {
		return nil
	}
	return strings.Split(rendered, "\n")
}

func TestScriptCaptureVerdict(t *testing.T) {
	tests := []struct {
		name   string
		pieces []string
		want   bool
	}{
		{"empty output", []string{""}, false},
		{"the script's constant failure event", []string{scriptLine("ERROR", "script_failure_detected", "") + "\n"}, true},
		{"the control event is read from the header, not from a quoted value",
			[]string{scriptLine("INFO", "translation_started", `preview="event=script_failure_detected"`) + "\n"}, false},
		{"a header with the offset that older releases wrote is still ours",
			[]string{"2026-07-01T14:20:09.456+08:00 ERROR component=llm-subtrans event=script_failure_detected msg=\"failed\"\n"}, true},
		{"a header from before the service field was removed is still ours",
			[]string{"2026-07-01T14:20:09.456 ERROR service=fusionn-muse component=llm-subtrans event=script_failure_detected msg=\"failed\"\n"}, true},
		{"a quoted control event in a legacy header is not the real record",
			[]string{"2026-07-01T14:20:09.456 INFO service=fusionn-muse component=llm-subtrans event=translation_started preview=\"event=script_failure_detected\" msg=\"failed\"\n"}, false},
		{"a foreign service with our component is not ours",
			[]string{"2026-07-01T14:20:09.456 ERROR service=fusionn-subs component=llm-subtrans event=script_failure_detected msg=\"theirs\"\n"}, false},
		{"a foreign component without the legacy prefix keeps the raw verdict",
			[]string{"2026-07-01T14:20:09.456 ERROR component=other event=translation_failed Traceback: body\n"}, true},
		{"the control event in a message is not a failure",
			[]string{scriptLine("INFO", "translation_started", "") + "\n"}, false},
		{"a foreign service keeps the raw verdict",
			[]string{"2026-07-01T14:20:09.456 ERROR service=fusionn-subs component=translator event=translation_failed Error: body" + "\n"}, true},
		{"a foreign component of our own service keeps the raw verdict",
			[]string{"2026-07-01T14:20:09.456 ERROR service=fusionn-muse component=other event=translation_failed Traceback: body" + "\n"}, true},
		{"a foreign service control event is not ours",
			[]string{"2026-07-01T14:20:09.456 ERROR service=fusionn-subs component=translator event=script_failure_detected msg=\"theirs\"" + "\n"}, false},
		{"a foreign service without a legacy marker is not a failure",
			[]string{"2026-07-01T14:20:09.456 ERROR service=fusionn-subs component=translator event=translation_failed msg=\"theirs\"" + "\n"}, false},
		{"a quoted translation_failed token is not the real record",
			[]string{scriptLine("INFO", "translation_started", `token="event=translation_failed"`) + "\n"}, false},
		{"a normalized line is never searched for failure text",
			[]string{scriptLine("DEBUG", "llm_response_preview", `preview="Error: Traceback"`) + "\n"}, false},
		{"a normalized error line is not a failure", []string{scriptLine("ERROR", "translation_failed", "") + "\n"}, false},
		{"unformatted output keeps the original predicate", []string{"Error: raw tail\n"}, true},
		{"an unformatted traceback keeps the original predicate", []string{"Traceback (most recent call last):\n"}, true},
		{"the legacy markers stay case sensitive", []string{"error: lowercase\n"}, false},
		{"a final line without a newline is still examined", []string{"noise\nError: raw tail"}, true},
		{"clean unformatted output", []string{"harmless raw tail"}, false},
		{"a marker split across writes is still found", []string{"raw Err", "or: body\n"}, true},
		{"a CR progress line does not merge with the next control event",
			[]string{"progress 100%\r" + scriptLine("ERROR", "script_failure_detected", "") + "\n"}, true},
		{"a CR progress line does not merge with the next legacy line",
			[]string{"progress 100%\r", "Error: raw tail\n"}, true},
		{"CRLF ends a line once",
			[]string{"progress 100%\r\n" + scriptLine("ERROR", "script_failure_detected", "") + "\r\n"}, true},
		{"a CRLF split across writes ends a line once",
			[]string{"progress 100%\r", "\n" + scriptLine("ERROR", "script_failure_detected", "") + "\n"}, true},
		{"a marker split across writes of the same line", []string{"raw Trace", "back\n"}, true},
		{"the verdict survives a long tail that is dropped",
			[]string{scriptLine("ERROR", "script_failure_detected", "") + "\n", strings.Repeat("x", 2*maxLineBytes)}, true},
		{"a marker past the retention bound is still found",
			[]string{strings.Repeat("a", maxLineBytes+100) + "Traceback and more\n"}, true},
		{"a normalized body past the retention bound is not searched",
			[]string{scriptLine("DEBUG", "llm_response_preview", `preview="`+strings.Repeat("x", maxLineBytes)+`Error:"`) + "\n"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := capturePieces(test.pieces...).verdict(); got != test.want {
				t.Fatalf("verdict = %v, want %v", got, test.want)
			}
		})
	}
}

// realRendererProgram prints one line with the repository's own renderer. The
// capture is then linked to the format the script actually writes, so a change
// the hand-written test lines miss cannot stay green on both sides.
const realRendererProgram = `import sys
sys.path.insert(0, sys.argv[2])
import logfmt

log = logfmt.configure(service="fusionn-muse", component="llm-subtrans", level="DEBUG")
if sys.argv[1] == "control":
    log.error("script_failure_detected", "Script output matched")
else:
    log.debug("llm_response_preview", "LLM response preview", preview="event=script_failure_detected")
`

func TestScriptCaptureReadsTheRealRendererHeader(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	scripts, err := filepath.Abs(filepath.Join("..", "..", "scripts"))
	if err != nil {
		t.Fatalf("resolve the scripts directory: %v", err)
	}
	for _, test := range []struct {
		mode string
		want bool
	}{
		{"control", true},
		{"preview", false},
	} {
		t.Run(test.mode, func(t *testing.T) {
			capture := &scriptCapture{}
			err := toolrun.ExecRunner{}.Stream(context.Background(), python, io.Discard, capture,
				"-B", "-c", realRendererProgram, test.mode, scripts)
			capture.close()
			if err != nil {
				t.Fatalf("run the renderer: %v", err)
			}
			if got := capture.verdict(); got != test.want {
				t.Fatalf("verdict = %v, want %v", got, test.want)
			}
		})
	}
}

func TestScriptCaptureFindsAMarkerAtEverySplit(t *testing.T) {
	raw := "noise Traceback body"
	for first := 0; first <= len(raw); first++ {
		for second := first; second <= len(raw); second++ {
			capture := capturePieces(raw[:first], raw[first:second], raw[second:])
			if !capture.verdict() {
				t.Fatalf("splits %d/%d: verdict = false, want the marker found", first, second)
			}
		}
	}
}

func TestScriptCaptureFindsAMarkerStraddlingTheRetentionBound(t *testing.T) {
	// One long write places the marker across the retention bound: the retained
	// prefix ends with the marker's first characters and the rest of the marker
	// arrives in the same write. A carry taken from the head of that write, rather
	// than its tail, loses the marker.
	line := strings.Repeat("x", maxLineBytes-len("Trace")) + "Traceback"
	capture := capturePieces(line)
	if !capture.verdict() {
		t.Fatal("verdict = false, want the marker across the retention bound found")
	}
}

func TestScriptCaptureFindsAMarkerWrittenByteByByte(t *testing.T) {
	line := strings.Repeat("x", maxLineBytes+1) + "Traceback"
	capture := &scriptCapture{}
	for i := 0; i < len(line); i++ {
		_, _ = capture.Write([]byte(line[i : i+1]))
	}
	capture.close()
	if !capture.verdict() {
		t.Fatal("verdict = false, want the marker found in one-byte writes")
	}
}

func TestScriptCaptureFindsAMarkerSplitAcrossManyWrites(t *testing.T) {
	for _, marker := range []string{"Traceback", "Error:"} {
		t.Run(marker, func(t *testing.T) {
			capture := &scriptCapture{}
			// One raw line past the retention bound, so the marker is only found
			// by the incremental scan, never by the retained prefix.
			_, _ = capture.Write([]byte(strings.Repeat("x", maxLineBytes+1)))
			for i := 0; i < len(marker); i++ {
				_, _ = capture.Write([]byte(marker[i : i+1]))
			}
			_, _ = capture.Write([]byte(" body\n"))
			capture.close()
			if !capture.verdict() {
				t.Fatalf("verdict = false, want the marker %q found across single-byte writes", marker)
			}
		})
	}
}

func TestScriptCaptureDoesNotReadAnEventOutOfRawText(t *testing.T) {
	// A raw fragment with no separator merges with the next record; a header is
	// only read at a line start, so raw text can never forge a control event.
	// The translation script starts its own records on a fresh line.
	capture := capturePieces("progress 100%" + scriptLine("ERROR", "script_failure_detected", "") + "\n")
	if capture.verdict() {
		t.Fatal("verdict = true, want raw text never read as a control event")
	}
}

func TestScriptCaptureBoundsWhatItRetains(t *testing.T) {
	capture := capturePieces(strings.Repeat("a", 4<<20) + "\n")
	if len(capture.pending) > maxLineBytes {
		t.Fatalf("retained %d bytes, want at most %d", len(capture.pending), maxLineBytes)
	}
	if capture.dropped != 1 {
		t.Fatalf("dropped = %d, want 1", capture.dropped)
	}
	if capture.rawLines != 1 {
		t.Fatalf("raw lines = %d, want 1", capture.rawLines)
	}
}

func TestScriptCaptureCountsWhatItDoesNotShow(t *testing.T) {
	capture := captureText(strings.Join([]string{
		scriptLine("INFO", "subtitle_sanitized", ""),
		scriptLine("INFO", "log", `logger=PySubtrans`),
		scriptLine("WARN", "log", `logger=httpx`),
		scriptLine("INFO", "translation_started", ""),
		"raw legacy line",
		"2026-07-01T14:20:09.456 INFO service=fusionn-subs component=translator event=job_started msg=\"theirs\"",
		"",
	}, "\n"))

	if !capture.sanitized {
		t.Fatal("sanitized summary event was not recorded")
	}
	if capture.logLines != 2 || capture.logWarnings != 1 {
		t.Fatalf("log lines = %d (warnings %d), want 2 (1)", capture.logLines, capture.logWarnings)
	}
	if capture.otherEvents != 1 {
		t.Fatalf("other events = %d, want 1", capture.otherEvents)
	}
	if capture.rawLines != 2 {
		t.Fatalf("raw lines = %d, want 2; another service's line is not ours", capture.rawLines)
	}
}

func TestScriptCaptureDecodesDiagnosticsExactlyOnce(t *testing.T) {
	frames := "  File \"C:\\tmp\\a.py\", line 1, in f\n  File \"b.py\", line 2, in g\n"
	line := scriptLine("ERROR", "translation_failed", strings.Join([]string{
		"error=" + strconv.Quote("ModuleNotFoundError"),
		"errno=2",
		"module=PySubtrans",
		"stack=" + strconv.Quote(frames),
		"strerror=" + strconv.Quote("No such file or directory"),
	}, " "))

	diagnostics := captureText(line).safeDiagnostics()
	if diagnostics.ErrorType != "ModuleNotFoundError" {
		t.Fatalf("error type = %q", diagnostics.ErrorType)
	}
	if diagnostics.Module != "PySubtrans" {
		t.Fatalf("module = %q", diagnostics.Module)
	}
	if diagnostics.Errno != 2 {
		t.Fatalf("errno = %d, want 2", diagnostics.Errno)
	}
	if diagnostics.Strerror != "No such file or directory" {
		t.Fatalf("strerror = %q", diagnostics.Strerror)
	}
	if diagnostics.Frames != frames {
		t.Fatalf("frames = %q, want %q", diagnostics.Frames, frames)
	}
}

func TestScriptCaptureReadsOnlyRealFields(t *testing.T) {
	// A value that contains field-shaped text must not be read as those fields.
	line := scriptLine("ERROR", "translation_failed", strings.Join([]string{
		"preview=" + strconv.Quote(` stack="forged" error="Forged" module=Forged `),
		"error=" + strconv.Quote("RealError"),
		"module=" + strconv.Quote("RealModule"),
	}, " "))

	diagnostics := captureText(line).safeDiagnostics()
	if diagnostics.ErrorType != "RealError" || diagnostics.Module != "RealModule" {
		t.Fatalf("diagnostics = %#v, want the real fields", diagnostics)
	}
	if diagnostics.Frames != "" {
		t.Fatalf("frames = %q, want nothing taken from the quoted value", diagnostics.Frames)
	}
}

func TestScriptCaptureFlagsACutRecord(t *testing.T) {
	line := scriptLine("ERROR", "translation_failed", "error="+strconv.Quote("LongError")+" stack="+strconv.Quote(strings.Repeat("x", maxLineBytes)))
	diagnostics := captureText(line + "\n").safeDiagnostics()
	if !diagnostics.Truncated {
		t.Fatal("truncated = false, want true for a record cut by the retention bound")
	}
}

func TestScriptCaptureFramesRoundTripThroughTheRenderer(t *testing.T) {
	frames := "  File \"a b.py\", line 7, in f\n  File \"c\\d.py\", line 8, in g\n"
	line := scriptLine("ERROR", "translation_failed",
		"error="+strconv.Quote("RuntimeError")+" stack="+strconv.Quote(frames))

	diagnostics := captureText(line).safeDiagnostics()
	lines := captureLogOutput(t, func() {
		ctx := logger.WithJobID(context.Background(), "job-1")
		fields := append([]zap.Field{zap.Error(diagnostics)}, diagnostics.Fields()...)
		logger.Component("llm-subtrans").Error(ctx, "job_failed", "Job failed", fields...)
	})

	if len(lines) != 1 {
		t.Fatalf("rendered %d lines, want 1: %q", len(lines), lines)
	}
	rendered := lines[0]
	if !strings.Contains(rendered, " job_id=job-1 ") {
		t.Fatalf("rendered line = %q, want the job id", rendered)
	}
	// A double-escaped newline is the old bug: the frames must be quoted once.
	if strings.Contains(rendered, `\\n`) {
		t.Fatalf("rendered line double-escapes the frames: %q", rendered)
	}
	value, ok := quotedFieldValue(rendered, "stack")
	if !ok {
		t.Fatalf("rendered line = %q, want a stack field", rendered)
	}
	if value != frames {
		t.Fatalf("stack = %q, want %q", value, frames)
	}
}

// quotedFieldValue decodes one quoted field value from a rendered line, the way
// any reader of the unified format would.
func quotedFieldValue(line, key string) (string, bool) {
	parts := strings.SplitN(line, " "+key+"=", 2)
	if len(parts) != 2 {
		return "", false
	}
	quoted, err := strconv.QuotedPrefix(parts[1])
	if err != nil {
		return "", false
	}
	value, err := strconv.Unquote(quoted)
	if err != nil {
		return "", false
	}
	return value, true
}

func TestScriptCaptureReportCountsWithoutReplaying(t *testing.T) {
	secret := "sk-abcdefghijklmnop"
	capture := captureText(strings.Join([]string{
		scriptLine("INFO", "subtitle_sanitized", ""),
		scriptLine("INFO", "log", `logger=PySubtrans`),
		scriptLine("WARN", "log", `logger=httpx`),
		"raw line with a subtitle body and Authorization: Bearer " + secret,
		"",
	}, "\n"))

	lines := captureLogOutput(t, func() {
		ctx := logger.WithJobID(context.Background(), "job-1")
		capture.report(ctx, logger.Component("llm-subtrans"))
	})

	if len(lines) != 2 {
		t.Fatalf("reported %d lines, want the summary and the sanitized event: %q", len(lines), lines)
	}
	var summary, sanitized string
	for _, line := range lines {
		switch {
		case strings.Contains(line, "event=tool_output_summary"):
			summary = line
		case strings.Contains(line, "event=subtitle_sanitized"):
			sanitized = line
		default:
			t.Fatalf("unexpected report line %q", line)
		}
	}
	if summary == "" || sanitized == "" {
		t.Fatalf("missing report lines: %q", lines)
	}
	if !strings.Contains(summary, " WARN ") {
		t.Fatalf("summary line = %q, want WARN for a warned child record", summary)
	}
	for _, want := range []string{"log_lines=2", "raw_lines=1"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary line = %q, want %q", summary, want)
		}
	}
	if !strings.Contains(sanitized, " job_id=job-1 ") {
		t.Fatalf("sanitized line = %q, want the parent correlation", sanitized)
	}
	for _, line := range lines {
		if strings.Contains(line, "subtitle body") || strings.Contains(line, secret) {
			t.Fatalf("report replays the child's raw text: %q", line)
		}
	}
}
