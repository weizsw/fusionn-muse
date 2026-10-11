package queue

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStageHandoffRecoversStorageWithoutRepeatingWork(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(media, []byte("media"), 0644); err != nil {
		t.Fatal(err)
	}
	var preparations atomic.Int32
	q := New(stageProcessorFunc(func(ctx context.Context, job *Job) (Stage, error) {
		if job.AttemptStage == StagePreparing {
			preparations.Add(1)
			if err := job.SaveCheckpoint(ctx, StagePrepared); err != nil {
				return "", err
			}
			return StageMoving, nil
		}
		return StageDelivered, nil
	}), 1, 0)
	q.Start()
	defer q.Close()
	if _, err := q.store.db.Exec(`CREATE TRIGGER reject_handoff BEFORE UPDATE ON attempts
		WHEN NEW.status='pending' AND NEW.current_stage='moving'
		BEGIN SELECT RAISE(ABORT, 'temporary storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := q.Accept(NewJob("job", media, "movie.mp4", "", "")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for q.GetJob("job").Checkpoint != StagePrepared && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if q.GetJob("job").Checkpoint != StagePrepared {
		t.Fatal("stage did not produce its durable result")
	}
	// Keep storage unavailable after the stage has returned, then repair it.
	time.Sleep(100 * time.Millisecond)
	data, err := json.Marshal(q.GetJobDetail("job"))
	if err != nil || !strings.Contains(string(data), `"persistence_error":"`) {
		t.Fatalf("blocked persistence is not visible: %s, %v", data, err)
	}
	if _, err := q.store.db.Exec(`DROP TRIGGER reject_handoff`); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, q, "job", StatusCompleted)
	detail := q.GetJobDetail("job")
	if preparations.Load() != 1 || len(detail.Attempts) != 1 {
		t.Fatalf("storage recovery repeated work: preparations=%d, attempts=%+v", preparations.Load(), detail.Attempts)
	}
}

func TestAttemptResultRecoversStorageWithoutRepeatingWork(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(media, []byte("media"), 0644); err != nil {
		t.Fatal(err)
	}
	var executions atomic.Int32
	produced := make(chan struct{})
	q := New(processorFunc(func(context.Context, *Job) error {
		executions.Add(1)
		close(produced)
		return nil
	}), 1, 0)
	q.Start()
	defer q.Close()
	if _, err := q.store.db.Exec(`CREATE TRIGGER reject_result BEFORE UPDATE ON attempts
		WHEN NEW.status='completed'
		BEGIN SELECT RAISE(ABORT, 'temporary storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := q.Accept(NewJob("job", media, "movie.mp4", "", "")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-produced:
	case <-time.After(time.Second):
		t.Fatal("work did not run")
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := q.store.db.Exec(`DROP TRIGGER reject_result`); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, q, "job", StatusCompleted)
	if executions.Load() != 1 || len(q.GetJobDetail("job").Attempts) != 1 {
		t.Fatal("storage recovery repeated external work")
	}
}

func TestStageCheckpointRecoversStorageWithoutRepeatingWork(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "movie.mp4")
	if err := os.WriteFile(media, []byte("media"), 0644); err != nil {
		t.Fatal(err)
	}
	produced := make(chan struct{})
	var executions atomic.Int32
	q := New(stageProcessorFunc(func(ctx context.Context, job *Job) (Stage, error) {
		if job.AttemptStage == StagePreparing {
			executions.Add(1)
			close(produced)
			if err := job.SaveCheckpoint(ctx, StagePrepared); err != nil {
				return "", err
			}
			return StageMoving, nil
		}
		return StageDelivered, nil
	}), 1, 0)
	q.Start()
	defer q.Close()
	if _, err := q.store.db.Exec(`CREATE TRIGGER reject_checkpoint BEFORE UPDATE ON attempts
		WHEN NEW.current_stage='prepared' AND NEW.status='processing'
		BEGIN SELECT RAISE(ABORT, 'temporary storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := q.Accept(NewJob("job", media, "movie.mp4", "", "")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-produced:
	case <-time.After(time.Second):
		t.Fatal("stage did not run")
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := q.store.db.Exec(`DROP TRIGGER reject_checkpoint`); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, q, "job", StatusCompleted)
	if executions.Load() != 1 || len(q.GetJobDetail("job").Attempts) != 1 {
		t.Fatal("checkpoint recovery repeated stage")
	}
}

func TestLegacyAmbiguousArtifactOwnershipIsBlocked(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "state.db")
	output := filepath.Join(root, "shared.srt")
	q, err := Open(dbPath, processorFunc(func(ctx context.Context, job *Job) error { return job.ClaimArtifacts(ctx, output) }), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	for _, id := range []string{"first", "second"} {
		path := filepath.Join(root, id+".mp4")
		if err := os.WriteFile(path, []byte(id), 0644); err != nil {
			t.Fatal(err)
		}
		if err := q.store.insertJob(NewJob(id, path, id+".mp4", "", "")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.store.db.Exec(`UPDATE jobs SET subtitle_path=?; PRAGMA user_version=3`, output); err != nil {
		t.Fatal(err)
	}
	q.Close()
	q, err = Open(dbPath, processorFunc(func(ctx context.Context, job *Job) error { return job.ClaimArtifacts(ctx, output) }), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	q.Start()
	for _, id := range []string{"first", "second"} {
		waitForStatus(t, q, id, StatusFailed)
		if !strings.Contains(q.GetJob(id).Error, "artifact conflict") {
			t.Fatalf("ambiguous legacy ownership was accepted: %+v", q.GetJob(id))
		}
	}
}

func TestInvalidArtifactPathFailsInsteadOfWaitingForStorage(t *testing.T) {
	root := t.TempDir()
	q := New(processorFunc(func(ctx context.Context, job *Job) error {
		return job.ClaimArtifacts(ctx, filepath.Join(root, "invalid\x00.srt"))
	}), 1, 0)
	q.Start()
	defer q.Close()
	path := filepath.Join(root, "media.mp4")
	if err := os.WriteFile(path, []byte("media"), 0644); err != nil {
		t.Fatal(err)
	}
	job := NewJob("invalid-output", path, "media.mp4", "", "")
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, q, job.ID, StatusFailed)
	got := q.GetJob(job.ID)
	if got.PersistenceError != "" || !strings.Contains(got.Error, "artifact unavailable") {
		t.Fatalf("invalid path was treated as transient database failure: %+v", got)
	}
}
