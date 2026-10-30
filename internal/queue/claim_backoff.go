package queue

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/fusionn-muse/pkg/logger"
)

// The wait of a claim loop after a failed claim. It works like the Redis
// backoff of fusionn-subs: the wait doubles up to the cap, so a failing
// database leaves a few lines instead of one every 100ms.
const (
	claimRetryInitial = time.Second
	claimRetryMax     = 30 * time.Second
)

// claimBackoff is the wait of one claim loop after failed claims. The zero
// value is ready to use.
type claimBackoff struct{ wait time.Duration }

// failed logs a failed claim as one WARN with the wait before the next try,
// then waits. It reports false when ctx ends during the wait.
func (b *claimBackoff) failed(ctx context.Context, log logger.Logger, msg string, err error) bool {
	b.wait = min(max(2*b.wait, claimRetryInitial), claimRetryMax)
	log.Warn(ctx, "claim_failed", msg, zap.Int64("retry_after_ms", b.wait.Milliseconds()), zap.Error(err))
	timer := time.NewTimer(b.wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// reset starts the wait over after a successful claim.
func (b *claimBackoff) reset() { b.wait = 0 }
