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

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a new job queue.
func New(processor Processor, maxRetries int, retryDelayMs int) *Queue {
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
	q.mu.Lock()
	q.jobs = append(q.jobs, job)
	q.jobMap[job.ID] = job
	q.mu.Unlock()

	logger.Infof("📥 Job queued: %s (%s)", job.ID, job.FileName)

	// Non-blocking send to channel
	select {
	case q.jobsChan <- job:
	default:
		logger.Warnf("⚠️ Job channel full, job %s will be processed later", job.ID)
	}
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

	stats := map[string]int{
		"total":      len(q.jobs),
		"pending":    0,
		"processing": 0,
		"completed":  0,
		"failed":     0,
	}

	for _, job := range q.jobs {
		switch job.Status {
		case StatusPending:
			stats["pending"]++
		case StatusProcessing:
			stats["processing"]++
		case StatusCompleted:
			stats["completed"]++
		case StatusFailed:
			stats["failed"]++
		}
	}

	return stats
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
