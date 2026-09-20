package manualrequeue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/pkg/logger"
)

func TestMain(m *testing.M) {
	logger.Init(true)
	os.Exit(m.Run())
}

type recordingAccepter struct {
	jobs       []*queue.Job
	reject     map[string]error
	beforeErr  func(*queue.Job)
	retries    []string
	retryPaths []string
	retryErr   error
	foundJob   *queue.Job
	foundJobs  map[string]*queue.Job
	findPaths  []string
	findNames  []string
}

func (a *recordingAccepter) Accept(job *queue.Job) error {
	copy := *job
	a.jobs = append(a.jobs, &copy)
	if err := a.reject[job.FileName]; err != nil {
		if a.beforeErr != nil {
			a.beforeErr(job)
		}
		return err
	}
	return nil
}

func (a *recordingAccepter) Retry(jobID string) error {
	a.retries = append(a.retries, jobID)
	return a.retryErr
}

func (a *recordingAccepter) RetryFrom(jobID, path string) error {
	a.retries = append(a.retries, jobID)
	a.retryPaths = append(a.retryPaths, path)
	return a.retryErr
}

func (a *recordingAccepter) FindJobByMedia(path, fileName string) *queue.Job {
	a.findPaths = append(a.findPaths, path)
	a.findNames = append(a.findNames, fileName)
	if a.foundJobs != nil {
		return a.foundJobs[fileName]
	}
	return a.foundJob
}

func TestFailedRequeueMovesMediaToProcessingAndRetriesExistingJob(t *testing.T) {
	root := t.TempDir()
	folders := testFolders(root)
	failedPath := filepath.Join(folders.Failed, "movie.mp4")
	processingPath := filepath.Join(folders.Process, "movie.mp4")
	mustWriteFile(t, failedPath, "media")
	accepter := &recordingAccepter{foundJob: &queue.Job{ID: "existing", FileName: "movie.mp4", Status: queue.StatusFailed}}
	service := New(accepter, folders, probeRunner{})
	name := "movie.mp4"

	result := service.Requeue(context.Background(), Request{Location: Failed, FileName: &name})
	if len(result.Accepted) != 1 || result.Accepted[0].JobID != "existing" || result.Accepted[0].FileName != name || result.Accepted[0].Staged {
		t.Fatalf("result = %+v, want existing Job accepted from failed", result)
	}
	if len(accepter.findPaths) != 1 || accepter.findPaths[0] != failedPath || accepter.findNames[0] != name {
		t.Fatalf("FindJobByMedia calls = %v %v, want failed media", accepter.findPaths, accepter.findNames)
	}
	if len(accepter.retries) != 1 || accepter.retries[0] != "existing" || len(accepter.retryPaths) != 1 || accepter.retryPaths[0] != processingPath {
		t.Fatalf("retries = %v paths = %v, want existing Job from processing path", accepter.retries, accepter.retryPaths)
	}
	if _, err := os.Stat(failedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed source still exists: %v", err)
	}
	if got, err := os.ReadFile(processingPath); err != nil || string(got) != "media" {
		t.Fatalf("processing media = %q, %v", got, err)
	}
}

func TestFailedRequeueResumesTranscriptionFromDurableCheckpoint(t *testing.T) {
	folders := testFolders(t.TempDir())
	failedPath := filepath.Join(folders.Failed, "movie.mp4")
	mustWriteFile(t, failedPath, "media")
	starts := make(chan queue.Stage, 1)
	calls := 0
	q := queue.New(processorFunc(func(ctx context.Context, job *queue.Job) error {
		calls++
		if calls == 1 {
			if err := job.SaveCheckpoint(ctx, queue.StageMoved); err != nil {
				return err
			}
			if err := job.BeginStage(ctx, queue.StageTranscribing); err != nil {
				return err
			}
			return errors.New("transcription failed")
		}
		starts <- job.StartStage
		return nil
	}), 1, 0)
	q.Start()
	defer q.Stop()
	job := queue.NewJob("existing", failedPath, "movie.mp4", "", "")
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitForJobStatus(t, q, job.ID, queue.StatusFailed)

	name := "movie.mp4"
	result := New(q, folders, probeRunner{}).Requeue(context.Background(), Request{Location: Failed, FileName: &name})
	if len(result.Accepted) != 1 || result.Accepted[0].JobID != job.ID {
		t.Fatalf("result = %+v, want failed transcription accepted", result)
	}
	select {
	case start := <-starts:
		if start != queue.StageTranscribing {
			t.Fatalf("retry start = %q, want %q", start, queue.StageTranscribing)
		}
	case <-time.After(time.Second):
		t.Fatal("manual retry did not resume")
	}
}

func TestFailedRequeueRestoresMediaWhenRetryCreationFails(t *testing.T) {
	root := t.TempDir()
	folders := testFolders(root)
	failedPath := filepath.Join(folders.Failed, "movie.mp4")
	processingPath := filepath.Join(folders.Process, "movie.mp4")
	mustWriteFile(t, failedPath, "media")
	retryErr := errors.New("create attempt")
	accepter := &recordingAccepter{
		foundJob: &queue.Job{ID: "existing", FileName: "movie.mp4", Status: queue.StatusFailed},
		retryErr: retryErr,
	}
	name := "movie.mp4"

	result := New(accepter, folders, probeRunner{}).Requeue(context.Background(), Request{Location: Failed, FileName: &name})
	if len(result.Failed) != 1 || !errors.Is(result.Failed[0].Err, retryErr) || len(result.Accepted) != 0 {
		t.Fatalf("result = %+v, want retry creation failure", result)
	}
	if got, err := os.ReadFile(failedPath); err != nil || string(got) != "media" {
		t.Fatalf("restored failed media = %q, %v", got, err)
	}
	if _, err := os.Stat(processingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("processing media remains after rollback: %v", err)
	}
}

type probeRunner struct{}

type processorFunc func(context.Context, *queue.Job) error

func (f processorFunc) Process(ctx context.Context, job *queue.Job) error {
	return f(ctx, job)
}

func (probeRunner) Run(context.Context, string, ...string) error { return nil }
func (probeRunner) Output(context.Context, string, ...string) ([]byte, error) {
	return []byte(`{"streams":[]}`), nil
}
func (probeRunner) Stream(context.Context, string, ...string) (string, string, error) {
	return "", "", nil
}

func TestSingleRequeueRejectsUnsafeNamesBeforeFilesystemAccess(t *testing.T) {
	root := t.TempDir()
	folders := testFolders(root)
	outside := filepath.Join(root, "outside.mp4")
	mustWriteFile(t, outside, "outside")
	accepter := &recordingAccepter{}
	service := New(accepter, folders, probeRunner{})

	for _, name := range []string{"", "/tmp/movie.mp4", "folder/movie.mp4", `folder\movie.mp4`, ".", "..", "../outside.mp4", "./movie.mp4", ".hidden.mp4"} {
		t.Run(name, func(t *testing.T) {
			result := service.Requeue(context.Background(), Request{Location: Failed, FileName: &name})
			if len(result.Accepted) != 0 || len(result.Failed) != 1 || !errors.Is(result.Failed[0].Err, ErrInvalidName) {
				t.Fatalf("result = %+v, want one invalid-name failure", result)
			}
		})
	}

	if len(accepter.jobs) != 0 {
		t.Fatalf("queue received %d jobs, want none", len(accepter.jobs))
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "outside" {
		t.Fatalf("outside file changed before validation: %q, %v", got, err)
	}
}

func TestFailedRequeueLeavesOrphanUntouched(t *testing.T) {
	root := t.TempDir()
	folders := testFolders(root)
	failedPath := filepath.Join(folders.Failed, "movie.mp4")
	mustWriteFile(t, failedPath, "media")
	accepter := &recordingAccepter{}
	service := New(accepter, folders, probeRunner{})
	name := "movie.mp4"

	result := service.Requeue(context.Background(), Request{Location: Failed, FileName: &name})
	if len(result.Skipped) != 1 || result.Skipped[0].FileName != name || result.Skipped[0].Staged {
		t.Fatalf("result = %+v, want one untouched skipped orphan", result)
	}
	if got, err := os.ReadFile(failedPath); err != nil || string(got) != "media" {
		t.Fatalf("failed orphan changed: %q, %v", got, err)
	}
	if len(accepter.jobs) != 0 {
		t.Fatalf("queue received %d jobs, want none", len(accepter.jobs))
	}
}

func TestFailedRequeuePreservesUnrelatedProcessingDestination(t *testing.T) {
	root := t.TempDir()
	folders := testFolders(root)
	failedPath := filepath.Join(folders.Failed, "movie.mp4")
	processingPath := filepath.Join(folders.Process, "movie.mp4")
	mustWriteFile(t, failedPath, "failed")
	mustWriteFile(t, processingPath, "unrelated")
	accepter := &recordingAccepter{foundJob: &queue.Job{ID: "existing", FileName: "movie.mp4", Status: queue.StatusFailed}}
	name := "movie.mp4"

	result := New(accepter, folders, probeRunner{}).Requeue(context.Background(), Request{Location: Failed, FileName: &name})
	if len(result.Failed) != 1 || !errors.Is(result.Failed[0].Err, ErrConflict) || len(result.Accepted) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("result = %+v, want processing conflict failure", result)
	}
	if got, _ := os.ReadFile(failedPath); string(got) != "failed" {
		t.Fatalf("failed source changed: %q", got)
	}
	if got, _ := os.ReadFile(processingPath); string(got) != "unrelated" {
		t.Fatalf("processing destination changed: %q", got)
	}
	if len(accepter.retries) != 0 {
		t.Fatalf("retries = %v, want none", accepter.retries)
	}
}

func TestBulkFailedRequeueContinuesAfterProcessingConflict(t *testing.T) {
	folders := testFolders(t.TempDir())
	mustWriteFile(t, filepath.Join(folders.Failed, "a-conflict.mp4"), "failed-a")
	mustWriteFile(t, filepath.Join(folders.Failed, "b-retry.mp4"), "failed-b")
	mustWriteFile(t, filepath.Join(folders.Process, "a-conflict.mp4"), "unrelated")
	accepter := &recordingAccepter{foundJobs: map[string]*queue.Job{
		"a-conflict.mp4": {ID: "job-a", FileName: "a-conflict.mp4", Status: queue.StatusFailed},
		"b-retry.mp4":    {ID: "job-b", FileName: "b-retry.mp4", Status: queue.StatusFailed},
	}}

	result := New(accepter, folders, probeRunner{}).Requeue(context.Background(), Request{Location: Failed})
	if len(result.Failed) != 1 || result.Failed[0].FileName != "a-conflict.mp4" || !errors.Is(result.Failed[0].Err, ErrConflict) {
		t.Fatalf("failed = %+v, want only processing conflict", result.Failed)
	}
	if len(result.Accepted) != 1 || result.Accepted[0].FileName != "b-retry.mp4" || result.Accepted[0].JobID != "job-b" {
		t.Fatalf("accepted = %+v, want second failed media retried", result.Accepted)
	}
	if len(accepter.retries) != 1 || accepter.retries[0] != "job-b" {
		t.Fatalf("retries = %v, want job-b", accepter.retries)
	}
	if got, _ := os.ReadFile(filepath.Join(folders.Process, "a-conflict.mp4")); string(got) != "unrelated" {
		t.Fatalf("conflicting processing destination changed: %q", got)
	}
}

func TestBulkRequeueContinuesAfterFailureAndClassifiesMedia(t *testing.T) {
	root := t.TempDir()
	folders := testFolders(root)
	mustWriteFile(t, filepath.Join(folders.Staging, "a-heavy.mp4"), "heavy")
	mustWriteFile(t, filepath.Join(folders.Staging, "b-light-C.mp4"), "light")
	accepter := &recordingAccepter{reject: map[string]error{"a-heavy.mp4": queue.ErrQueueFull}}
	service := New(accepter, folders, probeRunner{})

	result := service.Requeue(context.Background(), Request{Location: Staging})
	if len(result.Accepted) != 1 || result.Accepted[0].FileName != "b-light-C.mp4" || result.Accepted[0].JobID == "" {
		t.Fatalf("accepted = %+v, want accepted light item with Job ID", result.Accepted)
	}
	if len(result.Failed) != 1 || result.Failed[0].FileName != "a-heavy.mp4" || result.Failed[0].JobID != "" {
		t.Fatalf("failed = %+v, want rejected heavy item without Job ID", result.Failed)
	}
	if len(accepter.jobs) != 2 || accepter.jobs[0].IsLight || !accepter.jobs[1].IsLight {
		t.Fatalf("classified jobs = %+v, want heavy then light", accepter.jobs)
	}
	if accepter.jobs[0].StagingPath == "" || accepter.jobs[1].StagingPath == "" {
		t.Fatalf("jobs missing staging paths: %+v", accepter.jobs)
	}
}

func TestManualRequeueCreatesFreshJobEachTime(t *testing.T) {
	root := t.TempDir()
	folders := testFolders(root)
	mustWriteFile(t, filepath.Join(folders.Staging, "movie-C.mp4"), "media")
	accepter := &recordingAccepter{}
	service := New(accepter, folders, probeRunner{})
	name := "movie-C.mp4"

	first := service.Requeue(context.Background(), Request{Location: Staging, FileName: &name})
	second := service.Requeue(context.Background(), Request{Location: Staging, FileName: &name})
	if len(first.Accepted) != 1 || len(second.Accepted) != 1 || first.Accepted[0].JobID == second.Accepted[0].JobID {
		t.Fatalf("Job IDs = %q and %q, want distinct accepted IDs", first.Accepted[0].JobID, second.Accepted[0].JobID)
	}
	if len(accepter.jobs) != 2 || accepter.jobs[0].ContentSignature == "" || accepter.jobs[0].SourceKey == "" {
		t.Fatalf("manual jobs missing durable identity: %+v", accepter.jobs)
	}
	if accepter.jobs[0].ContentSignature != accepter.jobs[1].ContentSignature || accepter.jobs[0].SourceKey != accepter.jobs[1].SourceKey {
		t.Fatalf("manual job identities differ: %+v", accepter.jobs)
	}
}

func TestCompletedDuplicateIsReportedAsSkipped(t *testing.T) {
	folders := testFolders(t.TempDir())
	mustWriteFile(t, filepath.Join(folders.Staging, "movie.mp4"), "media")
	accepter := &recordingAccepter{reject: map[string]error{
		"movie.mp4": &queue.ConflictError{JobID: "existing", Status: queue.StatusCompleted},
	}}

	name := "movie.mp4"
	result := New(accepter, folders, probeRunner{}).Requeue(context.Background(), Request{Location: Staging, FileName: &name})
	if len(result.Skipped) != 1 || result.Skipped[0].JobID != "existing" || len(result.Accepted) != 0 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, want completed duplicate reported as skipped", result)
	}
	if len(accepter.retries) != 0 {
		t.Fatalf("retries = %v, want none", accepter.retries)
	}
}

func TestFailedRequeueSkipsJobThatIsAlreadyActive(t *testing.T) {
	folders := testFolders(t.TempDir())
	failedPath := filepath.Join(folders.Failed, "movie.mp4")
	mustWriteFile(t, failedPath, "media")
	accepter := &recordingAccepter{foundJob: &queue.Job{ID: "existing", FileName: "movie.mp4", Status: queue.StatusProcessing}}

	name := "movie.mp4"
	result := New(accepter, folders, probeRunner{}).Requeue(context.Background(), Request{Location: Failed, FileName: &name})
	if len(result.Skipped) != 1 || result.Skipped[0].JobID != "existing" || len(result.Accepted) != 0 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, want active Job reported as skipped", result)
	}
	if len(accepter.retries) != 0 {
		t.Fatalf("retries = %v, want none", accepter.retries)
	}
	if got, err := os.ReadFile(failedPath); err != nil || string(got) != "media" {
		t.Fatalf("failed media changed: %q, %v", got, err)
	}
}

func TestListEmptyManagedLocationSucceeds(t *testing.T) {
	service := New(&recordingAccepter{}, testFolders(t.TempDir()), probeRunner{})
	for _, location := range []Location{Staging, Failed} {
		files, err := service.List(location)
		if err != nil || len(files) != 0 {
			t.Fatalf("List(%s) = %v, %v, want empty success", location, files, err)
		}
	}
}

func waitForJobStatus(t *testing.T, q *queue.Queue, id string, want queue.JobStatus) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if job := q.GetJob(id); job != nil && job.Status == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Job %q status = %#v, want %q", id, q.GetJob(id), want)
}

func testFolders(root string) config.FoldersConfig {
	return config.FoldersConfig{
		Staging: filepath.Join(root, "staging"),
		Process: filepath.Join(root, "processing"),
		Failed:  filepath.Join(root, "failed"),
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
