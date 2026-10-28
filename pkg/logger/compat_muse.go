package logger

import (
	"context"
	"strconv"
)

// This file is this repository's access to state the shared core keeps outside
// the correlation block:
//
//   - the retry ordinal, because the Muse -> host-asr request carries it as its
//     existing `attempt` field. An ordinal is never an execution identity: log
//     events write it as a `retry` field, and WithAttemptID carries the
//     persisted attempt ID.
//   - readers for the correlation the shared core writes, for call sites that
//     must put an ID into a payload rather than into a log line.
//
// The free-form pre-migration logger is gone; every call site writes structured
// events through Component("...").Info/Debug/Warn/Error.

// ordinalKey holds the retry ordinal (1 for the first attempt).
type ordinalKey struct{}

// WithAttempt attaches the retry ordinal.
func WithAttempt(ctx context.Context, ordinal int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ordinalKey{}, ordinal)
}

// Attempt reports the retry ordinal attached to ctx, or 0 when there is none.
func Attempt(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	if ordinal, ok := ctx.Value(ordinalKey{}).(int); ok {
		return ordinal
	}
	return 0
}

// JobID reports the business Job ID attached to ctx.
func JobID(ctx context.Context) string { return ctxCorr(ctx).jobID }

// AttemptID reports the persisted attempt ID attached to ctx, or 0 when there
// is none.
func AttemptID(ctx context.Context) int64 {
	persisted, err := strconv.ParseInt(ctxCorr(ctx).attemptID, 10, 64)
	if err != nil {
		return 0
	}
	return persisted
}
