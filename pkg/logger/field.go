package logger

import (
	"time"

	"go.uber.org/zap"
)

// Policy helpers only. Callers otherwise build fields with the standard
// zap constructors (zap.String, zap.Int, zap.Bool, zap.Error, ...).

// DurMs records d under the canonical duration_ms key as integer milliseconds.
func DurMs(d time.Duration) zap.Field { return zap.Int64("duration_ms", d.Milliseconds()) }

// Preview records a content excerpt under the "preview" key. The renderer
// applies the shared policy by key: emitted only at DEBUG, redacted, capped
// at 512 Unicode characters, truncated=true appended when capped. At most one
// preview per event; never a per-line replay of third-party output.
func Preview(value string) zap.Field { return zap.String("preview", value) }
