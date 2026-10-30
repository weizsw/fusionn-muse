package logger

import (
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func at(sec, ms int) time.Time {
	return time.Date(2026, 7, 1, 14, 20, sec, ms*int(time.Millisecond), testZone)
}

func atMin(min, sec, ms int) time.Time {
	return time.Date(2026, 7, 1, 14, min, sec, ms*int(time.Millisecond), testZone)
}

// Golden samples from the spec (docs/design/unified-logging.md §3).
// Extra fields render sorted by key per spec rule 8; the spec's sample text
// lists retry fields before error, which violates that rule — the renderer
// follows the rule (error < retry), flagged to the coordinator.
func TestGoldenSpecSamples(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		lvl  Level
		fs   []zapcore.Field
		msg  string
		want string
	}{
		{
			name: "job_started",
			now:  at(1, 123),
			lvl:  INFO,
			fs: []zapcore.Field{
				zap.String("component", "worker"),
				zap.String("event", "job_started"),
				zap.String("job_id", "abc"),
			},
			msg:  "Job started",
			want: `2026-07-01T14:20:01.123 INFO component=worker event=job_started job_id=abc msg="Job started"`,
		},
		{
			name: "tool_started",
			now:  at(2, 123),
			lvl:  INFO,
			fs: []zapcore.Field{
				zap.String("component", "translator"),
				zap.String("event", "tool_started"),
				zap.String("job_id", "abc"),
				zap.String("tool", "llm-subtrans"),
			},
			msg:  "Translation tool started",
			want: `2026-07-01T14:20:02.123 INFO component=translator event=tool_started job_id=abc tool=llm-subtrans msg="Translation tool started"`,
		},
		{
			name: "translation_retry_warn",
			now:  at(3, 456),
			lvl:  WARN,
			fs: []zapcore.Field{
				zap.String("component", "translator"),
				zap.String("event", "translation_retry"),
				zap.String("job_id", "abc"),
				zap.Int("retry", 1),
				zap.Int64("retry_after_ms", 2000),
				zap.String("error", "provider timeout"),
			},
			msg:  "Translation will retry",
			want: `2026-07-01T14:20:03.456 WARN component=translator event=translation_retry job_id=abc error="provider timeout" retry=1 retry_after_ms=2000 msg="Translation will retry"`,
		},
		{
			name: "job_failed_error",
			now:  atMin(21, 0, 789),
			lvl:  ERROR,
			fs: []zapcore.Field{
				zap.String("component", "worker"),
				zap.String("event", "job_failed"),
				zap.String("job_id", "abc"),
				zap.String("stage", "translation"),
				zap.String("error", "retries exhausted"),
			},
			msg:  "Job failed",
			want: `2026-07-01T14:21:00.789 ERROR component=worker event=job_failed job_id=abc stage=translation error="retries exhausted" msg="Job failed"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderLine(tt.now, tt.lvl, "fusionn-subs", nil, tt.fs, tt.msg)
			if got != tt.want {
				t.Errorf("\n got: %s\nwant: %s", got, tt.want)
			}
			if strings.Contains(got, "\n") {
				t.Error("rendered line contains a raw newline")
			}
		})
	}
}

// Golden quoting/escaping matrix. The same inputs must produce byte-identical
// output in Go, Python and shell emitters. Quoting rule: bare only for
// [A-Za-z0-9][A-Za-z0-9_.-]*, otherwise strconv.Quote semantics (printable
// Unicode verbatim; C0/DEL \xNN; C1 and other non-printables \uNNNN; invalid
// UTF-8 bytes \xNN).
func TestGoldenEscaping(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"safe identifier", `abc`, `abc`},
		{"safe dotted version", `1.2.3-rc.1`, `1.2.3-rc.1`},
		{"path needs quotes", `/media/x.ass`, `"/media/x.ass"`},
		{"colon needs quotes", `fusionn:translation_queue`, `"fusionn:translation_queue"`},
		{"with spaces", `My Show S01E02.mkv`, `"My Show S01E02.mkv"`},
		{"with equals", `a=b`, `"a=b"`},
		{"empty", ``, `""`},
		{"double quotes", `he said "hi"`, `"he said \"hi\""`},
		{"backslash", `C:\new`, `"C:\\new"`},
		{"newline", "a\nb", `"a\nb"`},
		{"carriage return", "a\rb", `"a\rb"`},
		{"tab", "a\tb", `"a\tb"`},
		{"bell", "a\ab", `"a\ab"`},
		{"soh", "a\x01b", `"a\x01b"`},
		{"del char", "a\x7fb", `"a\x7fb"`},
		{"c1 control", "a\u0085b", `"a\u0085b"`},
		{"nbsp", "a\u00a0b", `"a\u00a0b"`},
		{"line separator", "a\u2028b", `"a\u2028b"`},
		{"paragraph separator", "a\u2029b", `"a\u2029b"`},
		{"chinese", `中文字幕`, `"中文字幕"`},
		{"emoji", `emoji😀`, `"emoji😀"`},
		{"invalid utf8", string([]byte{0xff, 0xfe}), `"\xff\xfe"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderLine(at(3, 456), INFO, "svc", nil, []zapcore.Field{
				zap.String("component", "c"),
				zap.String("event", "e"),
				zap.String("note", tt.input),
			}, "m")
			want := `2026-07-01T14:20:03.456 INFO component=c event=e note=` + tt.want + ` msg="m"`
			if got != want {
				t.Errorf("\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestGoldenNumbersAndBools(t *testing.T) {
	got := renderLine(at(3, 456), INFO, "svc", nil, []zapcore.Field{
		zap.String("component", "c"),
		zap.String("event", "e"),
		zap.Int("status", 200),
		zap.Bool("truncated", true),
		zap.Float64("ratio", 1.5),
	}, "m")
	want := `2026-07-01T14:20:03.456 INFO component=c event=e ratio=1.5 status=200 truncated=true msg="m"`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

// Correlation fields render in the fixed spec order regardless of insertion
// order; extras sort by key; msg is always last.
func TestGoldenFieldOrder(t *testing.T) {
	got := renderLine(at(3, 456), WARN, "fusionn-muse", nil, []zapcore.Field{
		zap.String("component", "processor"),
		zap.String("event", "translation_retry"),
		zap.String("stream", "stderr"),
		zap.String("stage", "translating"),
		zap.String("request_id", "req-9"),
		zap.Int64("attempt_id", 42),
		zap.String("job_id", "abc"),
		zap.String("submission_id", "sub-1"),
		zap.String("tool", "llm-subtrans"),
		zap.Int64("retry_after_ms", 2000),
		zap.Int("retry", 1),
		zap.String("note", "a=b"),
		zap.String("error", "provider timeout"),
	}, "Translation will retry")
	want := `2026-07-01T14:20:03.456 WARN component=processor event=translation_retry job_id=abc submission_id=sub-1 attempt_id=42 request_id=req-9 stage=translating tool=llm-subtrans stream=stderr error="provider timeout" note="a=b" retry=1 retry_after_ms=2000 msg="Translation will retry"`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

// Free-text fields (error/preview/stack) always render quoted, even when
// the value would match the bare rule.
func TestForceQuoteFreeText(t *testing.T) {
	got := renderLine(at(3, 456), INFO, "svc", nil, []zapcore.Field{
		zap.String("component", "c"),
		zap.String("event", "e"),
		zap.String("error", "timeout"),
		zap.String("note", "timeout"),
	}, "m")
	want := `2026-07-01T14:20:03.456 INFO component=c event=e error="timeout" note=timeout msg="m"`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

// Malformed keys (newline/equals/camelCase/dots/dashes) are dropped; keys
// are snake_case per spec.
func TestMalformedKeysDropped(t *testing.T) {
	got := renderLine(at(3, 456), INFO, "svc", nil, []zapcore.Field{
		zap.String("component", "c"),
		zap.String("event", "e"),
		{Key: "bad\nkey", Type: zapcore.StringType, String: "x"},
		{Key: "bad=key", Type: zapcore.StringType, String: "x"},
		{Key: "bad key", Type: zapcore.StringType, String: "x"},
		{Key: "BadKey", Type: zapcore.StringType, String: "x"},
		{Key: "bad-key", Type: zapcore.StringType, String: "x"},
		{Key: "bad.key", Type: zapcore.StringType, String: "x"},
		zap.String("good_key2", "y"),
	}, "m")
	want := `2026-07-01T14:20:03.456 INFO component=c event=e good_key2=y msg="m"`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

// Positional and envelope values go through the same redaction as extras.
func TestPositionalValuesRedacted(t *testing.T) {
	got := renderLine(at(3, 456), INFO, "svc", nil, []zapcore.Field{
		zap.String("component", "c"),
		zap.String("event", "e"),
		zap.String("job_id", "token=sk-test123"),
		zap.String("stage", "password=sk-test123"),
		zap.String("tool", "bearer sk-test123"),
	}, "m")
	if strings.Contains(got, "sk-test123") {
		t.Errorf("secret leaked through positional/envelope values: %q", got)
	}
	want := `2026-07-01T14:20:03.456 INFO component=c event=e job_id="token=***" stage="password=***" tool="bearer ***" msg="m"`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

// Sensitive field names mask values unconditionally.
func TestSensitiveFieldNames(t *testing.T) {
	got := renderLine(at(3, 456), INFO, "svc", nil, []zapcore.Field{
		zap.String("component", "c"),
		zap.String("event", "e"),
		zap.String("api_key", "FAKE_SECRET"),
		zap.String("openrouter_api_key", "FAKE_SECRET"),
		zap.String("refresh_token", "FAKE_SECRET"),
		zap.String("access_key", "FAKE_SECRET"),
		zap.String("client_secret", "FAKE_SECRET"),
		zap.Int("token_count", 42),
	}, "m")
	want := `2026-07-01T14:20:03.456 INFO component=c event=e access_key="***" api_key="***" client_secret="***" openrouter_api_key="***" refresh_token="***" token_count=42 msg="m"`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

// Preview is DEBUG-only and capped; a user-supplied truncated=false can never
// override a real truncation.
func TestPreviewPolicy(t *testing.T) {
	long := strings.Repeat("a", 600)
	// Dropped at INFO even when passed as a plain string field.
	got := renderLine(at(3, 456), INFO, "svc", nil, []zapcore.Field{
		zap.String("component", "c"),
		zap.String("event", "e"),
		zap.String("preview", long),
	}, "m")
	if strings.Contains(got, "preview") || strings.Contains(got, "truncated") {
		t.Errorf("preview must be dropped at INFO: %q", got)
	}
	// At DEBUG: capped, truncated=true wins over user-supplied false.
	got = renderLine(at(3, 456), DEBUG, "svc", nil, []zapcore.Field{
		zap.String("component", "c"),
		zap.String("event", "e"),
		zap.Bool("truncated", false),
		Preview(long),
	}, "m")
	if !strings.Contains(got, `preview="`+strings.Repeat("a", 512)+`" truncated=true`) {
		t.Errorf("preview policy broken: %q", got)
	}
}

// Reserved/envelope keys may not be spoofed by free-form fields; first
// occurrence wins for duplicates.
func TestReservedAndDuplicateFields(t *testing.T) {
	got := renderLine(at(3, 456), INFO, "svc", nil, []zapcore.Field{
		zap.String("component", "real"),
		zap.String("event", "real_event"),
		zap.String("job_id", "real-job"),
		zap.String("level", "CRITICAL"),
		zap.String("service", "fake"),
		zap.String("event", "fake_event"),
		zap.String("msg", "fake msg"),
		zap.String("job_id", "fake-job"),
		zap.String("time", "fake"),
		zap.String("k", "first"),
		zap.String("k", "second"),
	}, "real msg")
	want := `2026-07-01T14:20:03.456 INFO component=real event=real_event job_id=real-job k=first msg="real msg"`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

// The service identity is no longer rendered: the parameter is kept for API
// compatibility and ignored; a caller-supplied service field is rejected as a
// reserved key; and none of the four levels emits a service field.
func TestServiceNotRenderedAndReserved(t *testing.T) {
	base := []zapcore.Field{
		zap.String("component", "c"),
		zap.String("event", "e"),
	}
	for _, lvl := range []Level{DEBUG, INFO, WARN, ERROR} {
		got := renderLine(at(3, 456), lvl, "ignored-service", nil, base, "m")
		want := "2026-07-01T14:20:03.456 " + displayLevel(lvl) + " component=c event=e msg=\"m\""
		if got != want {
			t.Errorf("%s:\n got: %s\nwant: %s", lvl, got, want)
		}
		if strings.Contains(got, "service") {
			t.Errorf("%s line contains a service field: %q", lvl, got)
		}
	}
	got := renderLine(at(3, 456), INFO, "svc", nil, append(base, zap.String("service", "evil")), "m")
	want := "2026-07-01T14:20:03.456 INFO component=c event=e msg=\"m\""
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
}

func TestPreviewTruncationAndRedaction(t *testing.T) {
	long := strings.Repeat("字", 600)
	got := renderLine(at(3, 456), DEBUG, "svc", nil, []zapcore.Field{
		zap.String("component", "c"),
		zap.String("event", "e"),
		Preview(long + ` token=secret123`),
	}, "m")
	if !strings.Contains(got, `truncated=true`) {
		t.Errorf("expected truncated=true in %q", got)
	}
	// The 512-rune cap cuts the tail away; the secret must not appear.
	preview := strings.Repeat("字", 512)
	if !strings.Contains(got, `preview="`+preview+`"`) {
		t.Errorf("preview not capped at 512 runes in %q", got)
	}
	if strings.Contains(got, "secret123") {
		t.Errorf("secret leaked in %q", got)
	}

	// Short previews are redacted too.
	got = renderLine(at(3, 456), DEBUG, "svc", nil, []zapcore.Field{
		zap.String("component", "c"),
		zap.String("event", "e"),
		Preview(`token=secret123 ok`),
	}, "m")
	if !strings.Contains(got, `preview="token=*** ok"`) {
		t.Errorf("expected redaction inside preview in %q", got)
	}
	if strings.Contains(got, "secret123") || strings.Contains(got, "truncated") {
		t.Errorf("unexpected leak or truncation flag in %q", got)
	}
}

func TestCapRunesExactBoundary(t *testing.T) {
	s512 := strings.Repeat("a", 512)
	if got, truncated := capRunes(s512, previewMaxRunes); got != s512 || truncated {
		t.Errorf("512 runes should pass untruncated, got len=%d truncated=%v", len(got), truncated)
	}
	s513 := strings.Repeat("a", 513)
	got, truncated := capRunes(s513, previewMaxRunes)
	if !truncated || len(got) != 512 {
		t.Errorf("513 runes should truncate to 512, got len=%d truncated=%v", len(got), truncated)
	}
	s4096 := strings.Repeat("a", 4096)
	if got, truncated := capRunes(s4096, stackMaxRunes); got != s4096 || truncated {
		t.Errorf("4096 runes should pass untruncated for stacks, got len=%d truncated=%v", len(got), truncated)
	}
	s4097 := strings.Repeat("a", 4097)
	if got, truncated := capRunes(s4097, stackMaxRunes); !truncated || len(got) != 4096 {
		t.Errorf("4097 runes should truncate to 4096, got len=%d truncated=%v", len(got), truncated)
	}
}

func TestParseLevel(t *testing.T) {
	for s, want := range map[string]Level{
		"DEBUG": DEBUG, "debug": DEBUG, "INFO": INFO, "info": INFO,
		"WARN": WARN, "warn": WARN, "WARNING": WARN, "warning": WARN,
		"ERROR": ERROR, "error": ERROR,
	} {
		got, ok := ParseLevel(s)
		if !ok || got != want {
			t.Errorf("ParseLevel(%q) = %v,%v want %v,true", s, got, ok, want)
		}
	}
	for _, s := range []string{"", "banana", "TRACE", "CRITICAL"} {
		if _, ok := ParseLevel(s); ok {
			t.Errorf("ParseLevel(%q) unexpectedly ok", s)
		}
	}
}
