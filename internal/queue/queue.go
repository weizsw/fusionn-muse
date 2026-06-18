package queue

import (
	"context"
	"sync"
	"time"

	"github.com/fusionn-muse/pkg/logger"
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

	// Light job counters (light jobs bypass the queue but we track them)
	lightCompleted int
	lightFailed    int

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
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
	}

	return q
}

// Start begins the worker goroutine.
func (q *Queue) Start() {
	q.wg.Add(1)
	go q.worker()
	logger.Info("📥 Job queue started (sequential processing)")
}

// Stop gracefully stops the queue.
func (q *Queue) Stop() {
	logger.Info("🛑 Stopping job queue...")
	q.cancel()
	q.wg.Wait()
	logger.Info("✅ Job queue stopped")
}

// Enqueue adds a new job to the queue.
func (q *Queue) Enqueue(job *Job) {
	q.registerJob(job)

	logger.Infof("📥 Job queued: %s (%s)", job.ID, job.FileName)

	// Non-blocking send to channel
	select {
	case q.jobsChan <- job:
	default:
		logger.Warnf("⚠️ Job channel full, job %s will be processed later", job.ID)
	}
}

func (q *Queue) registerJob(job *Job) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, job)
	q.jobMap[job.ID] = job
}

// GetJob returns a job by ID.
func (q *Queue) GetJob(id string) *Job {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.jobMap[id]
}

// GetAllJobs returns all jobs.
func (q *Queue) GetAllJobs() []*Job {
	q.mu.RLock()
	defer q.mu.RUnlock()

	result := make([]*Job, len(q.jobs))
	copy(result, q.jobs)
	return result
}

// GetPendingJobs returns all pending jobs.
func (q *Queue) GetPendingJobs() []*Job {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var pending []*Job
	for _, job := range q.jobs {
		if job.Status == StatusPending {
			pending = append(pending, job)
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

	for _, job := range q.jobs {
		if job.IsLight {
			continue // Light jobs tracked separately
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
		"total": len(q.jobs),
		// Heavy jobs (transcribe + translate)
		"heavy_pending":    heavyPending,
		"heavy_processing": heavyProcessing,
		"heavy_completed":  heavyCompleted,
		"heavy_failed":     heavyFailed,
		// Light jobs (skip transcribe/translate)
		"light_completed": q.lightCompleted,
		"light_failed":    q.lightFailed,
	}
}

// RunImmediate processes a light job now while still tracking it in queue stats.
func (q *Queue) RunImmediate(job *Job) {
	q.registerJob(job)
	go q.processImmediateJob(job)
}

// worker processes jobs sequentially.
func (q *Queue) worker() {
	defer q.wg.Done()

	for {
		select {
		case <-q.ctx.Done():
			return
		case job := <-q.jobsChan:
			q.processJob(job)
		}
	}
}

func (q *Queue) processJob(job *Job) {
	q.mu.Lock()
	job.Status = StatusProcessing
	job.StartedAt = time.Now()
	q.mu.Unlock()

	logger.Infof("🔄 Processing job: %s (%s)", job.ID, job.FileName)

	err := q.processor.Process(q.ctx, job)

	q.mu.Lock()
	defer q.mu.Unlock()

	if err != nil {
		job.Retries++
		job.Error = err.Error()

		if job.Retries < q.maxRetries {
			logger.Warnf("⚠️ Job %s failed (attempt %d/%d): %v", job.ID, job.Retries, q.maxRetries, err)
			job.Status = StatusPending

			// Re-queue with delay
			go func() {
				time.Sleep(q.retryDelay)
				select {
				case q.jobsChan <- job:
				case <-q.ctx.Done():
				}
			}()
		} else {
			logger.Errorf("❌ Job %s failed after %d attempts: %v", job.ID, q.maxRetries, err)
			job.Status = StatusFailed
			job.CompletedAt = time.Now()
		}
	} else {
		logger.Infof("✅ Job completed: %s", job.ID)
		job.Status = StatusCompleted
		job.CompletedAt = time.Now()
		job.Error = ""
	}
}

func (q *Queue) processImmediateJob(job *Job) {
	q.mu.Lock()
	job.Status = StatusProcessing
	job.StartedAt = time.Now()
	q.mu.Unlock()

	logger.Infof("⚡ Processing immediate job: %s (%s)", job.ID, job.FileName)

	err := q.processor.Process(q.ctx, job)

	q.mu.Lock()
	defer q.mu.Unlock()

	if err != nil {
		logger.Errorf("❌ Immediate job %s failed: %v", job.ID, err)
		job.Status = StatusFailed
		job.Error = err.Error()
		job.CompletedAt = time.Now()
		q.lightFailed++
		return
	}

	logger.Infof("✅ Immediate job completed: %s", job.ID)
	job.Status = StatusCompleted
	job.CompletedAt = time.Now()
	job.Error = ""
	q.lightCompleted++
}
