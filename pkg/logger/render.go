package logger

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap/zapcore"
)

// timeLayout renders a timestamp as wall time without an offset. The core
// converts the time to its log zone first (see logZone).
const timeLayout = "2006-01-02T15:04:05.000"

// correlation holds the identity fields carried by context.Context. They are
// rendered in a fixed order and may never be overridden by free-form fields.
type correlation struct {
	jobID        string
	submissionID string
	attemptID    string
	requestID    string
	parentJobID  string
}

// positionalKeys render right after the correlation fields when present.
var positionalKeys = []string{"stage", "tool", "stream"}

// reservedKeys may not appear as free-form fields: they are either rendered
// from dedicated positions or would let caller input spoof the envelope.
var reservedKeys = map[string]bool{
	"time": true, "level": true, "service": true, "component": true, "event": true, "msg": true,
	"job_id": true, "submission_id": true, "attempt_id": true, "request_id": true, "parent_job_id": true,
	"stage": true, "tool": true, "stream": true,
}

// bareRe matches identifier-class safe values rendered without quotes. Any
// other string renders via strconv.Quote. Byte-identical rule across
// Go/Python/shell (Python: \A[A-Za-z0-9][A-Za-z0-9_.-]*\Z).
var bareRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validKeyRe rejects field keys that could inject a forged event into a
// line. Keys are snake_case per spec; anything else is dropped.
var validKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// forceQuoteKeys are free-text fields that always render quoted, even when
// the value would otherwise match the bare rule (spec: text values are
// quoted; only identifier-class values may go bare).
var forceQuoteKeys = map[string]bool{"detail": true, "error": true, "preview": true, "stack": true}

// kv is a field after value extraction, ready for sorting and rendering.
type kv struct {
	key string
	val any
}

// renderLine renders one event as exactly one physical line:
//
//	TS LEVEL component=C event=E [job_id..parent_job_id] [stage tool stream] [extras sorted] msg=M
//
// The service parameter is kept for API compatibility with the three-repo
// shared core but is no longer rendered: the application or container
// identity comes from the collection entry. It is still a reserved key, so
// caller input can never re-render it.
//
// It is the single render path for all output; golden tests call it directly.
func renderLine(now time.Time, lvl Level, _ string, with, call []zapcore.Field, msg string) string {
	loc := now.Location()
	component := "app"
	event := "log_message"
	compSet, eventSet := false, false
	var corr correlation
	positional := make(map[string]string, len(positionalKeys))
	seen := make(map[string]bool, len(call))
	extras := make([]kv, 0, len(call))
	truncatedFlag := false

	collect := func(f zapcore.Field) {
		switch f.Key {
		case "component":
			if s, ok := zstring(f, loc); ok && !compSet {
				component, compSet = s, true
			}
			return
		case "event":
			if s, ok := zstring(f, loc); ok && !eventSet {
				event, eventSet = s, true
			}
			return
		case "job_id", "submission_id", "attempt_id", "request_id", "parent_job_id":
			s, ok := zstring(f, loc)
			if !ok {
				return
			}
			switch f.Key {
			case "job_id":
				if corr.jobID == "" {
					corr.jobID = s
				}
			case "submission_id":
				if corr.submissionID == "" {
					corr.submissionID = s
				}
			case "attempt_id":
				if corr.attemptID == "" {
					corr.attemptID = s
				}
			case "request_id":
				if corr.requestID == "" {
					corr.requestID = s
				}
			case "parent_job_id":
				if corr.parentJobID == "" {
					corr.parentJobID = s
				}
			}
			return
		}
		if !validKeyRe.MatchString(f.Key) {
			return // a malformed key could inject a forged event; drop it
		}
		if reservedKeys[f.Key] {
			// stage/tool/stream render in the fixed positional block.
			for _, pk := range positionalKeys {
				if f.Key == pk {
					if _, dup := positional[pk]; !dup {
						if s, ok := zstring(f, loc); ok {
							positional[pk] = s
						}
					}
					return
				}
			}
			return
		}
		if seen[f.Key] {
			return // first occurrence wins
		}
		seen[f.Key] = true
		if f.Key == "preview" || f.Key == "stack" || f.Key == "detail" {
			// Content excerpts are DEBUG-only; a failure detail and a stack
			// render at their entry level. All are redacted and capped; a
			// user-supplied truncated=false can never override the cap.
			if f.Key == "preview" && lvl != DEBUG {
				return
			}
			s, ok := zstring(f, loc)
			if !ok {
				s = fmt.Sprint(zvalue(f, loc))
			}
			limit := previewMaxRunes
			switch f.Key {
			case "stack":
				limit = stackMaxRunes
			case "detail":
				limit = detailMaxRunes
			}
			capped, truncated := capRunes(Redact(s), limit)
			extras = append(extras, kv{f.Key, capped})
			if truncated {
				truncatedFlag = true
			}
			return
		}
		if isSensitiveKey(f.Key) {
			// A sensitive field name masks the value unconditionally,
			// whatever the value looks like.
			extras = append(extras, kv{f.Key, "***"})
			return
		}
		extras = append(extras, kv{f.Key, zvalue(f, loc)})
	}
	for _, f := range with {
		collect(f)
	}
	for _, f := range call {
		collect(f)
	}
	if truncatedFlag {
		filtered := make([]kv, 0, len(extras)+1)
		for _, e := range extras {
			if e.key != "truncated" {
				filtered = append(filtered, e)
			}
		}
		filtered = append(filtered, kv{"truncated", true})
		extras = filtered
	}
	sort.SliceStable(extras, func(i, j int) bool { return extras[i].key < extras[j].key })

	var b strings.Builder
	b.Grow(160)
	b.WriteString(now.Format(timeLayout))
	b.WriteByte(' ')
	b.WriteString(displayLevel(lvl))
	b.WriteString(" component=")
	b.WriteString(renderString(Redact(component)))
	b.WriteString(" event=")
	b.WriteString(renderString(Redact(event)))
	writeCorr := func(key, val string) {
		if val != "" {
			b.WriteByte(' ')
			b.WriteString(key)
			b.WriteByte('=')
			b.WriteString(renderString(Redact(val)))
		}
	}
	writeCorr("job_id", corr.jobID)
	writeCorr("submission_id", corr.submissionID)
	writeCorr("attempt_id", corr.attemptID)
	writeCorr("request_id", corr.requestID)
	writeCorr("parent_job_id", corr.parentJobID)
	for _, pk := range positionalKeys {
		writeCorr(pk, positional[pk])
	}
	for _, f := range extras {
		b.WriteByte(' ')
		b.WriteString(f.key)
		b.WriteByte('=')
		if s, ok := f.val.(string); ok {
			if forceQuoteKeys[f.key] {
				b.WriteString(strconv.Quote(Redact(s)))
			} else {
				b.WriteString(renderString(Redact(s)))
			}
		} else {
			b.WriteString(renderValue(f.val))
		}
	}
	b.WriteString(" msg=")
	b.WriteString(strconv.Quote(Redact(msg)))
	return b.String()
}

// displayLevel maps zap levels onto the four unified words. DPanic, Panic
// and Fatal display as ERROR; zap keeps its own panic/exit behavior for them.
func displayLevel(l zapcore.Level) string {
	if l >= zapcore.ErrorLevel {
		return "ERROR"
	}
	return l.CapitalString()
}

// zvalue extracts a renderable Go value from a zapcore.Field. A time renders as
// wall time in loc, the zone of the line's timestamp.
func zvalue(f zapcore.Field, loc *time.Location) any {
	switch f.Type {
	case zapcore.StringType:
		return f.String
	case zapcore.BoolType:
		return f.Integer == 1
	case zapcore.Int64Type, zapcore.Int32Type, zapcore.Int16Type, zapcore.Int8Type,
		zapcore.Uint64Type, zapcore.Uint32Type, zapcore.Uint16Type, zapcore.Uint8Type,
		zapcore.UintptrType, zapcore.DurationType:
		return f.Integer
	case zapcore.Float64Type:
		return math.Float64frombits(uint64(f.Integer))
	case zapcore.Float32Type:
		return float64(math.Float32frombits(uint32(f.Integer)))
	case zapcore.ErrorType:
		if err, ok := f.Interface.(error); ok {
			return err.Error()
		}
		return fmt.Sprint(f.Interface)
	case zapcore.StringerType:
		if s, ok := f.Interface.(fmt.Stringer); ok {
			return s.String()
		}
		return fmt.Sprint(f.Interface)
	case zapcore.TimeType, zapcore.TimeFullType:
		return time.Unix(0, f.Integer).In(loc).Format(timeLayout)
	default:
		// ReflectType and anything else is stringified at render time.
		if f.Interface != nil {
			return f.Interface
		}
		return f.String
	}
}

func zstring(f zapcore.Field, loc *time.Location) (string, bool) {
	switch v := zvalue(f, loc).(type) {
	case string:
		return v, true
	case int64:
		return strconv.FormatInt(v, 10), true
	}
	return "", false
}

// renderValue renders a non-string value (numbers and bools bare).
func renderValue(v any) string {
	switch t := v.(type) {
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return renderString(Redact(fmt.Sprint(t)))
	}
}

// renderString renders s bare when it matches the identifier-class safe rule,
// otherwise via strconv.Quote (shared golden semantics: keeps printable
// Unicode verbatim, escapes C0/DEL as \xNN, C1 and other non-printables as
// \uNNNN, and invalid UTF-8 bytes as \xNN).
func renderString(s string) string {
	if bareRe.MatchString(s) {
		return s
	}
	return strconv.Quote(s)
}

const (
	// previewMaxRunes caps a controlled content excerpt.
	previewMaxRunes = 512
	// stackMaxRunes caps an escaped panic/exception stack; larger than a
	// preview so the source location survives, still bounded.
	stackMaxRunes = 4096
	// detailMaxRunes caps a failure detail, which shows at every level.
	detailMaxRunes = 300
)

// capRunes truncates s to limit runes, reporting truncation.
func capRunes(s string, limit int) (string, bool) {
	n := 0
	for i := range s {
		if n == limit {
			return s[:i], true
		}
		n++
	}
	return s, false
}
