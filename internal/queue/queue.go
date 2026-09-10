package queue

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fusionn-muse/internal/fileops"
	"github.com/fusionn-muse/pkg/logger"
)

var (
	ErrInvalidJob      = errors.New("invalid job")
	ErrDuplicateJobID  = errors.New("duplicate job ID")
	ErrQueueNotRunning = errors.New("queue is not running")
	ErrQueueStopping   = errors.New("queue is stopping")
	ErrQueueStopped    = errors.New("queue is stopped")
	// Kept for API compatibility. Durable admission has no fixed queue capacity.
	ErrQueueFull = errors.New("heavy queue is full")
)

type queueState uint8

const (
	queueNotRunning queueState = iota
	queueRunning
	queueStopping
	queueStopped
)

// Processor runs one durable Job Attempt.

type Processor interface {
	Process(ctx context.Context, job *Job) error
}

type failureNotifier interface {
	NotifyFailure(context.Context, *Job, Stage, error)
}

type settingsSnapshotter interface {
	SnapshotSettings() (string, error)
}

// Queue manages durable heavy and light Job processing.

type Queue struct {
	mu       sync.RWMutex
	jobsChan chan *Job

	store      *store
	processor  Processor
	maxRetries int
	retryDelay time.Duration

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	state     queueState
	stoppedCh chan struct{}
}

var memoryQueueID atomic.Uint64

// New creates an in-memory queue. Production should use Open with a durable path.
func New(processor Processor, maxRetries, retryDelayMs int) *Queue {
	dsn := fmt.Sprintf("file:queue-%d?mode=memory&cache=shared", memoryQueueID.Add(1))
	q, err := Open(dsn, processor, maxRetries, retryDelayMs)
	if err != nil {
		panic(err)
	}
	return q
}

// Open creates a queue backed by SQLite at path.
func Open(path string, processor Processor, maxRetries, retryDelayMs int) (*Queue, error) {
	store, err := openStore(path)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Queue{
		jobsChan:   make(chan *Job, 100),
		store:      store,
		processor:  processor,
		maxRetries: maxRetries,
		retryDelay: time.Duration(retryDelayMs) * time.Millisecond,
		ctx:        ctx,
		cancel:     cancel,
		stoppedCh:  make(chan struct{}),
	}, nil
}

// Start begins the heavy and light worker goroutines.

func (q *Queue) Start() {
	q.mu.Lock()
	if q.state != queueNotRunning {
		q.mu.Unlock()
		return
	}
	q.state = queueRunning
	q.wg.Add(2)
	q.mu.Unlock()
	go q.worker(false)
	go q.worker(true)
	logger.Info("📥 Job queue started (durable heavy/light processing)")
}

// Stop gracefully stops the queue.

func (q *Queue) Stop() {
	q.mu.Lock()
	switch q.state {
	case queueStopped:
		q.mu.Unlock()
		return
	case queueStopping:
		stopped := q.stoppedCh
		q.mu.Unlock()
		<-stopped
		return
	}
	q.state = queueStopping
	q.cancel()
	q.mu.Unlock()

	logger.Info("🛑 Stopping job queue...")
	q.wg.Wait()

	q.mu.Lock()
	q.state = queueStopped
	close(q.stoppedCh)
	q.mu.Unlock()
	logger.Info("✅ Job queue stopped")
}

// Close stops the queue and closes its durable store.

func (q *Queue) Close() error {
	q.Stop()
	return q.store.db.Close()
}

// Accept persists a Job before workers can observe it.
func (q *Queue) Accept(job *Job) error {
	if job == nil || job.ID == "" || job.SourcePath == "" || job.FileName == "" || job.Status != StatusPending {
		return ErrInvalidJob
	}
	owned := *job
	if snapshotter, ok := q.processor.(settingsSnapshotter); ok {
		snapshot, err := snapshotter.SnapshotSettings()
		if err != nil {
			return err
		}
		owned.SettingsSnapshot = snapshot
	}

	q.mu.Lock()
	switch q.state {
	case queueStopping:
		q.mu.Unlock()
		return ErrQueueStopping
	case queueStopped:
		q.mu.Unlock()
		return ErrQueueStopped
	case queueNotRunning:
		q.mu.Unlock()
		return ErrQueueNotRunning
	}
	q.wg.Add(1)
	q.mu.Unlock()
	defer q.wg.Done()

	if err := q.store.reserveJob(&owned); err != nil {
		return err
	}
	staged := owned.StagingPath != ""
	if staged && owned.SourcePath != owned.StagingPath {
		if signature := fileSignature(owned.StagingPath); signature != "" {
			if signature != owned.ContentSignature {
				return q.failAdmission(&owned, fmt.Errorf("staging destination already contains different media: %s", owned.StagingPath))
			}
		} else if err := fileops.HardlinkOrCopyNoReplace(q.ctx, owned.SourcePath, owned.StagingPath); err != nil {
			return q.failAdmission(&owned, fmt.Errorf("stage media: %w", err))
		}
	}
	if err := q.store.completeAdmission(&owned, staged); err != nil {
		return q.failAdmission(&owned, fmt.Errorf("persist prepared admission: %w", err))
	}
	q.wake(&owned)
	return nil
}

func (q *Queue) failAdmission(job *Job, admissionErr error) error {
	if _, _, err := q.store.finish(job, admissionErr, 0); err != nil {
		return fmt.Errorf("%v; persist admission failure: %w", admissionErr, err)
	}
	return admissionErr
}

func (q *Queue) wake(job *Job) {
	select {
	case q.jobsChan <- job:
	default:
	}
}

// GetJob returns a snapshot of a Job by ID.

func (q *Queue) GetJob(id string) *Job {
	job, err := q.store.job(id)
	if err != nil {
		return nil
	}
	return job
}

// FindJobByMedia returns the tracked Job matching a file's full media identity.
func (q *Queue) FindJobByMedia(path, fileName string) *Job {
	job, err := q.store.jobByIdentity(fileSignature(path), MediaID(fileName))
	if err != nil {
		return nil
	}
	return job
}

// GetJobDetail returns a Job and all of its Attempts.

func (q *Queue) GetJobDetail(id string) *JobDetail {
	job, err := q.store.job(id)
	if err != nil {
		return nil
	}
	attempts, err := q.store.attempts(id)
	if err != nil {
		return nil
	}
	return &JobDetail{Job: *job, Attempts: attempts}
}

// GetAllJobs returns snapshots of all Jobs.

func (q *Queue) GetAllJobs() []*Job {
	jobs, err := q.store.list("")
	if err != nil {
		logger.Errorf("list jobs: %v", err)
		return nil
	}
	return jobs
}

// ListJobs returns newest-first Job history and an optional continuation cursor.
func (q *Queue) ListJobs(cursor string, limit int) ([]*Job, string, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}
	var before int64
	var beforeID string
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("invalid cursor")
		}
		value, id, ok := strings.Cut(string(decoded), ":")
		if !ok || id == "" {
			return nil, "", fmt.Errorf("invalid cursor")
		}
		before, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, "", fmt.Errorf("invalid cursor")
		}
		beforeID = id
	}
	jobs, err := q.store.listPage(before, beforeID, limit+1)
	if err != nil {
		return nil, "", err
	}
	if jobs == nil {
		jobs = []*Job{}
	}
	if len(jobs) <= limit {
		return jobs, "", nil
	}
	jobs = jobs[:limit]
	last := jobs[len(jobs)-1]
	return jobs, base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%s", last.CreatedAt.UnixNano(), last.ID))), nil
}

// GetPendingJobs returns snapshots of all pending Jobs.

func (q *Queue) GetPendingJobs() []*Job {
	jobs, err := q.store.list("WHERE status='pending'")
	if err != nil {
		logger.Errorf("list pending jobs: %v", err)
		return nil
	}
	return jobs
}

// GetQueueStats returns persisted Job counts grouped by status.

func (q *Queue) GetQueueStats() map[string]int {
	jobs := q.GetAllJobs()
	stats := map[string]int{
		"total": 0, "heavy_pending": 0, "heavy_processing": 0,
		"heavy_completed": 0, "heavy_failed": 0,
		"light_completed": 0, "light_failed": 0,
	}
	for _, job := range jobs {
		stats["total"]++
		prefix := "heavy_"
		if job.IsLight {
			prefix = "light_"
		}
		switch job.Status {
		case StatusPending:
			stats[prefix+"pending"]++
		case StatusProcessing:
			stats[prefix+"processing"]++
		case StatusCompleted:
			stats[prefix+"completed"]++
		case StatusFailed, StatusInterrupted:
			stats[prefix+"failed"]++
		}
	}
	return stats
}

// Retry starts a manual retry for a failed Job.
func (q *Queue) Retry(id string) error {
	_, err := q.RetryAttempt(id)
	return err
}

// RetryAttempt starts a manual retry and returns the durable Attempt.
func (q *Queue) RetryAttempt(id string) (*Attempt, error) {
	return q.createAction(id, AttemptManual)
}

// RetryFrom starts a failed Job using replacement media at path.
func (q *Queue) RetryFrom(id, path string) error {
	_, err := q.createActionFrom(id, AttemptManual, path)
	return err
}

// RetryFromAttempt starts a failed Job with replacement media and returns the durable Attempt.
func (q *Queue) RetryFromAttempt(id, path string) (*Attempt, error) {
	return q.createActionFrom(id, AttemptManual, path)
}

// Resume continues an interrupted Job from its last checkpoint.
func (q *Queue) Resume(id string) error {
	_, err := q.ResumeAttempt(id)
	return err
}

// ResumeAttempt continues an interrupted Job and returns the durable Attempt.
func (q *Queue) ResumeAttempt(id string) (*Attempt, error) {
	return q.createAction(id, AttemptResume)
}

// Retranslate reruns translation from the persisted transcription.
func (q *Queue) Retranslate(id string) error {
	_, err := q.RetranslateAttempt(id)
	return err
}

// RetranslateAttempt reruns translation and returns the durable Attempt.
func (q *Queue) RetranslateAttempt(id string) (*Attempt, error) {
	return q.createAction(id, AttemptRetranslate)
}

func (q *Queue) createAction(id string, kind AttemptKind) (*Attempt, error) {
	return q.createActionFrom(id, kind, "")
}

func (q *Queue) createActionFrom(id string, kind AttemptKind, path string) (*Attempt, error) {
	q.mu.RLock()
	state := q.state
	q.mu.RUnlock()
	if state != queueRunning {
		return nil, ErrQueueNotRunning
	}
	var settings string
	if kind == AttemptRetranslate {
		if snapshotter, ok := q.processor.(settingsSnapshotter); ok {
			var err error
			settings, err = snapshotter.SnapshotSettings()
			if err != nil {
				return nil, err
			}
		}
	}
	attempt, err := q.store.createAction(id, kind, settings, path)
	if err != nil {
		return nil, err
	}
	q.wake(&Job{ID: id, IsLight: attempt.StartStage == StageDelivering || attempt.StartStage == StageTranslating})
	return attempt, nil
}

func (q *Queue) worker(light bool) {
	defer q.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		q.mu.RLock()
		if q.state != queueRunning {
			q.mu.RUnlock()
			return
		}
		job, err := q.store.claimNext(light, time.Now().Add(-q.retryDelay))
		q.mu.RUnlock()
		if err != nil {
			logger.Errorf("claim queued attempt: %v", err)
		} else if job != nil {
			if light {
				q.launchLight(job)
			} else {
				q.processAttempt(job)
			}
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

func (q *Queue) launchLight(job *Job) {
	q.mu.Lock()
	q.wg.Add(1)
	q.mu.Unlock()
	go func() {
		defer q.wg.Done()
		q.processAttempt(job)
	}()
}

func (q *Queue) processAttempt(job *Job) {
	for {
		job.progress = q.store.updateProgress
		job.settings = q.store.saveSettings
		ctx := logger.WithAttempt(logger.WithJob(q.ctx, job.ID), job.Retries+1)
		log := logger.FromContext(ctx)
		log.Infof("🔄 Processing job: %s", job.FileName)

		lifecycle := *job
		processErr := q.processor.Process(ctx, job)
		if processErr != nil && q.ctx.Err() != nil {
			processErr = q.ctx.Err()
		}
		job.ID = lifecycle.ID
		job.MediaID = lifecycle.MediaID
		job.Status = lifecycle.Status
		job.Retries = lifecycle.Retries
		job.CreatedAt = lifecycle.CreatedAt
		job.StartedAt = lifecycle.StartedAt
		job.CompletedAt = lifecycle.CompletedAt
		job.AttemptID = lifecycle.AttemptID
		job.AttemptKind = lifecycle.AttemptKind
		job.StartStage = lifecycle.StartStage
		job.TranslationCacheEnabled = lifecycle.TranslationCacheEnabled

		failureStage, next, persistErr := q.store.finish(job, processErr, q.maxRetries)
		if persistErr != nil {
			log.Errorf("persist attempt result: %v", persistErr)
			return
		}
		if processErr == nil {
			log.Info("✅ Job completed")
			return
		}
		if errors.Is(processErr, context.Canceled) {
			return
		}
		if next == nil {
			log.Errorf("❌ Job failed at %s after %d attempt(s): %v", failureStage, job.Retries+1, processErr)
			q.notifyFailure(ctx, job, failureStage, processErr)
			return
		}
		log.Warnf("⚠️ Translation failed; retrying in %s: %v", q.retryDelay, processErr)
		timer := time.NewTimer(q.retryDelay)
		select {
		case <-timer.C:
			q.mu.RLock()
			if q.state != queueRunning {
				q.mu.RUnlock()
				return
			}
			claimed, claimErr := q.store.claimAttempt(next.AttemptID)
			q.mu.RUnlock()
			if claimErr != nil {
				log.Errorf("claim translation retry: %v", claimErr)
				return
			}
			if claimed == nil {
				return
			}
			job = claimed
		case <-q.ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func (q *Queue) notifyFailure(ctx context.Context, job *Job, stage Stage, err error) {
	if notifier, ok := q.processor.(failureNotifier); ok {
		notifier.NotifyFailure(ctx, job, stage, err)
	}
}
