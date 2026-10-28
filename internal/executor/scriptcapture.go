package executor

import (
	"bytes"
	"context"
	"regexp"
	"strconv"
	"sync"

	"go.uber.org/zap"

	"github.com/fusionn-muse/pkg/logger"
)

// The translation script's own events. The failure event is its verdict, the
// failure record carries the safe facts about the failure, and the sanitized
// event is a summary a reader still wants. Everything else the script writes is
// only counted.
const (
	scriptFailureEvent   = "script_failure_detected"
	scriptFailureRecord  = "translation_failed"
	scriptSanitizedEvent = "subtitle_sanitized"
	scriptLogEvent       = "log"
)

// The script's own identity. Only a line that carries exactly this service and
// component is read as the script's display output; every other line, including
// another service's unified line, keeps the raw verdict and is only counted.
const (
	scriptServiceName   = "fusionn-muse"
	scriptComponentName = "llm-subtrans"
)

// unifiedLine is the header every line of the script's own display output
// starts with: timestamp, level, its service, its component and the event name.
// Only the header is matched, never a quoted value, so text inside a preview or
// a message can never be read as an event.
var unifiedLine = regexp.MustCompile(
	`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}[+-]\d{2}:\d{2} (DEBUG|INFO|WARN|ERROR) service=` +
		regexp.QuoteMeta(scriptServiceName) + ` component=` + regexp.QuoteMeta(scriptComponentName) +
		` event=(\S+)(?: |$)`,
)

// The previous release judged the script's output by searching its whole
// standard error for these two case-sensitive markers. That verdict is kept for
// output the script did not format.
var (
	legacyErrorMarker     = []byte("Error:")
	legacyTracebackMarker = []byte("Traceback")
)

// legacyCarry is the longest legacy marker minus one: a marker split across two
// writes is still found from the carried bytes.
const legacyCarry = len("Traceback") - 1

// maxLineBytes bounds what the capture retains for one line. Verdict and
// diagnostics are taken as bytes arrive, so a longer line loses no fact a
// reader needs.
const maxLineBytes = 32 << 10

// scriptCapture consumes the translation script's standard error.
//
// The script writes the unified format itself, so the capture reads the header
// of each of its lines instead of searching raw text: the failure verdict, the
// safe failure facts and the counters are updated as bytes arrive, and only a
// bounded prefix of a line is retained. Output that is not the script's own —
// including another service's unified lines — keeps the previous verdict's
// markers and is only counted. Nothing the script wrote is replayed verbatim;
// the caller re-emits the few facts a reader needs, with its own correlation.
type scriptCapture struct {
	mu sync.Mutex

	// pending retains the current line's first maxLineBytes bytes, and skipLF
	// remembers a CR that ended the last write, so the LF of a CRLF pair is not
	// read as an empty line.
	pending  []byte
	overflow bool
	skipLF   bool
	// scanning and scanningRaw describe what happens past the retention bound:
	// only a line that is not ours still needs the legacy marker scan, and carry
	// keeps enough bytes to match a marker split across writes.
	scanning    bool
	scanningRaw bool
	carry       []byte

	failureDetected bool
	legacyFailure   bool
	diagnostics     ScriptDiagnostics
	sanitized       bool

	logLines    int
	logWarnings int
	rawLines    int
	otherEvents int
	dropped     int
}

func (c *scriptCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	written := len(p)
	if c.skipLF {
		c.skipLF = false
		if len(p) > 0 && p[0] == '\n' {
			p = p[1:]
		}
	}
	for len(p) > 0 {
		end := bytes.IndexAny(p, "\r\n")
		if end < 0 {
			c.appendToLine(p)
			break
		}
		c.appendToLine(p[:end])
		c.endLine()
		if p[end] == '\r' {
			// A CR ends a progress line, so the next line is read on its own
			// even when no LF follows it. An LF that immediately follows a CR is
			// the same boundary, not an empty line, even across two writes.
			p = p[end+1:]
			if len(p) == 0 {
				c.skipLF = true
			} else if p[0] == '\n' {
				p = p[1:]
			}
			continue
		}
		p = p[end+1:]
	}
	return written, nil
}

// close ends the last line, which the script may have written without a
// trailing newline.
func (c *scriptCapture) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) > 0 || c.overflow {
		c.endLine()
	}
}

func (c *scriptCapture) appendToLine(chunk []byte) {
	room := maxLineBytes - len(c.pending)
	if len(chunk) <= room {
		c.pending = append(c.pending, chunk...)
		return
	}
	if room > 0 {
		c.pending = append(c.pending, chunk[:room]...)
		chunk = chunk[room:]
	}
	c.overflow = true
	c.beginTailScan()
	c.scanTail(chunk)
}

// beginTailScan decides once, at the retention bound, whether the rest of a
// long line needs the legacy marker scan.
func (c *scriptCapture) beginTailScan() {
	if c.scanning {
		return
	}
	c.scanning = true
	if _, ok := parseUnifiedLine(c.pending); ok {
		return // our own line: the bytes after the header are never searched
	}
	c.scanningRaw = true
	c.setCarry(c.pending)
}

func (c *scriptCapture) scanTail(chunk []byte) {
	if !c.scanningRaw || len(chunk) == 0 {
		return
	}
	if legacyMatches(c.carry, chunk) {
		c.legacyFailure = true
	}
	c.setCarry(chunk)
}

// setCarry keeps the last legacyCarry bytes consumed, including the previous
// carry, so a marker split across any number of writes is still found. A chunk
// longer than the window contributes its own tail, never its head.
func (c *scriptCapture) setCarry(chunk []byte) {
	if len(chunk) >= legacyCarry {
		c.carry = append(c.carry[:0], chunk[len(chunk)-legacyCarry:]...)
		return
	}
	if combined := len(c.carry) + len(chunk); combined > legacyCarry {
		c.carry = append(c.carry[:0], c.carry[combined-legacyCarry:]...)
	}
	c.carry = append(c.carry, chunk...)
}

func (c *scriptCapture) endLine() {
	line := c.pending
	if len(line) == 0 && !c.overflow {
		return // a blank separator is not output
	}
	if c.overflow {
		c.dropped++
	}
	if header, ok := parseUnifiedLine(line); ok {
		c.recordEvent(header)
	} else {
		c.rawLines++
		if containsLegacyMarker(line) {
			c.legacyFailure = true
		}
	}
	c.pending = c.pending[:0]
	c.carry = c.carry[:0]
	c.overflow, c.scanning, c.scanningRaw = false, false, false
}

func (c *scriptCapture) recordEvent(header unifiedHeader) {
	switch header.event {
	case scriptFailureEvent:
		c.failureDetected = true
	case scriptFailureRecord:
		if c.diagnostics.ErrorType == "" {
			c.diagnostics = parseScriptDiagnostics(scriptFields(header.fields), c.overflow)
		}
	case scriptSanitizedEvent:
		c.sanitized = true
	case scriptLogEvent:
		c.logLines++
		if header.level == "WARN" || header.level == "ERROR" {
			c.logWarnings++
		}
	default:
		c.otherEvents++
	}
}

// verdict reports whether the script's output means the translation failed.
func (c *scriptCapture) verdict() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failureDetected || c.legacyFailure
}

func (c *scriptCapture) safeDiagnostics() ScriptDiagnostics {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.diagnostics
}

// report re-emits what the script said that a reader still needs, under this
// process's own correlation: its own summary event, and one bounded count of
// everything else it wrote. The script's lines are never replayed.
func (c *scriptCapture) report(ctx context.Context, log logger.Logger) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sanitized {
		log.Info(ctx, scriptSanitizedEvent, "Replaced age-sensitive subtitle terms before translation")
	}
	if c.logLines+c.rawLines+c.otherEvents == 0 {
		return
	}
	fields := []zap.Field{
		zap.Int("log_lines", c.logLines),
		zap.Int("raw_lines", c.rawLines),
		zap.Int("other_events", c.otherEvents),
	}
	if c.dropped > 0 {
		fields = append(fields, zap.Int("dropped_lines", c.dropped))
	}
	if c.logWarnings > 0 {
		log.Warn(ctx, "tool_output_summary", "Child reported warnings", fields...)
		return
	}
	log.Debug(ctx, "tool_output_summary", "Child output summarized", fields...)
}

// unifiedHeader is the part of a unified line that carries meaning: its level,
// its event and the remaining fields.
type unifiedHeader struct {
	level  string
	event  string
	fields []byte
}

func parseUnifiedLine(line []byte) (unifiedHeader, bool) {
	match := unifiedLine.FindSubmatchIndex(line)
	if match == nil {
		return unifiedHeader{}, false
	}
	return unifiedHeader{
		level:  string(line[match[2]:match[3]]),
		event:  string(line[match[4]:match[5]]),
		fields: line[match[5]:],
	}, true
}

// containsLegacyMarker applies the previous release's verdict to one line the
// script did not format.
func containsLegacyMarker(line []byte) bool {
	return bytes.Contains(line, legacyErrorMarker) || bytes.Contains(line, legacyTracebackMarker)
}

// legacyMatches continues the search across a write boundary, so a marker split
// between two writes is still found.
func legacyMatches(carry, chunk []byte) bool {
	if containsLegacyMarker(chunk) {
		return true
	}
	if len(carry) == 0 {
		return false
	}
	var window [2 * legacyCarry]byte
	n := copy(window[:], carry)
	n += copy(window[n:], chunk)
	return containsLegacyMarker(window[:n])
}

// scriptFields walks the key=value pairs after a line's header. A quoted value
// is skipped whole, so text inside one value is never read as another field.
// Each value is decoded exactly once here; the log renderer quotes it once more
// when the caller reports it.
func scriptFields(rest []byte) map[string]string {
	fields := make(map[string]string, 6)
	for {
		rest = bytes.TrimLeft(rest, " ")
		if len(rest) == 0 {
			return fields
		}
		eq := bytes.IndexByte(rest, '=')
		if eq <= 0 || !validFieldName(rest[:eq]) {
			return fields
		}
		key := string(rest[:eq])
		rest = rest[eq+1:]
		if len(rest) == 0 {
			return fields
		}
		var value string
		if rest[0] == '"' {
			quoted, err := strconv.QuotedPrefix(string(rest))
			if err != nil {
				return fields
			}
			decoded, err := strconv.Unquote(quoted)
			if err != nil {
				return fields
			}
			value = decoded
			rest = rest[len(quoted):]
		} else {
			end := bytes.IndexByte(rest, ' ')
			if end < 0 {
				value, rest = string(rest), nil
			} else {
				value, rest = string(rest[:end]), rest[end:]
			}
		}
		if _, seen := fields[key]; !seen {
			fields[key] = value
		}
	}
}

func validFieldName(name []byte) bool {
	if len(name) == 0 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, b := range name[1:] {
		switch {
		case b >= 'a' && b <= 'z', b >= '0' && b <= '9', b == '_':
		default:
			return false
		}
	}
	return true
}

// parseScriptDiagnostics reads the safe facts out of the script's own failure
// record: what failed, any structural detail and where. It carries no message,
// so the record's content cannot reach a reader through it.
func parseScriptDiagnostics(fields map[string]string, truncated bool) ScriptDiagnostics {
	diagnostics := ScriptDiagnostics{
		ErrorType: fields["error"],
		Module:    fields["module"],
		Strerror:  fields["strerror"],
		Frames:    fields["stack"],
		Truncated: truncated,
	}
	if errno, err := strconv.Atoi(fields["errno"]); err == nil {
		diagnostics.Errno = errno
	}
	return diagnostics
}
