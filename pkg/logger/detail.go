package logger

import (
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"go.uber.org/zap"
)

// detailMaxLines is the number of error lines that FailureDetail keeps.
const detailMaxLines = 3

var (
	// ansiRe matches terminal color and cursor sequences: ESC [, parameter
	// bytes, intermediate bytes (0x20-0x2f) and one final byte (0x40-0x7e).
	ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[\x20-\x2f]*[\x40-\x7e]`)
	// errorLineRe matches the words that tools use to report a failure.
	errorLineRe = regexp.MustCompile(`(?i)\w*(error|exception)\b|\b(fatal|fail\w*|panic\w*|abort\w*|denied|` +
		`forbidden|unauthori[sz]ed|invalid|cannot|can't|unable|refused|killed|exceeded|not found|no such|` +
		`no space left|rate limit|timed out|out of memory)\b`)
	// tracebackFrameRe matches the location line of a Python traceback frame.
	tracebackFrameRe = regexp.MustCompile(`^(\s*)File ".*", line \d+`)
)

// FailureDetail returns the detail field of a failed tool run: the last lines
// of its output that look like an error message, oldest first, joined with
// " | ". It leaves out terminal sequences, blank lines, repeated lines and the
// frames of a Python traceback, and masks each non-empty secret value. The
// newest line is always kept and is cut to the cap when it is too long; an
// older line is added only while the whole detail stays within the cap.
// Output without such a line returns a field that renders nothing.
//
// A matching line can contain a short piece of the tool's input, such as
// subtitle text. The renderer redacts and caps detail at every level, so a
// caller cannot use it to write unbounded content.
func FailureDetail(output string, secrets ...string) zap.Field {
	var errorLines []string
	frameIndent := -1
	for _, raw := range strings.FieldsFunc(output, func(r rune) bool { return r == '\n' || r == '\r' }) {
		raw = ansiRe.ReplaceAllString(raw, "")
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		if frameIndent >= 0 && indent > frameIndent {
			continue // the source line printed under a traceback frame
		}
		frameIndent = -1
		if m := tracebackFrameRe.FindStringSubmatch(raw); m != nil {
			frameIndent = len(m[1])
			continue
		}
		if errorLineRe.MatchString(line) {
			errorLines = append(errorLines, line)
		}
	}

	var picked []string // newest first
	size := 0
	for i := len(errorLines) - 1; i >= 0 && len(picked) < detailMaxLines; i-- {
		line := errorLines[i]
		for _, secret := range secrets {
			if secret != "" {
				line = strings.ReplaceAll(line, secret, "***")
			}
		}
		// Redact the whole line before any cut, so a credential that crosses
		// the cut keeps the context its pattern needs.
		line = Redact(line)
		if slices.Contains(picked, line) {
			continue
		}
		n := utf8.RuneCountInString(line)
		if len(picked) == 0 && n > detailMaxRunes {
			cut, _ := capRunes(line, detailMaxRunes-1)
			picked = append(picked, cut+"…")
			break
		}
		if len(picked) > 0 && size+len(" | ")+n > detailMaxRunes {
			break
		}
		if len(picked) > 0 {
			size += len(" | ")
		}
		size += n
		picked = append(picked, line)
	}
	if len(picked) == 0 {
		return zap.Skip()
	}
	slices.Reverse(picked)
	return zap.String("detail", strings.Join(picked, " | "))
}
