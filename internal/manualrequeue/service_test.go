package manualrequeue

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/queue"
)

type recordingAccepter struct {
	jobs       []*queue.Job
	reject     map[string]error
	beforeErr  func(*queue.Job)
	retries    []string
	retryPaths []string
	retryErr   error
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

type probeRunner struct{}

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

func TestFailedRequeueNeverOverwritesStagingConflict(t *testing.T) {
	root := t.TempDir()
	folders := testFolders(root)
	failedPath := filepath.Join(folders.Failed, "movie.mp4")
	stagingPath := filepath.Join(folders.Staging, "movie.mp4")
	mustWriteFile(t, failedPath, "failed")
	mustWriteFile(t, stagingPath, "staging")
	accepter := &recordingAccepter{}
	service := New(accepter, folders, probeRunner{})
	name := "movie.mp4"

	result := service.Requeue(context.Background(), Request{Location: Failed, FileName: &name})
	if len(result.Skipped) != 1 || len(result.Accepted) != 0 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, want skipped", result)
	}
	if got, _ := os.ReadFile(failedPath); string(got) != "failed" {
		t.Fatalf("failed source changed: %q", got)
	}
	if got, _ := os.ReadFile(stagingPath); string(got) != "staging" {
		t.Fatalf("staging destination changed: %q", got)
	}
	if len(accepter.jobs) != 0 {
		t.Fatalf("queue received %d jobs, want none", len(accepter.jobs))
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

func TestFailedFolderItemDoesNotInferOrRetryExistingJob(t *testing.T) {
	folders := testFolders(t.TempDir())
	failedPath := filepath.Join(folders.Failed, "movie.mp4")
	mustWriteFile(t, failedPath, "media")
	accepter := &recordingAccepter{reject: map[string]error{
		"movie.mp4": &queue.ConflictError{JobID: "existing", Status: queue.StatusFailed},
	}}

	name := "movie.mp4"
	result := New(accepter, folders, probeRunner{}).Requeue(context.Background(), Request{Location: Failed, FileName: &name})
	if len(result.Skipped) != 1 || result.Skipped[0].JobID != "" || len(result.Accepted) != 0 || len(result.Failed) != 0 {
		t.Fatalf("result = %+v, want unmatched failed-folder item reported as skipped", result)
	}
	if len(accepter.retries) != 0 || len(accepter.jobs) != 0 {
		t.Fatalf("failed-folder item reached queue: jobs=%d retries=%v", len(accepter.jobs), accepter.retries)
	}
	if _, err := os.Stat(failedPath); err != nil {
		t.Fatalf("failed media changed: %v", err)
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

func testFolders(root string) config.FoldersConfig {
	return config.FoldersConfig{
		Staging: filepath.Join(root, "staging"),
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
