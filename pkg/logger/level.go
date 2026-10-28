package logger

import (
	"strings"

	"go.uber.org/zap/zapcore"
)

// Level aliases zapcore.Level: one type, no mapping tables. The unified set
// is DEBUG/INFO/WARN/ERROR; Python's WARNING maps to WARN.
type Level = zapcore.Level

const (
	DEBUG = zapcore.DebugLevel
	INFO  = zapcore.InfoLevel
	WARN  = zapcore.WarnLevel
	ERROR = zapcore.ErrorLevel
)

// ParseLevel parses DEBUG/INFO/WARN/ERROR (case-insensitive). "WARNING" is
// accepted as an alias for WARN so Python-style values map cleanly.
func ParseLevel(s string) (Level, bool) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return DEBUG, true
	case "INFO":
		return INFO, true
	case "WARN", "WARNING":
		return WARN, true
	case "ERROR":
		return ERROR, true
	default:
		return INFO, false
	}
}
