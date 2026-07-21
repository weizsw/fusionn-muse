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
	jobs      []*queue.Job
	reject    map[string]error
	beforeErr func(*queue.Job)
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

func TestFailedRequeueMovesBeforeAcceptanceAndRecoversInStaging(t *testing.T) {
	root := t.TempDir()
	folders := testFolders(root)
	failedPath := filepath.Join(folders.Failed, "movie.mp4")
	stagingPath := filepath.Join(folders.Staging, "movie.mp4")
	mustWriteFile(t, failedPath, "media")

	accepter := &recordingAccepter{reject: map[string]error{"movie.mp4": queue.ErrQueueNotRunning}}
	accepter.beforeErr = func(*queue.Job) {
		if _, err := os.Stat(failedPath); !os.IsNotExist(err) {
			t.Errorf("failed source still exists at acceptance: %v", err)
		}
		if _, err := os.Stat(stagingPath); err != nil {
			t.Errorf("staging media missing at acceptance: %v", err)
		}
	}
	service := New(accepter, folders, probeRunner{})
	name := "movie.mp4"

	result := service.Requeue(context.Background(), Request{Location: Failed, FileName: &name})
	if len(result.Accepted) != 0 || len(result.Failed) != 1 {
		t.Fatalf("result = %+v, want one failure", result)
	}
	failure := result.Failed[0]
	if !failure.Staged || failure.JobID != "" || !errors.Is(failure.Err, queue.ErrQueueNotRunning) {
		t.Fatalf("failure = %+v, want staged queue rejection without Job ID", failure)
	}
	if got, err := os.ReadFile(stagingPath); err != nil || string(got) != "media" {
		t.Fatalf("staged media = %q, %v", got, err)
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
	if len(result.Failed) != 1 || !errors.Is(result.Failed[0].Err, ErrConflict) {
		t.Fatalf("result = %+v, want conflict", result)
	}
	if got, _ := os.ReadFile(failedPath); string(got) != "failed" {
		t.Fatalf("failed source overwritten or removed: %q", got)
	}
	if got, _ := os.ReadFile(stagingPath); string(got) != "staging" {
		t.Fatalf("staging destination overwritten: %q", got)
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
