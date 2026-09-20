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

type stagedLaneProcessor struct {
	started map[string]map[Stage]chan struct{}
	release map[string]map[Stage]chan struct{}
}

func (p *stagedLaneProcessor) Process(context.Context, *Job) error {
	return errors.New("legacy processor path called")
}

func (p *stagedLaneProcessor) ProcessStage(ctx context.Context, job *Job) (Stage, error) {
	stage := job.AttemptStage
	if err := job.BeginStage(ctx, stage); err != nil {
		return "", err
	}
	if stages := p.started[job.ID]; stages != nil && stages[stage] != nil {
		close(stages[stage])
	}
	if stages := p.release[job.ID]; stages != nil && stages[stage] != nil {
		select {
		case <-stages[stage]:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	switch stage {
	case StageTranscribing:
		if err := job.SaveCheckpoint(ctx, StageTranscribed); err != nil {
			return "", err
		}
		return StageTranslating, nil
	case StageTranslating:
		if err := job.SaveCheckpoint(ctx, StageTranslated); err != nil {
			return "", err
		}
		return StageDelivering, nil
	case StageDelivering:
		if err := job.SaveCheckpoint(ctx, StageDelivered); err != nil {
			return "", err
		}
		return StageDelivered, nil
	default:
		return "", fmt.Errorf("unexpected stage %q", stage)
	}
}

func TestStagedProcessorUsesIndependentFIFOLanesAndOneAttempt(t *testing.T) {
	started := map[string]map[Stage]chan struct{}{}
	release := map[string]map[Stage]chan struct{}{}
	for _, id := range []string{"a", "b", "c"} {
		started[id] = map[Stage]chan struct{}{
			StageTranscribing: make(chan struct{}),
			StageTranslating:  make(chan struct{}),
		}
		release[id] = map[Stage]chan struct{}{}
	}
	release["a"][StageTranslating] = make(chan struct{})
	release["b"][StageTranscribing] = make(chan struct{})
	release["b"][StageTranslating] = make(chan struct{})
	release["c"][StageTranscribing] = make(chan struct{})
	release["c"][StageTranslating] = make(chan struct{})

	q := New(&stagedLaneProcessor{started: started, release: release}, 1, 0)
	for _, id := range []string{"a", "b", "c"} {
		job := NewJob(id, "/tmp/"+id+".mp4", id+".mp4", "", "")
		if err := q.store.insertJob(job); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		if _, err := q.store.db.Exec(`UPDATE attempts SET start_stage=?, current_stage='' WHERE job_id=?`, StageTranscribing, id); err != nil {
			t.Fatalf("queue %s for transcription: %v", id, err)
		}
		if _, err := q.store.db.Exec(`UPDATE jobs SET checkpoint=? WHERE id=?`, StageMoved, id); err != nil {
			t.Fatalf("checkpoint %s: %v", id, err)
		}
	}
	q.Start()
	defer q.Stop()

	waitClosed(t, started["a"][StageTranscribing], "job a transcription")
	waitClosed(t, started["a"][StageTranslating], "job a translation")
	waitClosed(t, started["b"][StageTranscribing], "job b transcription while a translates")
	assertOpen(t, started["c"][StageTranscribing], "job c transcription while b transcribes")
	assertOpen(t, started["b"][StageTranslating], "job b translation while a translates")

	close(release["b"][StageTranscribing])
	waitClosed(t, started["c"][StageTranscribing], "job c transcription after b")
	assertOpen(t, started["b"][StageTranslating], "job b translation while a still translates")

	close(release["a"][StageTranslating])
	waitClosed(t, started["b"][StageTranslating], "job b translation after a")
	close(release["c"][StageTranscribing])
	assertOpen(t, started["c"][StageTranslating], "job c translation while b translates")

	close(release["b"][StageTranslating])
	waitClosed(t, started["c"][StageTranslating], "job c translation after b")
	close(release["c"][StageTranslating])

	for _, id := range []string{"a", "b", "c"} {
		waitForStatus(t, q, id, StatusCompleted)
		detail := q.GetJobDetail(id)
		if detail == nil || len(detail.Attempts) != 1 || detail.Attempts[0].Status != AttemptCompleted {
			t.Fatalf("job %s detail = %#v, want one completed attempt", id, detail)
		}
	}
}

func waitClosed(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func assertOpen(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("unexpectedly started %s", description)
	case <-time.After(50 * time.Millisecond):
	}
}

type stageProcessorFunc func(context.Context, *Job) (Stage, error)

func (f stageProcessorFunc) Process(context.Context, *Job) error {
	return errors.New("legacy processor path called")
}

func (f stageProcessorFunc) ProcessStage(ctx context.Context, job *Job) (Stage, error) {
	return f(ctx, job)
}

func TestStagedProcessorDoesNotHoldTranslationLaneDuringRetryDelay(t *testing.T) {
	var aCalls int
	processor := stageProcessorFunc(func(ctx context.Context, job *Job) (Stage, error) {
		if err := job.BeginStage(ctx, job.AttemptStage); err != nil {
			return "", err
		}
		switch job.AttemptStage {
		case StageTranslating:
			if job.ID == "a" {
				aCalls++
				return "", errors.New("translation unavailable")
			}
			if err := job.SaveCheckpoint(ctx, StageTranslated); err != nil {
				return "", err
			}
			return StageDelivering, nil
		case StageDelivering:
			if err := job.SaveCheckpoint(ctx, StageDelivered); err != nil {
				return "", err
			}
			return StageDelivered, nil
		default:
			return "", fmt.Errorf("unexpected stage %q", job.AttemptStage)
		}
	})
	q := New(processor, 2, 60_000)
	for _, id := range []string{"a", "b"} {
		job := NewJob(id, "/tmp/"+id+".mp4", id+".mp4", "", "")
		if err := q.store.insertJob(job); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
		if _, err := q.store.db.Exec(`UPDATE attempts SET start_stage=?, current_stage='' WHERE job_id=?`, StageTranslating, id); err != nil {
			t.Fatalf("queue %s for translation: %v", id, err)
		}
		if _, err := q.store.db.Exec(`UPDATE jobs SET checkpoint=? WHERE id=?`, StageTranscribed, id); err != nil {
			t.Fatalf("checkpoint %s: %v", id, err)
		}
	}
	q.Start()
	defer q.Stop()

	waitForStatus(t, q, "b", StatusCompleted)
	if aCalls != 1 {
		t.Fatalf("job a translation calls = %d, want delayed retry to remain pending", aCalls)
	}
	detail := q.GetJobDetail("a")
	if detail == nil || detail.Status != StatusPending || len(detail.Attempts) != 2 || detail.Attempts[1].Status != AttemptPending {
		t.Fatalf("job a after first failure = %#v, want delayed pending retry", detail)
	}
}

func TestStagedProcessorDoesNotDelayManualRetryWithPositiveRetryIndex(t *testing.T) {
	started := make(chan struct{})
	processor := stageProcessorFunc(func(ctx context.Context, job *Job) (Stage, error) {
		if err := job.BeginStage(ctx, job.AttemptStage); err != nil {
			return "", err
		}
		close(started)
		return StageDelivered, nil
	})
	q := New(processor, 1, 60_000)
	job := NewJob("manual", "/tmp/manual.mp4", "manual.mp4", "", "")
	if err := q.store.insertJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`UPDATE attempts SET kind=?, retry_index=?, start_stage=?, current_stage='' WHERE job_id=?`,
		AttemptManual, 2, StageTranslating, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`UPDATE jobs SET checkpoint=? WHERE id=?`, StageTranscribed, job.ID); err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Stop()

	waitClosed(t, started, "manual retry without automatic delay")
}

func TestStagedProcessorDelaysAutomaticRetranslationRetry(t *testing.T) {
	started := make(chan struct{})
	processor := stageProcessorFunc(func(ctx context.Context, job *Job) (Stage, error) {
		close(started)
		return StageDelivered, nil
	})
	q := New(processor, 1, 60_000)
	job := NewJob("retranslate", "/tmp/retranslate.mp4", "retranslate.mp4", "", "")
	if err := q.store.insertJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`UPDATE attempts SET kind=?, retry_index=?, start_stage=?, current_stage='' WHERE job_id=?`,
		AttemptRetranslate, 1, StageTranslating, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`UPDATE jobs SET checkpoint=? WHERE id=?`, StageTranscribed, job.ID); err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Stop()

	select {
	case <-started:
		t.Fatal("automatic retranslation retry started before its delay elapsed")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestStagedProcessorClaimsPreparedAdmissionAtMoving(t *testing.T) {
	firstStage := make(chan Stage, 1)
	processor := stageProcessorFunc(func(ctx context.Context, job *Job) (Stage, error) {
		select {
		case firstStage <- job.AttemptStage:
		default:
		}
		if err := job.BeginStage(ctx, job.AttemptStage); err != nil {
			return "", err
		}
		switch job.AttemptStage {
		case StageMoving:
			if err := job.SaveCheckpoint(ctx, StageMoved); err != nil {
				return "", err
			}
			return StageDelivering, nil
		case StageDelivering:
			if err := job.SaveCheckpoint(ctx, StageDelivered); err != nil {
				return "", err
			}
			return StageDelivered, nil
		default:
			return "", fmt.Errorf("unexpected stage %q", job.AttemptStage)
		}
	})
	q := New(processor, 1, 0)
	q.Start()
	defer q.Stop()

	root := t.TempDir()
	source := filepath.Join(root, "source.mp4")
	staging := filepath.Join(root, "staging", "source.mp4")
	if err := os.WriteFile(source, []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	job := NewJob("job", source, "source.mp4", "", "")
	job.StagingPath = staging
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, q, job.ID, StatusCompleted)
	select {
	case stage := <-firstStage:
		if stage != StageMoving {
			t.Fatalf("first stage = %q, want %q", stage, StageMoving)
		}
	default:
		t.Fatal("processor did not report a first stage")
	}
}

type terminalFailureRecorder struct {
	mu               sync.Mutex
	translationCalls int
	handled          chan Stage
}

func (p *terminalFailureRecorder) Process(context.Context, *Job) error {
	return errors.New("legacy processor path called")
}

func (p *terminalFailureRecorder) ProcessStage(ctx context.Context, job *Job) (Stage, error) {
	if err := job.BeginStage(ctx, job.AttemptStage); err != nil {
		return "", err
	}
	if job.AttemptStage == StageTranslating {
		p.mu.Lock()
		p.translationCalls++
		p.mu.Unlock()
	}
	return "", fmt.Errorf("%s failed", job.AttemptStage)
}

func (p *terminalFailureRecorder) HandleTerminalFailure(_ context.Context, job *Job, stage Stage, _ error) error {
	job.ProcessingPath = "/failed/" + job.FileName
	p.handled <- stage
	return nil
}

func TestTranscriptionTerminalFailureRunsFailureHandler(t *testing.T) {
	processor := &terminalFailureRecorder{handled: make(chan Stage, 1)}
	q := New(processor, 3, 0)
	job := NewJob("job", "/tmp/job.mp4", "job.mp4", "", "")
	job.ProcessingPath = "/processing/job.mp4"
	if err := q.store.insertJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`UPDATE attempts SET start_stage=?, current_stage='' WHERE job_id=?`, StageTranscribing, job.ID); err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Stop()

	waitForStatus(t, q, job.ID, StatusFailed)
	select {
	case stage := <-processor.handled:
		if stage != StageTranscribing {
			t.Fatalf("handled stage = %q, want %q", stage, StageTranscribing)
		}
	default:
		t.Fatal("terminal transcription failure was not handled")
	}
	if got := q.GetJob(job.ID).ProcessingPath; got != "/failed/job.mp4" {
		t.Fatalf("processing path = %q, want terminal handler result", got)
	}
}

func TestTranslationFailureHandlerRunsOnlyAfterRetriesExhausted(t *testing.T) {
	processor := &terminalFailureRecorder{handled: make(chan Stage, 1)}
	q := New(processor, 2, 200)
	job := NewJob("job", "/tmp/job.mp4", "job.mp4", "", "")
	job.ProcessingPath = "/processing/job.mp4"
	if err := q.store.insertJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`UPDATE attempts SET start_stage=?, current_stage='' WHERE job_id=?`, StageTranslating, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := q.store.db.Exec(`UPDATE jobs SET checkpoint=? WHERE id=?`, StageTranscribed, job.ID); err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Stop()

	deadline := time.Now().Add(time.Second)
	for {
		detail := q.GetJobDetail(job.ID)
		if detail != nil && len(detail.Attempts) == 2 && detail.Attempts[1].Status == AttemptPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("automatic retry not queued: %#v", detail)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case stage := <-processor.handled:
		t.Fatalf("intermediate failure unexpectedly handled at %q", stage)
	default:
	}
	if got := q.GetJob(job.ID).ProcessingPath; got != "/processing/job.mp4" {
		t.Fatalf("intermediate processing path = %q, want unchanged", got)
	}

	waitForStatus(t, q, job.ID, StatusFailed)
	select {
	case stage := <-processor.handled:
		if stage != StageTranslating {
			t.Fatalf("handled stage = %q, want %q", stage, StageTranslating)
		}
	default:
		t.Fatal("terminal translation failure was not handled")
	}
	processor.mu.Lock()
	calls := processor.translationCalls
	processor.mu.Unlock()
	if calls != 2 {
		t.Fatalf("translation calls = %d, want 2", calls)
	}
	if got := q.GetJob(job.ID).ProcessingPath; got != "/failed/job.mp4" {
		t.Fatalf("terminal processing path = %q, want failure handler result", got)
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
