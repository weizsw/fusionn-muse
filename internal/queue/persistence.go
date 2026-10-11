package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fusionn-muse/pkg/logger"
)

type persistenceFailure struct{ message string }

func (q *Queue) persist(ctx context.Context, jobID, operation string, write func() error) error {
	var last *persistenceFailure
	defer func() {
		if last != nil {
			q.persistenceErrors.CompareAndDelete(jobID, last)
		}
	}()
	for {
		err := write()
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidAction) || errors.Is(err, ErrArtifactConflict) || errors.Is(err, ErrArtifactUnavailable) {
			return err
		}
		message := fmt.Sprintf("%s: %v", operation, err)
		if last == nil || last.message != message {
			last = &persistenceFailure{message: message}
			q.persistenceErrors.Store(jobID, last)
			logger.FromContext(ctx).Errorf("%s; retaining result", message)
		}
		if !waitPersistence(ctx) {
			return ctx.Err()
		}
	}
}

func (q *Queue) persistProgress(ctx context.Context, job *Job, stage Stage, checkpoint bool) error {
	return q.persist(ctx, job.ID, "persist stage progress", func() error { return q.store.updateProgress(ctx, job, stage, checkpoint) })
}

func (q *Queue) attachPersistenceError(job *Job) {
	if value, ok := q.persistenceErrors.Load(job.ID); ok {
		job.PersistenceError = value.(*persistenceFailure).message
	}
}

func waitPersistence(ctx context.Context) bool {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
