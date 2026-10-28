package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// lineCore is a minimal zapcore.Core that renders the unified single-line
// format. zap's ConsoleEncoder is not usable here because it appends context
// fields as a JSON tail; a full custom zapcore.Encoder would be ~20 methods
// where this Core needs 5. zap still owns the pipeline: level gating,
// checked entries, SugaredLogger/observer injection, Sync, and (via
// zapcore.Lock) goroutine-safe writes. Every entry is rendered in one
// renderLine call and emitted in one Write, so there is exactly one
// timestamp, one level and one physical line per event.
type lineCore struct {
	service string
	lvl     zapcore.LevelEnabler
	out     zapcore.WriteSyncer
	with    []zapcore.Field
}

func newLineCore(service string, lvl Level, out zapcore.WriteSyncer) *lineCore {
	return &lineCore{service: service, lvl: lvl, out: zapcore.Lock(out)}
}

func (c *lineCore) Enabled(l zapcore.Level) bool { return c.lvl.Enabled(l) }

func (c *lineCore) With(fs []zapcore.Field) zapcore.Core {
	merged := make([]zapcore.Field, 0, len(c.with)+len(fs))
	merged = append(merged, c.with...)
	merged = append(merged, fs...)
	return &lineCore{service: c.service, lvl: c.lvl, out: c.out, with: merged}
}

func (c *lineCore) Check(e zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(e.Level) {
		return ce.AddCore(e, c)
	}
	return ce
}

func (c *lineCore) Write(e zapcore.Entry, fs []zapcore.Field) error {
	if e.Stack != "" && !hasKey(fs, "stack") {
		// Keep the stack as an escaped, redacted, bounded field instead of
		// dropping it. An explicit stack field always wins.
		fs = append(fs, zap.String("stack", e.Stack))
	}
	_, err := c.out.Write([]byte(renderLine(e.Time, e.Level, c.service, c.with, fs, e.Message) + "\n"))
	return err
}

func hasKey(fs []zapcore.Field, key string) bool {
	for _, f := range fs {
		if f.Key == key {
			return true
		}
	}
	return false
}

func (c *lineCore) Sync() error { return c.out.Sync() }
