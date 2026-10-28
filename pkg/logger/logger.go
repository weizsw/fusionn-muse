// Package logger emits the unified single-line log format shared by the
// fusionn services. All output flows through one zapcore.Core (lineCore)
// that renders each entry in a single renderLine call and a single Write;
// zap provides level gating, the SugaredLogger API and goroutine-safe syncs.
//
// New code uses Component("...") with a context carrying correlation IDs:
//
//	log := logger.Component("worker")
//	ctx = logger.WithJobID(ctx, job.ID)
//	log.Info(ctx, "job_started", "Job started")
//	log.Error(ctx, "job_failed", "Job failed", zap.String("stage", "translation"), zap.Error(err))
//
// The level comes from LOG_LEVEL (DEBUG/INFO/WARN/ERROR, default INFO);
// an invalid value produces a visible config_invalid warning at startup.
package logger

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// serviceName is the per-repo constant for the service= field. Sibling repos
// (fusionn-muse, fusionn-subs) change this one line when syncing this package.
const serviceName = "fusionn-muse"

// Log is the process-wide sugared logger and the single write path for all
// output (legacy helpers and Component loggers alike). It stays an assignable
// variable so tests can inject a zap observer core; every helper dereferences
// it at call time, so an injected observer also captures Component events.
var Log *zap.SugaredLogger

// Config configures the process-wide logger via Configure.
type Config struct {
	// Service overrides serviceName; normally left empty.
	Service string
	// Output defaults to os.Stdout. Buffers need no Sync.
	Output zapcore.WriteSyncer
	// The level always comes from LOG_LEVEL (default INFO) so every emitter
	// shares one knob; it is not part of Config.
}

// Configure (re)builds the process-wide logger and is the only place Log is
// assigned in production. Call it once at startup, before logging begins;
// swapping Log while logging is in flight is a data race (unchanged from the
// pre-unification design) and is only for tests between cases. Re-calling
// fully replaces the old logger with no duplicated output. Reads LOG_LEVEL:
// DEBUG/INFO/WARN/ERROR (case-insensitive, WARNING accepted), default INFO.
// An invalid value keeps INFO and emits a visible config_invalid warning
// instead of silently disabling logs.
func Configure(cfg Config) {
	service := cfg.Service
	if service == "" {
		service = serviceName
	}
	out := cfg.Output
	if out == nil {
		out = zapcore.AddSync(os.Stdout)
	}

	level := INFO
	raw := strings.TrimSpace(os.Getenv("LOG_LEVEL"))
	parsed, valid := ParseLevel(raw)
	if raw != "" && valid {
		level = parsed
	}

	Log = zap.New(newLineCore(service, level, out)).Sugar()

	if raw != "" && !valid {
		Component("logger").Warn(context.Background(), "config_invalid",
			"Invalid LOG_LEVEL, using INFO", zap.String("log_level", raw))
	}
}

// Init is deprecated: isDev no longer enables DEBUG or colors (only
// LOG_LEVEL does). Kept so existing call sites compile; use Configure.
func Init(isDev bool) { Configure(Config{}) }

// Sync flushes the log sink. Safe on nil.
func Sync() {
	if Log != nil {
		_ = Log.Sync() //nolint:errcheck // Sync can fail on stdout/stderr, safe to ignore
	}
}

func init() {
	Log = zap.New(newLineCore(serviceName, INFO, zapcore.AddSync(os.Stdout))).Sugar()
}

// Logger emits structured events for one stable module name. It is cheap to
// create, safe for concurrent use, and resolves the current process-wide core
// at call time, so a package-level `var log = logger.Component("x")` picks up
// a later Configure.
type Logger struct{ name string }

// Component binds a Logger to a stable module name (e.g. "worker",
// "pipeline", "handler"); not a file name.
func Component(name string) Logger { return Logger{name: name} }

func (c Logger) log(ctx context.Context, lvl Level, event, msg string, fields ...zap.Field) {
	l := Log
	if l == nil {
		return
	}
	ce := l.Desugar().Check(lvl, msg)
	if ce == nil {
		return
	}
	corr := ctxCorr(ctx)
	fs := make([]zap.Field, 0, len(fields)+8)
	fs = append(fs, zap.String("component", c.name), zap.String("event", event))
	if corr.jobID != "" {
		fs = append(fs, zap.String("job_id", corr.jobID))
	}
	if corr.submissionID != "" {
		fs = append(fs, zap.String("submission_id", corr.submissionID))
	}
	if corr.attemptID != "" {
		fs = append(fs, zap.String("attempt_id", corr.attemptID))
	}
	if corr.requestID != "" {
		fs = append(fs, zap.String("request_id", corr.requestID))
	}
	if corr.parentJobID != "" {
		fs = append(fs, zap.String("parent_job_id", corr.parentJobID))
	}
	fs = append(fs, fields...)
	ce.Write(fs...)
}

func (c Logger) Debug(ctx context.Context, event, msg string, fields ...zap.Field) {
	c.log(ctx, DEBUG, event, msg, fields...)
}

func (c Logger) Info(ctx context.Context, event, msg string, fields ...zap.Field) {
	c.log(ctx, INFO, event, msg, fields...)
}

func (c Logger) Warn(ctx context.Context, event, msg string, fields ...zap.Field) {
	c.log(ctx, WARN, event, msg, fields...)
}

func (c Logger) Error(ctx context.Context, event, msg string, fields ...zap.Field) {
	c.log(ctx, ERROR, event, msg, fields...)
}

// ---------------------------------------------------------------------------
// Legacy package-level API kept for existing call sites. Every helper routes
// through Log -> lineCore, so the same render path and redaction policy
// apply; all are nil-safe. Migrate to Component when touching a call site.
// ---------------------------------------------------------------------------

func Info(args ...interface{}) {
	if Log != nil {
		Log.Info(args...)
	}
}

func Infof(template string, args ...interface{}) {
	if Log != nil {
		Log.Infof(template, args...)
	}
}

// Infow logs msg with loosely typed key-value pairs through the shared core.
func Infow(msg string, keysAndValues ...interface{}) {
	if Log != nil {
		Log.Infow(msg, keysAndValues...)
	}
}

func Error(args ...interface{}) {
	if Log != nil {
		Log.Error(args...)
	}
}

func Errorf(template string, args ...interface{}) {
	if Log != nil {
		Log.Errorf(template, args...)
	}
}

func Errorw(msg string, keysAndValues ...interface{}) {
	if Log != nil {
		Log.Errorw(msg, keysAndValues...)
	}
}

func Debug(args ...interface{}) {
	if Log != nil {
		Log.Debug(args...)
	}
}

func Debugf(template string, args ...interface{}) {
	if Log != nil {
		Log.Debugf(template, args...)
	}
}

func Debugw(msg string, keysAndValues ...interface{}) {
	if Log != nil {
		Log.Debugw(msg, keysAndValues...)
	}
}

func Warn(args ...interface{}) {
	if Log != nil {
		Log.Warn(args...)
	}
}

func Warnf(template string, args ...interface{}) {
	if Log != nil {
		Log.Warnf(template, args...)
	}
}

func Warnw(msg string, keysAndValues ...interface{}) {
	if Log != nil {
		Log.Warnw(msg, keysAndValues...)
	}
}

func Fatal(args ...interface{}) {
	if Log != nil {
		Log.Fatal(args...)
	}
	os.Exit(1)
}

func Fatalf(template string, args ...interface{}) {
	if Log != nil {
		Log.Fatalf(template, args...)
	}
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// Legacy visual-hierarchy helpers. Deprecated: the unified format forbids
// tree glyphs and per-line forwarding; call sites are being migrated to
// Component events and bounded tool-output collection.
// ---------------------------------------------------------------------------

// IndentPrefix is deprecated and will be removed with the migration.
const IndentPrefix = "  ├─ "

// Indent is deprecated: use Component.
func Indent() IndentedLogger {
	return IndentedLogger{prefix: IndentPrefix}
}

// IndentedLogger is deprecated: use Component.
type IndentedLogger struct {
	prefix string
}

func (il IndentedLogger) Info(args ...interface{}) {
	if Log != nil {
		Log.Info(il.prefix, fmt.Sprint(args...))
	}
}

func (il IndentedLogger) Infof(template string, args ...interface{}) {
	if Log != nil {
		Log.Infof(il.prefix+template, args...)
	}
}

func (il IndentedLogger) Error(args ...interface{}) {
	if Log != nil {
		Log.Error(il.prefix, fmt.Sprint(args...))
	}
}

func (il IndentedLogger) Errorf(template string, args ...interface{}) {
	if Log != nil {
		Log.Errorf(il.prefix+template, args...)
	}
}

func (il IndentedLogger) Debug(args ...interface{}) {
	if Log != nil {
		Log.Debug(il.prefix, fmt.Sprint(args...))
	}
}

func (il IndentedLogger) Debugf(template string, args ...interface{}) {
	if Log != nil {
		Log.Debugf(il.prefix+template, args...)
	}
}

func (il IndentedLogger) Warn(args ...interface{}) {
	if Log != nil {
		Log.Warn(il.prefix, fmt.Sprint(args...))
	}
}

func (il IndentedLogger) Warnf(template string, args ...interface{}) {
	if Log != nil {
		Log.Warnf(il.prefix+template, args...)
	}
}

// InfoWriter is deprecated: it writes raw lines to stderr, bypassing the
// unified render path. It is kept only until subprocess collection migrates
// to bounded tool_* events.
type InfoWriter struct {
	prefix string
	buffer []byte
}

// NewInfoWriter is deprecated: see InfoWriter.
func NewInfoWriter() *InfoWriter {
	return &InfoWriter{
		prefix: "  │ ",
		buffer: make([]byte, 0, 1024),
	}
}

func (w *InfoWriter) Write(p []byte) (n int, err error) {
	w.buffer = append(w.buffer, p...)
	for {
		idx := bytes.IndexByte(w.buffer, '\n')
		if idx == -1 {
			idx = bytes.IndexByte(w.buffer, '\r')
			if idx == -1 {
				break
			}
		}
		line := string(w.buffer[:idx])
		if line != "" {
			fmt.Fprintf(os.Stderr, "%s%s\n", w.prefix, line)
		}
		w.buffer = w.buffer[idx+1:]
	}
	return len(p), nil
}
