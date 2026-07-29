package logger

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var Log *zap.SugaredLogger

type correlation struct {
	jobID   string
	attempt int
}

type correlationKey struct{}

// WithJob adds a Job ID to ctx for correlated logging.
func WithJob(ctx context.Context, jobID string) context.Context {
	return context.WithValue(ctx, correlationKey{}, correlation{jobID: jobID})
}

// WithAttempt adds an Attempt number while preserving the Job ID in ctx.
func WithAttempt(ctx context.Context, attempt int) context.Context {
	value, _ := ctx.Value(correlationKey{}).(correlation)
	value.attempt = attempt
	return context.WithValue(ctx, correlationKey{}, value)
}

func JobID(ctx context.Context) string {
	value, _ := ctx.Value(correlationKey{}).(correlation)
	return value.jobID
}

func Attempt(ctx context.Context) int {
	value, _ := ctx.Value(correlationKey{}).(correlation)
	return value.attempt
}

// Prefix returns the stable text prefix used to grep one Job's logs.
func Prefix(ctx context.Context) string {
	jobID := JobID(ctx)
	if jobID == "" {
		return ""
	}
	return jobID + " "
}

// ContextLogger prefixes each physical line with correlation data from ctx.
type ContextLogger struct {
	prefix string
}

func FromContext(ctx context.Context) ContextLogger {
	return ContextLogger{prefix: Prefix(ctx)}
}

func (l ContextLogger) write(write func(...interface{}), message string) {
	if l.prefix == "" {
		write(message)
		return
	}
	message = strings.TrimRight(message, "\n")
	for _, line := range strings.Split(message, "\n") {
		write(l.prefix + line)
	}
}

func (l ContextLogger) Info(args ...interface{}) {
	l.write(Log.Info, fmt.Sprint(args...))
}
func (l ContextLogger) Infof(template string, args ...interface{}) {
	l.write(Log.Info, fmt.Sprintf(template, args...))
}
func (l ContextLogger) Errorf(template string, args ...interface{}) {
	l.write(Log.Error, fmt.Sprintf(template, args...))
}
func (l ContextLogger) Debugf(template string, args ...interface{}) {
	l.write(Log.Debug, fmt.Sprintf(template, args...))
}
func (l ContextLogger) Warn(args ...interface{}) {
	l.write(Log.Warn, fmt.Sprint(args...))
}
func (l ContextLogger) Warnf(template string, args ...interface{}) {
	l.write(Log.Warn, fmt.Sprintf(template, args...))
}

func Init(isDev bool) {
	var encoder zapcore.Encoder
	var level zapcore.Level

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:       "time",
		LevelKey:      "level",
		MessageKey:    "msg",
		StacktraceKey: "", // Hide stacktrace in normal logs
		EncodeTime:    customTimeEncoder,
		EncodeCaller:  nil, // Hide caller
	}

	if isDev {
		// Development: colorful console output
		level = zapcore.DebugLevel
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		encoderConfig.ConsoleSeparator = " "
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	} else {
		// Production: clean console output (no JSON)
		level = zapcore.InfoLevel
		encoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
		encoderConfig.ConsoleSeparator = " "
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	}

	core := zapcore.NewCore(
		encoder,
		zapcore.AddSync(os.Stdout),
		level,
	)

	logger := zap.New(core)
	Log = logger.Sugar()
}

// customTimeEncoder formats time as "2006-01-02 15:04:05" for logs
func customTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("2006-01-02 15:04:05"))
}

func Sync() {
	if Log != nil {
		_ = Log.Sync() //nolint:errcheck // Sync can fail on stdout/stderr, safe to ignore
	}
}

// Convenience methods
func Info(args ...interface{})                    { Log.Info(args...) }
func Infof(template string, args ...interface{})  { Log.Infof(template, args...) }
func Error(args ...interface{})                   { Log.Error(args...) }
func Errorf(template string, args ...interface{}) { Log.Errorf(template, args...) }
func Debug(args ...interface{})                   { Log.Debug(args...) }
func Debugf(template string, args ...interface{}) { Log.Debugf(template, args...) }
func Warn(args ...interface{})                    { Log.Warn(args...) }
func Warnf(template string, args ...interface{})  { Log.Warnf(template, args...) }
func Fatal(args ...interface{})                   { Log.Fatal(args...); os.Exit(1) }
func Fatalf(template string, args ...interface{}) { Log.Fatalf(template, args...); os.Exit(1) }
