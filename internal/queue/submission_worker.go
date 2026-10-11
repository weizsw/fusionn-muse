package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/pkg/logger"
	"github.com/google/uuid"
)

const submissionPreparationTimeout = 3 * time.Hour

// Two adapters implement this seam: the media processor and fault-injection tests.
// Cleanup releases only preparation-owned artifacts, never the submitted input.
type submissionPreparer interface {
	PrepareSubmission(context.Context, *Submission) (*mediaintake.ResolvedMedia, func() error, error)
}

var ErrInvalidSubmission = errors.New("invalid submission")

// Submit acknowledges only a committed input and pending Preparation run.
// It performs no media filesystem operations on the calling goroutine.
func (q *Queue) Submit(ctx context.Context, path, name, category string) (*Submission, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidSubmission
	}
	ctx, cancel, err := q.submissionOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	snapshot, err := q.submissionSettings()
	if err != nil {
		return nil, err
	}
	submission := &Submission{ID: uuid.NewString(), SourcePath: path, TorrentName: name, Category: category}
	if err := q.store.insertSubmission(ctx, submission, snapshot); err != nil {
		return nil, err
	}
	q.wake(nil)
	return submission, nil
}

// RecoverSubmission queues preparation again without editing the original input.
// Recovery retains the run being retried or resumed. Current settings require
// explicit selection.
func (q *Queue) RecoverSubmission(ctx context.Context, id string, useCurrentSettings bool) (*Submission, error) {
	return q.recoverSubmission(ctx, id, useCurrentSettings, "")
}

func (q *Queue) recoverSubmission(ctx context.Context, id string, useCurrentSettings bool, action string) (*Submission, error) {
	ctx, cancel, err := q.submissionOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	var current *string
	if useCurrentSettings {
		settings, err := q.submissionSettings()
		if err != nil {
			return nil, err
		}
		current = &settings
	}
	submission, err := q.store.recoverSubmission(ctx, id, current, action)
	if err != nil {
		return nil, err
	}
	q.wake(nil)
	return submission, nil
}

func (q *Queue) submissionOperation(ctx context.Context) (context.Context, context.CancelFunc, error) {
	q.mu.Lock()
	if q.state != queueRunning {
		q.mu.Unlock()
		return nil, nil, ErrQueueNotRunning
	}
	if _, ok := q.processor.(submissionPreparer); !ok {
		q.mu.Unlock()
		return nil, nil, fmt.Errorf("%w: media preparation unavailable", ErrInvalidSubmission)
	}
	q.wg.Add(1)
	q.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	stop := context.AfterFunc(q.ctx, cancel)
	return ctx, func() { stop(); cancel(); q.wg.Done() }, nil
}

func (q *Queue) submissionSettings() (string, error) {
	if snapshotter, ok := q.processor.(settingsSnapshotter); ok {
		return snapshotter.SnapshotSettings()
	}
	return "", nil
}

func (q *Queue) GetSubmission(id string) (*Submission, error) {
	return q.store.submission(id)
}

func (q *Queue) GetSubmissionDetail(ctx context.Context, id string) (*SubmissionDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return q.store.submissionDetail(ctx, id)
}

func (q *Queue) submissionWorker() {
	defer q.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		q.mu.RLock()
		if q.state != queueRunning {
			q.mu.RUnlock()
			return
		}
		submission, err := q.store.claimSubmission(q.ctx)
		q.mu.RUnlock()
		if err != nil && q.ctx.Err() == nil {
			logger.Errorf("claim preparation: %v", err)
		} else if submission != nil {
			q.prepareSubmission(submission)
			continue
		}
		select {
		case <-q.ctx.Done():
			return
		case <-q.jobsChan:
		case <-ticker.C:
		}
	}
}

func (q *Queue) prepareSubmission(submission *Submission) {
	ctx, cancel := context.WithTimeout(logger.WithSubmission(q.ctx, submission.ID), submissionPreparationTimeout)
	log := logger.FromContext(ctx)
	log.Infof("Preparing run %d: %s", submission.RunID, submission.SourcePath)
	media, cleanup, prepareErr := q.processor.(submissionPreparer).PrepareSubmission(ctx, submission)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		prepareErr = fmt.Errorf("media preparation timed out after %s: %w", submissionPreparationTimeout, ctx.Err())
	} else if ctx.Err() != nil {
		prepareErr = ctx.Err()
	}
	cancel()
	retained := false
	defer func() {
		if cleanup != nil && !retained {
			if err := cleanup(); err != nil {
				log.Errorf("release preparation artifacts: %v", err)
			}
		}
	}()
	var job *Job
	if prepareErr == nil {
		if media == nil || media.SourcePath == "" || media.FileName == "" || media.StagingPath == "" {
			prepareErr = errors.New("media preparer returned incomplete media")
		} else {
			job = NewJob(uuid.NewString(), media.SourcePath, media.FileName, submission.TorrentName, submission.Category)
			job.IsLight = media.HasChineseSubtitle
			job.SubtitleDetectionReason = media.SubtitleDetectionReason
			job.SidecarSubtitlePath = media.SidecarSubtitlePath
			job.StagingPath = media.StagingPath
		}
	}
	linked, err := q.persistPreparationResult(submission, job, prepareErr)
	if err != nil {
		// A failed commit can have an uncertain outcome. Preserve media until
		// its durable ownership can be established after storage recovers.
		retained = true
		log.Errorf("persist preparation result: %v", err)
		return
	}
	if linked == nil {
		if q.ctx.Err() == nil {
			if errors.Is(prepareErr, mediaintake.ErrNoValidMedia) {
				log.Infof("Ignored: %v", prepareErr)
			} else {
				log.Errorf("Preparation failed: %v", prepareErr)
			}
		}
		return
	}
	if linked.ID != job.ID {
		log.Infof("Resolved to existing Job %s", linked.ID)
		return
	}
	// Job ownership transfers at the same commit as its Submission link.
	// Keep prepared media even when its initial admission fails or is interrupted.
	retained = true
	log.Infof("Resolved to Job %s", linked.ID)
	if linked.AttemptStatus != AttemptProcessing {
		return
	}
	if admissionErr := q.prepareAdmission(linked); admissionErr != nil {
		if q.ctx.Err() != nil {
			admissionErr = q.ctx.Err()
		}
		for {
			if _, _, err := q.store.finish(linked, admissionErr, 0); err == nil {
				break
			} else {
				log.Errorf("persist initial admission failure: %v", err)
			}
			var status AttemptStatus
			if err := q.store.db.QueryRow(`SELECT status FROM attempts WHERE id=?`, linked.AttemptID).Scan(&status); err == nil && status != AttemptProcessing {
				break
			}
			if !q.waitPreparationWrite() {
				return
			}
		}
		if q.ctx.Err() == nil {
			log.Errorf("Job %s initial admission failed: %v", linked.ID, admissionErr)
		}
		return
	}
	// After the prepared checkpoint commits, a separate source cache is no
	// longer required. Keep caches used directly as Job staging artifacts.
	retained = linked.SourcePath == linked.StagingPath
	q.wake(linked)
}

// Keep the prepared result while storage recovers; never rerun extraction or
// discard a live claim just because committing its outcome failed.
func (q *Queue) persistPreparationResult(submission *Submission, job *Job, prepareErr error) (*Job, error) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var linked *Job
		var err error
		if q.ctx.Err() != nil {
			job, prepareErr = nil, q.ctx.Err()
		}
		if job != nil {
			linked, err = q.store.resolveSubmission(ctx, submission, job)
		} else {
			status := SubmissionFailed
			if errors.Is(prepareErr, mediaintake.ErrNoValidMedia) {
				status = SubmissionIgnored
			}
			if q.ctx.Err() != nil {
				status = SubmissionInterrupted
			}
			err = q.store.finishSubmission(ctx, submission, status, prepareErr.Error())
		}
		cancel()
		if err == nil || errors.Is(err, ErrSubmissionNotFound) || errors.Is(err, ErrInvalidSubmissionAction) {
			return linked, err
		}
		logger.FromContext(logger.WithSubmission(q.ctx, submission.ID)).Errorf("persist preparation outcome; retaining result: %v", err)
		if !q.waitPreparationWrite() {
			return nil, err
		}
	}
}

func (q *Queue) waitPreparationWrite() bool {
	select {
	case <-q.ctx.Done():
		return false
	case <-time.After(time.Second):
		return true
	}
}
