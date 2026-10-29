package logger

import (
	"context"
	"regexp"
	"strconv"

	"github.com/google/uuid"
)

// ctxKey is the context key for correlation fields. Correlation IDs travel
// only via context.Context, never via shared mutable logger state, so
// concurrent jobs cannot cross-contaminate.
type ctxKey struct{}

func ctxCorr(ctx context.Context) correlation {
	if ctx == nil {
		return correlation{}
	}
	if c, ok := ctx.Value(ctxKey{}).(correlation); ok {
		return c
	}
	return correlation{}
}

func withCorr(ctx context.Context, set func(*correlation)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	c := ctxCorr(ctx)
	set(&c)
	return context.WithValue(ctx, ctxKey{}, c)
}

// WithJobID attaches the business Job ID (never a synthetic trace ID).
func WithJobID(ctx context.Context, id string) context.Context {
	return withCorr(ctx, func(c *correlation) { c.jobID = id })
}

// WithSubmissionID attaches a Submission ID (exists before a Job in Muse).
func WithSubmissionID(ctx context.Context, id string) context.Context {
	return withCorr(ctx, func(c *correlation) { c.submissionID = id })
}

// WithAttemptID attaches the real persisted attempt ID. A non-positive ID
// means "unknown" and is omitted rather than rendered as a fake identity.
// Use a plain zap.Int("retry", n) field for retry ordinals; they are not
// attempt IDs.
func WithAttemptID(ctx context.Context, id int64) context.Context {
	if id <= 0 {
		return ctx
	}
	return withCorr(ctx, func(c *correlation) { c.attemptID = strconv.FormatInt(id, 10) })
}

// WithRequestID attaches the request ID (see ValidRequestID).
func WithRequestID(ctx context.Context, id string) context.Context {
	return withCorr(ctx, func(c *correlation) { c.requestID = id })
}

// RequestID reports the request ID attached to ctx, or "" when there is none.
// Use it to carry the ID into queued work or an outbound X-Request-ID header.
func RequestID(ctx context.Context) string { return ctxCorr(ctx).requestID }

// WithParentJobID attaches the originating Job ID when a callback creates a
// new linked Job (rendered as parent_job_id, paired with job_linked events).
func WithParentJobID(ctx context.Context, id string) context.Context {
	return withCorr(ctx, func(c *correlation) { c.parentJobID = id })
}

var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidRequestID reports whether s is a legal caller-supplied request ID:
// ASCII alphanumerics, dot, underscore, hyphen; 1-64 chars. Anything else
// must be replaced with NewRequestID, never logged raw.
func ValidRequestID(s string) bool { return requestIDRe.MatchString(s) }

// NewRequestID generates a request ID for requests without a valid incoming
// one. Return it in the response so callers can correlate.
func NewRequestID() string { return uuid.NewString() }
