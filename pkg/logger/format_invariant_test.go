package logger

import (
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// parseLine tokenizes a rendered line back into level and ordered key/value
// pairs, decoding quoted values. It fails the test on anything malformed, on
// a raw control character, or on a duplicate key.
func parseLine(t *testing.T, line string) (string, []string, []string) {
	t.Helper()
	if strings.ContainsAny(line, "\n\r\x00\t") {
		t.Fatalf("rendered line contains a raw control character: %q", line)
	}
	fields := splitRespectingQuotes(line)
	if len(fields) < 6 {
		t.Fatalf("too few tokens: %q", line)
	}
	if !lineRe.MatchString(line) {
		t.Fatalf("timestamp/level prefix malformed: %q", line)
	}
	level := fields[1]
	keys := make([]string, 0, len(fields))
	values := make([]string, 0, len(fields))
	for _, token := range fields[2:] {
		key, raw, ok := strings.Cut(token, "=")
		if !ok || !validKeyRe.MatchString(key) {
			t.Fatalf("malformed token %q in %q", token, line)
		}
		var value string
		if strings.HasPrefix(raw, `"`) {
			decoded, err := strconv.Unquote(raw)
			if err != nil {
				t.Fatalf("undecodable quoted value %q in %q: %v", raw, line, err)
			}
			value = decoded
		} else {
			if raw == "" {
				t.Fatalf("empty bare value in %q", line)
			}
			value = raw
		}
		keys = append(keys, key)
		values = append(values, value)
	}
	seen := map[string]bool{}
	for _, key := range keys {
		if seen[key] {
			t.Fatalf("duplicate key %q in %q", key, line)
		}
		seen[key] = true
	}
	return level, keys, values
}

// splitRespectingQuotes splits on spaces that are outside double quotes.
func splitRespectingQuotes(line string) []string {
	tokens := make([]string, 0, 16)
	var b strings.Builder
	inQuote, escaped := false, false
	for _, r := range line {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\' && inQuote:
			b.WriteRune(r)
			escaped = true
		case r == '"':
			inQuote = !inQuote
			b.WriteRune(r)
		case r == ' ' && !inQuote:
			tokens = append(tokens, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	return append(tokens, b.String())
}

func TestRenderInvariantsAndRoundTrip(t *testing.T) {
	nasty := map[string]string{
		"nul":         "a\x00b",
		"newline":     "a\nb",
		"crlf":        "a\r\nb",
		"tab":         "a\tb",
		"bell":        "a\ab",
		"quote":       `he said "hi"`,
		"backslash":   `C:\new`,
		"equals":      "a=b",
		"spaces":      "two words",
		"cjk":         "中文字幕",
		"emoji":       "emoji😀",
		"empty":       "",
		"invalid":     string([]byte{0xff}),
		"unicodeline": "a\u2028b",
	}
	fields := []zapcore.Field{
		zap.String("component", "c"),
		zap.String("event", "e"),
		zap.String("job_id", "j1"),
		zap.String("stage", "analyzing"),
		zap.String("tool", "ffmpeg"),
		zap.String("stream", "stderr"),
		zap.String("error", "boom"),
		zap.Int("count", 3),
		zap.Bool("done", false),
		zap.Float64("ratio", 0.5),
		zap.String("api_key", "FAKE_SECRET"),
		Preview(strings.Repeat("字", 600)),
	}
	for key := range nasty {
		fields = append(fields, zap.String(key, nasty[key]))
	}
	got := renderLine(at(3, 456), DEBUG, "svc", nil, fields, "Message with \"quotes\" and\nnewline")

	level, parsedKeys, parsedValues := parseLine(t, got)
	if level != "DEBUG" {
		t.Errorf("level = %q, want DEBUG", level)
	}

	// Fixed prefix order. service is never emitted: it stays a reserved key
	// but the renderer no longer writes it.
	wantPrefix := []string{"component", "event", "job_id", "stage", "tool", "stream"}
	for i, key := range wantPrefix {
		if parsedKeys[i] != key {
			t.Fatalf("position %d = %q, want %q (line: %s)", i, parsedKeys[i], key, got)
		}
	}
	for _, key := range parsedKeys {
		if key == "service" {
			t.Errorf("rendered line must not contain a service field: %s", got)
		}
	}
	// msg is last.
	if parsedKeys[len(parsedKeys)-1] != "msg" {
		t.Errorf("msg must be last, got %q", parsedKeys[len(parsedKeys)-1])
	}
	// Extras between the positional block and msg are sorted by key.
	extras := parsedKeys[len(wantPrefix) : len(parsedKeys)-1]
	for i := 1; i < len(extras); i++ {
		if extras[i-1] >= extras[i] {
			t.Errorf("extras not sorted: %q then %q", extras[i-1], extras[i])
		}
	}
	// Values round-trip: find the key and compare the decoded value.
	valueOf := func(key string) string {
		for i, k := range parsedKeys {
			if k == key {
				return parsedValues[i]
			}
		}
		return "<missing>"
	}
	for key, want := range nasty {
		if gotValue := valueOf(key); gotValue != want {
			t.Errorf("value %q round-trip = %q, want %q", key, gotValue, want)
		}
	}
	for _, want := range []string{"c", "e", "j1", "analyzing", "ffmpeg", "stderr"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if valueOf("api_key") != "***" {
		t.Errorf("api_key not masked: %q", valueOf("api_key"))
	}
	if !strings.Contains(got, "truncated=true") {
		t.Errorf("preview truncation flag missing: %s", got)
	}
	if strings.Contains(got, "FAKE_SECRET") {
		t.Errorf("secret leaked: %s", got)
	}
}

// Preview boundaries: 511 passes, 513 truncates, exactly 512 does not.
func TestPreviewBoundaries(t *testing.T) {
	for _, tc := range []struct {
		n         int
		truncated bool
	}{
		{511, false}, {512, false}, {513, true},
	} {
		got := renderLine(at(3, 456), DEBUG, "svc", nil, []zapcore.Field{
			zap.String("component", "c"),
			zap.String("event", "e"),
			Preview(strings.Repeat("x", tc.n)),
		}, "m")
		if strings.Contains(got, "truncated=true") != tc.truncated {
			t.Errorf("preview of %d runes: truncated=%v, want %v (%s)", tc.n, !tc.truncated, tc.truncated, got)
		}
		_, _, values := parseLine(t, got)
		preview := values[2] // component, event, preview
		if gotLen, wantLen := len([]rune(preview)), min(tc.n, previewMaxRunes); gotLen != wantLen {
			t.Errorf("preview of %d runes rendered %d runes, want %d", tc.n, gotLen, wantLen)
		}
	}
}
