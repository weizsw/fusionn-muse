package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fusionn-muse/pkg/logger"
)

func init() {
	logger.Init(true)
}

type countingProcessor struct {
	mu       sync.Mutex
	calls    int
	err      error
	attempts []int
	jobIDs   []string
	failures int
}

func newCountingProcessor(err error) *countingProcessor {
	return &countingProcessor{err: err}
}

func (p *countingProcessor) Process(ctx context.Context, job *Job) error {
	p.mu.Lock()
	p.calls++
	p.attempts = append(p.attempts, logger.Attempt(ctx))
	p.jobIDs = append(p.jobIDs, logger.JobID(ctx))
	p.mu.Unlock()
	if p.err != nil && !job.IsLight {
		job.BeginStage(ctx, StageTranslating)
	}
	return p.err
}

func (p *countingProcessor) NotifyFailure(_ context.Context, _ *Job, _ Stage, _ error) {
	p.mu.Lock()
	p.failures++
	p.mu.Unlock()
}

func (p *countingProcessor) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *countingProcessor) failureCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failures
}

func TestAcceptStagesBeforeWorkerCanClaim(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.mp4")
	staging := filepath.Join(root, "staging", "source.mp4")
	if err := os.WriteFile(source, []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	checked := make(chan error, 1)
	q := New(processorFunc(func(_ context.Context, job *Job) error {
		if job.StartStage != StageMoving || job.Checkpoint != StagePrepared {
			checked <- fmt.Errorf("worker saw start=%q checkpoint=%q", job.StartStage, job.Checkpoint)
			return nil
		}
		content, err := os.ReadFile(staging)
		if err == nil && string(content) != "video" {
			err = fmt.Errorf("staged content = %q", content)
		}
		checked <- err
		return nil
	}), 1, 0)
	q.Start()
	defer q.Stop()
	job := NewJob("job", source, "source.mp4", "", "")
	job.StagingPath = staging
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	if err := <-checked; err != nil {
		t.Fatal(err)
	}
}

func TestAcceptStagingFailureIsDurablyFailed(t *testing.T) {
	q := New(newCountingProcessor(nil), 1, 0)
	q.Start()
	defer q.Stop()
	job := NewJob("job", "/missing/source.mp4", "source.mp4", "", "")
	job.StagingPath = filepath.Join(t.TempDir(), "staging", "source.mp4")
	if err := q.Accept(job); err == nil {
		t.Fatal("Accept error = nil, want staging failure")
	}
	got := q.GetJob(job.ID)
	if got == nil || got.Status != StatusFailed || got.FailureStage != StagePreparing {
		t.Fatalf("failed admission = %#v", got)
	}
}

func TestAutomaticRetriesKeepJobIDAndIncrementAttempt(t *testing.T) {
	proc := newCountingProcessor(errors.New("boom"))
	q := New(proc, 3, 1)
	q.Start()

	job := NewJob("job-a", "/tmp/source.mp4", "source.mp4", "", "")
	if err := q.Accept(job); err != nil {
		t.Fatalf("accept job: %v", err)
	}
	waitForCalls(t, proc, 3)
	q.Stop()
	if got := q.GetJob(job.ID).Status; got != StatusFailed {
		t.Fatalf("job status = %q, want %q", got, StatusFailed)
	}
	if got := proc.failureCount(); got != 1 {
		t.Fatalf("failure notifications = %d, want 1 after retries are exhausted", got)
	}

	proc.mu.Lock()
	defer proc.mu.Unlock()
	if got, want := proc.attempts, []int{1, 2, 3}; !equalInts(got, want) {
		t.Fatalf("attempts = %v, want %v", got, want)
	}
	for _, jobID := range proc.jobIDs {
		if jobID != "job-a" {
			t.Fatalf("job ID = %q, want job-a", jobID)
		}
	}
}

func TestAcceptedLightJobRunsExactlyOneAttempt(t *testing.T) {
	proc := newCountingProcessor(errors.New("boom"))
	q := New(proc, 3, 1)
	q.Start()
	defer q.Stop()

	job := NewJob("job1", "/tmp/source.mp4", "source.mp4", "", "")
	job.IsLight = true
	if err := q.Accept(job); err != nil {
		t.Fatalf("accept light job: %v", err)
	}

	waitForLightFailures(t, q, 1)

	if proc.callCount() != 1 {
		t.Fatalf("processor calls = %d, want 1", proc.callCount())
	}
	if got := q.GetJob(job.ID).Status; got != StatusFailed {
		t.Fatalf("job.Status = %q, want %q", got, StatusFailed)
	}
	if q.GetQueueStats()["light_failed"] != 1 {
		t.Fatalf("light_failed = %d, want 1", q.GetQueueStats()["light_failed"])
	}
}

func TestAcceptRejectsInvalidJobWithoutRegistration(t *testing.T) {
	q := New(newCountingProcessor(nil), 1, 0)

	if err := q.Accept(nil); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("Accept(nil) error = %v, want %v", err, ErrInvalidJob)
	}
	if got := q.GetQueueStats()["total"]; got != 0 {
		t.Fatalf("total jobs = %d, want 0", got)
	}
}

func TestAcceptRejectsNonRunningAndStoppedQueue(t *testing.T) {
	q := New(newCountingProcessor(nil), 1, 0)
	job := NewJob("job-a", "/tmp/source.mp4", "source.mp4", "", "")

	if err := q.Accept(job); !errors.Is(err, ErrQueueNotRunning) {
		t.Fatalf("non-running Accept error = %v, want %v", err, ErrQueueNotRunning)
	}
	q.Start()
	q.Stop()
	if err := q.Accept(job); !errors.Is(err, ErrQueueStopped) {
		t.Fatalf("stopped Accept error = %v, want %v", err, ErrQueueStopped)
	}
	if got := q.GetQueueStats()["total"]; got != 0 {
		t.Fatalf("total jobs = %d, want 0", got)
	}
}

func TestAcceptRejectsDuplicateJobID(t *testing.T) {
	q := New(newCountingProcessor(nil), 1, 0)
	q.Start()
	defer q.Stop()

	first := NewJob("job-a", "/tmp/source.mp4", "source.mp4", "", "")
	if err := q.Accept(first); err != nil {
		t.Fatalf("accept first job: %v", err)
	}
	duplicate := NewJob("job-a", "/tmp/other.mp4", "other.mp4", "", "")
	if err := q.Accept(duplicate); !errors.Is(err, ErrDuplicateJobID) {
		t.Fatalf("duplicate Accept error = %v, want %v", err, ErrDuplicateJobID)
	}
	if got := q.GetQueueStats()["total"]; got != 1 {
		t.Fatalf("total jobs = %d, want 1", got)
	}
	if got := q.GetJob("job-a"); got.SourcePath != first.SourcePath {
		t.Fatalf("registered source = %q, want %q", got.SourcePath, first.SourcePath)
	}
}

func TestAcceptAndReadsUseJobSnapshots(t *testing.T) {
	q := New(newCountingProcessor(nil), 1, 0)
	q.Start()
	defer q.Stop()

	job := NewJob("job-a", "/tmp/source.mp4", "source.mp4", "", "")
	if err := q.Accept(job); err != nil {
		t.Fatalf("accept job: %v", err)
	}
	waitForStatus(t, q, job.ID, StatusCompleted)

	job.FileName = "caller-mutated.mp4"
	firstRead := q.GetJob(job.ID)
	if firstRead.FileName != "source.mp4" {
		t.Fatalf("accepted filename = %q, want source.mp4", firstRead.FileName)
	}
	firstRead.FileName = "reader-mutated.mp4"
	if got := q.GetJob(job.ID).FileName; got != "source.mp4" {
		t.Fatalf("stored filename after reader mutation = %q, want source.mp4", got)
	}
}

type lifecycleMutatingProcessor struct {
	mutated chan struct{}
	release chan struct{}
}

func (p *lifecycleMutatingProcessor) Process(_ context.Context, job *Job) error {
	job.StagingPath = "/tmp/staged.mp4"
	job.Status = StatusFailed
	job.Error = "processor-owned error"
	job.Retries = 99
	job.StartedAt = time.Unix(1, 0)
	job.CompletedAt = time.Unix(2, 0)
	close(p.mutated)
	<-p.release
	return nil
}

func TestAttemptOwnsLifecycleWhilePublishingProcessorArtifacts(t *testing.T) {
	proc := &lifecycleMutatingProcessor{mutated: make(chan struct{}), release: make(chan struct{})}
	q := New(proc, 1, 0)
	q.Start()
	defer q.Stop()

	job := NewJob("job-a", "/tmp/source.mp4", "source.mp4", "", "")
	createdAt := job.CreatedAt
	if err := q.Accept(job); err != nil {
		t.Fatalf("accept job: %v", err)
	}
	<-proc.mutated

	processing := q.GetJob(job.ID)
	if processing.Status != StatusProcessing || processing.StagingPath != "" || processing.StartedAt.IsZero() {
		t.Fatalf("processing snapshot = %+v", processing)
	}
	close(proc.release)
	waitForStatus(t, q, job.ID, StatusCompleted)

	completed := q.GetJob(job.ID)
	if completed.Error != "" || completed.Retries != 0 || completed.CompletedAt.IsZero() {
		t.Fatalf("completed lifecycle = %+v", completed)
	}
	if !completed.CreatedAt.Equal(createdAt) || completed.StartedAt.Equal(time.Unix(1, 0)) {
		t.Fatalf("processor overwrote queue timestamps: %+v", completed)
	}
	if completed.StagingPath != "/tmp/staged.mp4" {
		t.Fatalf("StagingPath = %q, want processor artifact", completed.StagingPath)
	}
}

type blockingProcessor struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingProcessor) Process(ctx context.Context, _ *Job) error {
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestAcceptedLightJobsRunConcurrently(t *testing.T) {
	proc := &blockingProcessor{started: make(chan struct{}, 2), release: make(chan struct{})}
	q := New(proc, 1, 0)
	q.Start()
	defer q.Stop()
	defer close(proc.release)

	for i := 0; i < 2; i++ {
		job := NewJob(fmt.Sprintf("light-%d", i), fmt.Sprintf("/tmp/light-%d.mp4", i), fmt.Sprintf("light-%d.mp4", i), "", "")
		job.IsLight = true
		if err := q.Accept(job); err != nil {
			t.Fatalf("accept light job %d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-proc.started:
		case <-time.After(time.Second):
			t.Fatal("light jobs did not run concurrently")
		}
	}
}

func TestAcceptedHeavyJobsRunSequentially(t *testing.T) {
	proc := &blockingProcessor{started: make(chan struct{}, 2), release: make(chan struct{})}
	q := New(proc, 1, 0)
	q.Start()
	defer q.Stop()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(proc.release) }) }
	defer release()

	for i := 0; i < 2; i++ {
		job := NewJob(fmt.Sprintf("heavy-%d", i), fmt.Sprintf("/tmp/heavy-%d.mp4", i), fmt.Sprintf("heavy-%d.mp4", i), "", "")
		if err := q.Accept(job); err != nil {
			t.Fatalf("accept heavy job %d: %v", i, err)
		}
	}
	<-proc.started
	select {
	case <-proc.started:
		t.Fatal("second heavy job started before first completed")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case <-proc.started:
	case <-time.After(time.Second):
		t.Fatal("second heavy job did not start after first completed")
	}
}

type stoppingProcessor struct {
	started   chan struct{}
	canceled  chan struct{}
	release   chan struct{}
	returnErr error
}

func (p *stoppingProcessor) Process(ctx context.Context, _ *Job) error {
	p.started <- struct{}{}
	<-ctx.Done()
	p.canceled <- struct{}{}
	<-p.release
	if p.returnErr != nil {
		return p.returnErr
	}
	return ctx.Err()
}

func TestAcceptDistinguishesStoppingAndStoppedQueue(t *testing.T) {
	proc := &stoppingProcessor{started: make(chan struct{}, 1), canceled: make(chan struct{}, 1), release: make(chan struct{})}
	q := New(proc, 1, 0)
	q.Start()
	job := NewJob("light", "/tmp/light.mp4", "light.mp4", "", "")
	job.IsLight = true
	if err := q.Accept(job); err != nil {
		t.Fatalf("accept light job: %v", err)
	}
	<-proc.started

	stopped := make(chan struct{})
	go func() {
		q.Stop()
		close(stopped)
	}()
	<-proc.canceled
	if err := q.Accept(NewJob("late", "/tmp/late.mp4", "late.mp4", "", "")); !errors.Is(err, ErrQueueStopping) {
		t.Fatalf("stopping Accept error = %v, want %v", err, ErrQueueStopping)
	}
	alsoStopped := make(chan struct{})
	go func() {
		q.Stop()
		close(alsoStopped)
	}()
	close(proc.release)
	<-alsoStopped
	<-stopped
	if err := q.Accept(NewJob("later", "/tmp/later.mp4", "later.mp4", "", "")); !errors.Is(err, ErrQueueStopped) {
		t.Fatalf("stopped Accept error = %v, want %v", err, ErrQueueStopped)
	}
}

func TestStopClassifiesCleanupErrorAsInterrupted(t *testing.T) {
	proc := &stoppingProcessor{
		started: make(chan struct{}, 1), canceled: make(chan struct{}, 1), release: make(chan struct{}),
		returnErr: errors.New("transaction has already been committed or rolled back"),
	}
	q := New(proc, 1, 0)
	q.Start()
	job := NewJob("job", "/tmp/source.mp4", "source.mp4", "", "")
	job.IsLight = true
	if err := q.Accept(job); err != nil {
		t.Fatalf("accept job: %v", err)
	}
	<-proc.started

	stopped := make(chan struct{})
	go func() {
		q.Stop()
		close(stopped)
	}()
	<-proc.canceled
	close(proc.release)
	<-stopped

	got := q.GetJobDetail(job.ID)
	if got == nil || got.Status != StatusInterrupted || len(got.Attempts) != 1 || got.Attempts[0].Status != AttemptInterrupted {
		t.Fatalf("shutdown result = %#v, want interrupted Job and Attempt", got)
	}
}

func TestStopLeavesDelayedAutomaticRetryPending(t *testing.T) {
	proc := newCountingProcessor(errors.New("boom"))
	q := New(proc, 3, 60_000)
	q.Start()

	job := NewJob("job-a", "/tmp/source.mp4", "source.mp4", "", "")
	if err := q.Accept(job); err != nil {
		t.Fatalf("accept job: %v", err)
	}
	waitForCalls(t, proc, 1)

	started := time.Now()
	q.Stop()
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Stop took %v during retry delay", elapsed)
	}
	got := q.GetJob(job.ID)
	if got.Status != StatusPending || got.Error != "" {
		t.Fatalf("stopped job = {status:%q error:%q}, want pending retry", got.Status, got.Error)
	}
	detail := q.GetJobDetail(job.ID)
	if detail == nil || len(detail.Attempts) != 2 || detail.Attempts[0].Status != AttemptFailed || detail.Attempts[1].Status != AttemptPending {
		t.Fatalf("attempts after shutdown = %#v, want failed then pending", detail)
	}
}

func TestSaveCheckpointUpdatesMemoryOnlyAfterPersistence(t *testing.T) {
	job := NewJob("job", "/tmp/source.mp4", "source.mp4", "", "")
	job.Checkpoint = StageMoved
	job.progress = func(context.Context, *Job, Stage, bool) error { return errors.New("db unavailable") }
	if err := job.SaveCheckpoint(context.Background(), StageTranscribed); err == nil {
		t.Fatal("SaveCheckpoint error = nil, want persistence error")
	}
	if job.Checkpoint != StageMoved {
		t.Fatalf("checkpoint = %q, want %q", job.Checkpoint, StageMoved)
	}
}

func TestAcceptDurablyQueuesBeyondWakeChannelCapacity(t *testing.T) {
	proc := &blockingProcessor{started: make(chan struct{}, 1), release: make(chan struct{})}
	q := New(proc, 1, 0)
	q.Start()
	defer func() {
		close(proc.release)
		q.Stop()
	}()

	if err := q.Accept(NewJob("processing", "/tmp/processing.mp4", "processing.mp4", "", "")); err != nil {
		t.Fatalf("accept processing job: %v", err)
	}
	<-proc.started

	queued := cap(q.jobsChan) + 1
	for i := 0; i < queued; i++ {
		job := NewJob(fmt.Sprintf("queued-%d", i), fmt.Sprintf("/tmp/queued-%d.mp4", i), fmt.Sprintf("queued-%d.mp4", i), "", "")
		if err := q.Accept(job); err != nil {
			t.Fatalf("accept queued job %d: %v", i, err)
		}
		if got := q.GetJob(job.ID); got == nil || got.Status != StatusPending {
			t.Fatalf("queued job %d = %+v", i, got)
		}
	}
	if got, want := q.GetQueueStats()["total"], queued+1; got != want {
		t.Fatalf("total jobs = %d, want %d", got, want)
	}
}

func TestListJobsPaginatesNewestFirst(t *testing.T) {
	q := New(newCountingProcessor(nil), 1, 0)
	for i, id := range []string{"job-1", "job-2", "job-3"} {
		job := NewJob(id, "/tmp/"+id+".mp4", id+".mp4", "", "")
		job.CreatedAt = time.Unix(0, int64(i+1))
		if err := q.store.insertJob(job); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	page, cursor, err := q.ListJobs("", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].ID != "job-3" || page[1].ID != "job-2" || cursor == "" {
		t.Fatalf("first page = %#v, cursor = %q", page, cursor)
	}
	page, next, err := q.ListJobs(cursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != "job-1" || next != "" {
		t.Fatalf("second page = %#v, cursor = %q", page, next)
	}
	if _, _, err := q.ListJobs("bad", 2); err == nil {
		t.Fatal("expected invalid cursor error")
	}
}

func waitForStatus(t *testing.T, q *Queue, jobID string, want JobStatus) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if job := q.GetJob(jobID); job != nil && job.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	job := q.GetJob(jobID)
	if job == nil {
		t.Fatalf("job %q not found", jobID)
	}
	t.Fatalf("job status = %q, want %q", job.Status, want)
}

func waitForCalls(t *testing.T, processor *countingProcessor, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if processor.callCount() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("processor calls = %d, want %d", processor.callCount(), want)
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func waitForLightFailures(t *testing.T, q *Queue, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.GetQueueStats()["light_failed"] == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("light_failed = %d, want %d", q.GetQueueStats()["light_failed"], want)
}
