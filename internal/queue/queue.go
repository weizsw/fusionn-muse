package queue

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/fusionn-muse/pkg/logger"
)

var (
	ErrInvalidJob      = errors.New("invalid job")
	ErrDuplicateJobID  = errors.New("duplicate job ID")
	ErrQueueNotRunning = errors.New("queue is not running")
	ErrQueueStopping   = errors.New("queue is stopping")
	ErrQueueStopped    = errors.New("queue is stopped")
	ErrQueueFull       = errors.New("heavy queue is full")
)

type queueState uint8

const (
	queueNotRunning queueState = iota
	queueRunning
	queueStopping
	queueStopped
)

// Processor is the interface that processes a job.
type Processor interface {
	Process(ctx context.Context, job *Job) error
}

// Queue manages the sequential processing of jobs.
type Queue struct {
	mu       sync.RWMutex
	jobs     []*Job
	jobMap   map[string]*Job // For quick lookup by ID
	jobsChan chan *Job

	processor  Processor
	maxRetries int
	retryDelay time.Duration

	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	state     queueState
	stoppedCh chan struct{}
}

// New creates a new job queue.
func New(processor Processor, maxRetries, retryDelayMs int) *Queue {
	ctx, cancel := context.WithCancel(context.Background())

	q := &Queue{
		jobs:       make([]*Job, 0),
		jobMap:     make(map[string]*Job),
		jobsChan:   make(chan *Job, 100), // Buffer for incoming jobs
		processor:  processor,
		maxRetries: maxRetries,
		retryDelay: time.Duration(retryDelayMs) * time.Millisecond,
		ctx:        ctx,
		cancel:     cancel,
		stoppedCh:  make(chan struct{}),
	}

	return q
}

// Start begins the worker goroutine.
func (q *Queue) Start() {
	q.mu.Lock()
	if q.state != queueNotRunning {
		q.mu.Unlock()
		return
	}
	q.state = queueRunning
	q.wg.Add(1)
	q.mu.Unlock()

	go q.worker()
	logger.Info("📥 Job queue started (sequential processing)")
}

// Stop gracefully stops the queue.
func (q *Queue) Stop() {
	q.mu.Lock()
	switch q.state {
	case queueStopped:
		q.mu.Unlock()
		return
	case queueStopping:
		stoppedCh := q.stoppedCh
		q.mu.Unlock()
		<-stoppedCh
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

// Accept registers a valid Job and assigns its heavy or light execution.
func (q *Queue) Accept(job *Job) error {
	if job == nil || job.ID == "" || job.SourcePath == "" || job.FileName == "" || job.Status != StatusPending {
		return ErrInvalidJob
	}
	owned := *job

	q.mu.Lock()
	defer q.mu.Unlock()

	switch q.state {
	case queueStopping:
		return ErrQueueStopping
	case queueStopped:
		return ErrQueueStopped
	case queueNotRunning:
		return ErrQueueNotRunning
	}
	if _, exists := q.jobMap[owned.ID]; exists {
		return ErrDuplicateJobID
	}

	if owned.IsLight {
		q.wg.Add(1)
	} else {
		select {
		case q.jobsChan <- &owned:
		default:
			return ErrQueueFull
		}
	}

	q.jobs = append(q.jobs, &owned)
	q.jobMap[owned.ID] = &owned
	if owned.IsLight {
		go q.processLightJob(&owned)
	}
	return nil
}

// GetJob returns a snapshot of a job by ID.
func (q *Queue) GetJob(id string) *Job {
	q.mu.RLock()
	defer q.mu.RUnlock()
	job := q.jobMap[id]
	if job == nil {
		return nil
	}
	snapshot := *job
	return &snapshot
}

// GetAllJobs returns snapshots of all jobs.
func (q *Queue) GetAllJobs() []*Job {
	q.mu.RLock()
	defer q.mu.RUnlock()

	result := make([]*Job, len(q.jobs))
	for i, job := range q.jobs {
		snapshot := *job
		result[i] = &snapshot
	}
	return result
}

// GetPendingJobs returns snapshots of all pending jobs.
func (q *Queue) GetPendingJobs() []*Job {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var pending []*Job
	for _, job := range q.jobs {
		if job.Status == StatusPending {
			snapshot := *job
			pending = append(pending, &snapshot)
		}
	}
	return pending
}

// GetQueueStats returns queue statistics.
func (q *Queue) GetQueueStats() map[string]int {
	q.mu.RLock()
	defer q.mu.RUnlock()

	// Count heavy jobs (queued jobs)
	heavyPending := 0
	heavyProcessing := 0
	heavyCompleted := 0
	heavyFailed := 0

	lightCompleted := 0
	lightFailed := 0
	for _, job := range q.jobs {
		if job.IsLight {
			switch job.Status {
			case StatusCompleted:
				lightCompleted++
			case StatusFailed:
				lightFailed++
			}
			continue
		}
		switch job.Status {
		case StatusPending:
			heavyPending++
		case StatusProcessing:
			heavyProcessing++
		case StatusCompleted:
			heavyCompleted++
		case StatusFailed:
			heavyFailed++
		}
	}

	return map[string]int{
		"total":            len(q.jobs),
		"heavy_pending":    heavyPending,
		"heavy_processing": heavyProcessing,
		"heavy_completed":  heavyCompleted,
		"heavy_failed":     heavyFailed,
		"light_completed":  lightCompleted,
		"light_failed":     lightFailed,
	}
}

// worker processes heavy jobs sequentially.
func (q *Queue) worker() {
	defer q.wg.Done()

	for {
		if q.ctx.Err() != nil {
			q.cancelPendingJobs()
			return
		}
		select {
		case <-q.ctx.Done():
			q.cancelPendingJobs()
			return
		case job := <-q.jobsChan:
			q.processHeavyJob(job)
		}
	}
}

func (q *Queue) processHeavyJob(job *Job) {
	maxAttempts := q.maxRetries
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := q.ctx.Err(); err != nil {
			q.failJob(job, err)
			return
		}
		if err := q.runAttempt(job, attempt, attempt == maxAttempts); err == nil || attempt == maxAttempts {
			return
		}

		timer := time.NewTimer(q.retryDelay)
		select {
		case <-timer.C:
		case <-q.ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			q.failJob(job, q.ctx.Err())
			return
		}
	}
}

func (q *Queue) processLightJob(job *Job) {
	defer q.wg.Done()
	_ = q.runAttempt(job, 1, true)
}

func (q *Queue) runAttempt(job *Job, attempt int, isFinalAttempt bool) error {
	q.mu.Lock()
	job.Status = StatusProcessing
	job.Error = ""
	job.Retries = attempt - 1
	job.CompletedAt = time.Time{}
	if job.StartedAt.IsZero() {
		job.StartedAt = time.Now()
	}
	jobID := job.ID
	createdAt := job.CreatedAt
	startedAt := job.StartedAt
	attemptJob := *job
	q.mu.Unlock()

	ctx := logger.WithAttempt(logger.WithJob(q.ctx, job.ID), attempt)
	log := logger.FromContext(ctx)
	log.Infof("🔄 Processing job: %s", attemptJob.FileName)
	err := q.processor.Process(ctx, &attemptJob)

	q.mu.Lock()
	*job = attemptJob
	job.ID = jobID
	job.CreatedAt = createdAt
	job.StartedAt = startedAt
	job.Retries = attempt - 1
	job.Error = ""
	job.CompletedAt = time.Time{}
	if err == nil {
		job.Status = StatusCompleted
		job.CompletedAt = time.Now()
	} else {
		job.Retries = attempt
		job.Error = err.Error()
		if isFinalAttempt {
			job.Status = StatusFailed
			job.CompletedAt = time.Now()
		} else {
			job.Status = StatusPending
		}
	}
	q.mu.Unlock()

	if err == nil {
		log.Info("✅ Job completed")
	} else if isFinalAttempt {
		log.Errorf("❌ Job failed after %d attempt(s): %v", attempt, err)
	} else {
		log.Warnf("⚠️ Job failed (attempt %d/%d): %v", attempt, q.maxRetries, err)
	}
	return err
}

func (q *Queue) cancelPendingJobs() {
	for {
		select {
		case job := <-q.jobsChan:
			q.failJob(job, q.ctx.Err())
		default:
			return
		}
	}
}

func (q *Queue) failJob(job *Job, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job.Status = StatusFailed
	job.Error = err.Error()
	job.CompletedAt = time.Now()
}
