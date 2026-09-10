package queue

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type processorFunc func(context.Context, *Job) error

func (f processorFunc) Process(ctx context.Context, job *Job) error { return f(ctx, job) }

type snapshotProcessor struct {
	processorFunc
	snapshot string
}

func (p snapshotProcessor) SnapshotSettings() (string, error) { return p.snapshot, nil }

func TestQueueDeduplicatesCleanedMediaAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	started := make(chan struct{})
	q, err := Open(dbPath, processorFunc(func(ctx context.Context, _ *Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	q.Start()

	job := NewJob("first", "/downloads/sone-269.mp4", "sone-269.mp4", "", "")
	if err := q.Accept(job); err != nil {
		t.Fatalf("accept first job: %v", err)
	}
	<-started

	duplicate := NewJob("second", "/manual/SONE-269-C.mp4", "SONE-269-C.mp4", "", "")
	if err := q.Accept(duplicate); !errors.Is(err, ErrDuplicateMedia) {
		t.Fatalf("duplicate error = %v, want %v", err, ErrDuplicateMedia)
	}
	var conflict *ConflictError
	if !errors.As(q.Accept(duplicate), &conflict) || conflict.JobID != "first" {
		t.Fatalf("conflict = %#v, want existing job first", conflict)
	}
	q.Stop()
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dbPath, processorFunc(func(context.Context, *Job) error { return nil }), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Accept(NewJob("third", "/other/xxxSONE-269.mp4", "xxxSONE-269.mp4", "", "")); !errors.Is(err, ErrQueueNotRunning) {
		t.Fatalf("non-running admission error = %v", err)
	}
	reopened.Start()
	defer reopened.Stop()
	if err := reopened.Accept(NewJob("third", "/other/xxxSONE-269.mp4", "xxxSONE-269.mp4", "", "")); !errors.Is(err, ErrDuplicateMedia) {
		t.Fatalf("persisted duplicate error = %v, want %v", err, ErrDuplicateMedia)
	}
}

func TestStorePrefersContentSignatureOverFilenameFallback(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.mp4")
	aliasPath := filepath.Join(root, "alias.mp4")
	otherDir := filepath.Join(root, "other")
	otherPath := filepath.Join(otherDir, "first.mp4")
	if err := os.MkdirAll(otherDir, 0755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		firstPath: "same media", aliasPath: "same media", otherPath: "different media",
	} {
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	store, err := openStore(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()
	if err := store.insertJob(NewJob("first", firstPath, "first.mp4", "", "")); err != nil {
		t.Fatal(err)
	}
	if err := store.insertJob(NewJob("alias", aliasPath, "alias.mp4", "", "")); !errors.Is(err, ErrDuplicateMedia) {
		t.Fatalf("same content error = %v, want %v", err, ErrDuplicateMedia)
	}
	if err := store.insertJob(NewJob("different", otherPath, "first.mp4", "", "")); err != nil {
		t.Fatalf("different content with same name rejected: %v", err)
	}
}

func TestIdentityLookupPrefersStrongSignatureOverLegacySourceKey(t *testing.T) {
	root := t.TempDir()
	media := filepath.Join(root, "canonical.mp4")
	if err := os.WriteFile(media, []byte("strong match"), 0644); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()
	legacy := NewJob("legacy", "/missing/collision.mp4", "collision.mp4", "", "")
	strong := NewJob("strong", media, "canonical.mp4", "", "")
	if err := store.insertJob(legacy); err != nil {
		t.Fatal(err)
	}
	if err := store.insertJob(strong); err != nil {
		t.Fatal(err)
	}

	got, err := store.jobByIdentity(strong.ContentSignature, legacy.SourceKey)
	if err != nil || got.ID != strong.ID {
		t.Fatalf("identity lookup = %#v, %v; want strong content match", got, err)
	}
}

func TestStoreMigratesLegacyMediaIdentity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`CREATE TABLE jobs (
		id TEXT PRIMARY KEY, media_id TEXT NOT NULL UNIQUE, source_path TEXT NOT NULL,
		file_name TEXT NOT NULL, torrent_name TEXT NOT NULL, category TEXT NOT NULL,
		status TEXT NOT NULL, error TEXT NOT NULL, retries INTEGER NOT NULL,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
		started_at INTEGER NOT NULL DEFAULT 0, completed_at INTEGER NOT NULL DEFAULT 0,
		is_light INTEGER NOT NULL DEFAULT 0, subtitle_detection_reason TEXT NOT NULL DEFAULT '',
		sidecar_subtitle_path TEXT NOT NULL DEFAULT '', staging_path TEXT NOT NULL DEFAULT '',
		processing_path TEXT NOT NULL DEFAULT '', subtitle_path TEXT NOT NULL DEFAULT '',
		translated_path TEXT NOT NULL DEFAULT '', checkpoint TEXT NOT NULL DEFAULT '',
		failure_stage TEXT NOT NULL DEFAULT '', transcription_source TEXT NOT NULL DEFAULT ''
	);
	INSERT INTO jobs (id, media_id, source_path, file_name, torrent_name, category, status, error, retries, created_at, updated_at)
	VALUES ('legacy', 'SONE-269.mp4', '/old/SONE-269-C.mp4', 'SONE-269-C.mp4', '', '', 'completed', '', 0, 1, 1);
	PRAGMA user_version = 1;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()
	job, err := store.job("legacy")
	if err != nil || job.SourceKey != "SONE-269.mp4" || job.ContentSignature != "" {
		t.Fatalf("migrated identity = %+v, %v", job, err)
	}
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("schema version = %d, want 2", version)
	}
	if err := store.insertJob(NewJob("duplicate", "/missing/SONE-269.mp4", "SONE-269.mp4", "", "")); !errors.Is(err, ErrDuplicateMedia) {
		t.Fatalf("legacy duplicate error = %v, want %v", err, ErrDuplicateMedia)
	}
}

func TestTranslationFailureRetriesFromPersistedTranscription(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	var mu sync.Mutex
	var starts []Stage
	var snapshots []string
	calls := 0
	q, err := Open(dbPath, processorFunc(func(ctx context.Context, job *Job) error {
		mu.Lock()
		starts = append(starts, job.StartStage)
		snapshots = append(snapshots, job.SettingsSnapshot)
		calls++
		call := calls
		mu.Unlock()

		if call == 1 {
			if err := job.SaveSettings(ctx, `{"provider":"frozen"}`); err != nil {
				return err
			}
			job.SubtitlePath = "/artifacts/show.en.srt"
			job.TranscriptionSource = TranscriptionGenerated
			if err := job.SaveCheckpoint(ctx, StageTranscribed); err != nil {
				return err
			}
			if err := job.BeginStage(ctx, StageTranslating); err != nil {
				return err
			}
			return errors.New("translator unavailable")
		}
		if job.SubtitlePath != "/artifacts/show.en.srt" {
			return errors.New("persisted transcription missing")
		}
		return nil
	}), 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	q.Start()
	defer q.Stop()

	if err := q.Accept(NewJob("job", "/downloads/show.mkv", "show.mkv", "", "")); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, q, "job", StatusCompleted)

	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 2 || starts[0] != StagePreparing || starts[1] != StageTranslating {
		t.Fatalf("attempt starts = %v, want [preparing translating]", starts)
	}
	if len(snapshots) != 2 || snapshots[1] != `{"provider":"frozen"}` {
		t.Fatalf("attempt snapshots = %v, want automatic retry to reuse first snapshot", snapshots)
	}
	detail := q.GetJobDetail("job")
	if detail == nil || len(detail.Attempts) != 2 || detail.Attempts[0].FailureStage != StageTranslating || detail.Attempts[1].Kind != AttemptAutomatic {
		t.Fatalf("job detail = %#v", detail)
	}
}

func TestRetryFromUsesReplacementAsProcessingMediaAfterTranscription(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	seen := make(chan *Job, 1)
	q := New(processorFunc(func(ctx context.Context, job *Job) error {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			job.ProcessingPath = "/missing/processing.mp4"
			if err := job.SaveCheckpoint(ctx, StageTranscribed); err != nil {
				return err
			}
			if err := job.BeginStage(ctx, StageTranslating); err != nil {
				return err
			}
			return errors.New("translation failed")
		}
		snapshot := *job
		seen <- &snapshot
		return nil
	}), 1, 0)
	q.Start()
	defer q.Stop()

	job := NewJob("job", "/downloads/show.mkv", "show.mkv", "", "")
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, q, job.ID, StatusFailed)
	replacement := filepath.Join(t.TempDir(), "show.mkv")
	if err := os.WriteFile(replacement, []byte("media"), 0644); err != nil {
		t.Fatal(err)
	}
	attempt, err := q.RetryFromAttempt(job.ID, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.StartStage != StageTranslating {
		t.Fatalf("retry start = %q, want %q", attempt.StartStage, StageTranslating)
	}
	select {
	case retried := <-seen:
		if retried.ProcessingPath != replacement {
			t.Fatalf("retry processing path = %q, want %q", retried.ProcessingPath, replacement)
		}
	case <-time.After(time.Second):
		t.Fatal("manual retry did not run")
	}
}

func TestInterruptedAttemptResumesAtFirstIncompleteStage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	checkpointed := make(chan struct{})
	q, err := Open(dbPath, processorFunc(func(ctx context.Context, job *Job) error {
		job.SubtitlePath = "/artifacts/show.en.srt"
		job.TranscriptionSource = TranscriptionGenerated
		if err := job.SaveCheckpoint(ctx, StageTranscribed); err != nil {
			return err
		}
		close(checkpointed)
		<-ctx.Done()
		return ctx.Err()
	}), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	if err := q.Accept(NewJob("job", "/downloads/show.mkv", "show.mkv", "", "")); err != nil {
		t.Fatal(err)
	}
	<-checkpointed
	q.Stop()
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}

	resumed := make(chan Stage, 1)
	q, err = Open(dbPath, processorFunc(func(_ context.Context, job *Job) error {
		resumed <- job.StartStage
		return nil
	}), 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	q.Start()
	defer q.Stop()
	if got := q.GetJob("job"); got == nil || got.Status != StatusInterrupted {
		t.Fatalf("job after restart = %#v, want interrupted", got)
	}
	select {
	case <-resumed:
		t.Fatal("interrupted attempt resumed without an explicit action")
	case <-time.After(25 * time.Millisecond):
	}
	if err := q.Resume("job"); err != nil {
		t.Fatalf("resume job: %v", err)
	}

	select {
	case stage := <-resumed:
		if stage != StageTranslating {
			t.Fatalf("resumed stage = %q, want %q", stage, StageTranslating)
		}
	case <-time.After(time.Second):
		t.Fatal("explicit Resume did not start the interrupted job")
	}
	waitForStatus(t, q, "job", StatusCompleted)
}

func TestAcceptSnapshotsSettingsBeforeProcessing(t *testing.T) {
	processor := snapshotProcessor{
		processorFunc: func(ctx context.Context, _ *Job) error {
			<-ctx.Done()
			return ctx.Err()
		},
		snapshot: `{"provider":"accepted"}`,
	}
	q, err := Open(filepath.Join(t.TempDir(), "state.db"), processor, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	q.Start()

	if err := q.Accept(NewJob("job", "/downloads/show.mkv", "show.mkv", "", "")); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, q, "job", StatusProcessing)
	detail := q.GetJobDetail("job")
	if detail == nil || len(detail.Attempts) != 1 || detail.Attempts[0].SettingsSnapshot != processor.snapshot {
		t.Fatalf("job detail = %#v, want accepted settings snapshot", detail)
	}
}

func TestRetranslateSnapshotsCurrentSettingsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	store, err := openStore(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()

	subtitlePath := filepath.Join(root, "show.srt")
	if err := os.WriteFile(subtitlePath, []byte("source subtitle"), 0644); err != nil {
		t.Fatal(err)
	}
	job := NewJob("job", "/downloads/show.mkv", "show.mkv", "", "")
	job.SettingsSnapshot = `{"provider":"accepted"}`
	if err := store.insertJob(job); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixNano()
	if _, err := store.db.Exec(`UPDATE jobs SET status=?, checkpoint=?, subtitle_path=?, completed_at=?, updated_at=? WHERE id=?`,
		StatusCompleted, StageTranscribed, subtitlePath, now, now, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE attempts SET status=?, completed_at=? WHERE job_id=?`, AttemptCompleted, now, job.ID); err != nil {
		t.Fatal(err)
	}

	first, err := store.createAction(job.ID, AttemptRetranslate, `{"provider":"current"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.createAction(job.ID, AttemptRetranslate, `{"provider":"newer"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.SettingsSnapshot != `{"provider":"current"}` || second.SettingsSnapshot != first.SettingsSnapshot {
		t.Fatalf("attempts = %#v, %#v, want one current-settings attempt", first, second)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE job_id=?`, job.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("attempt count = %d, want initial plus one retranslation", count)
	}
	claimed, err := store.claimNext(true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.AttemptID != first.ID {
		t.Fatalf("claimed attempt = %#v, want retranslation %d in light lane", claimed, first.ID)
	}
}

func TestPendingAutomaticRetrySurvivesShutdown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	first := newCountingProcessor(errors.New("translation failed"))
	q, err := Open(dbPath, first, 2, 60_000)
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	if err := q.Accept(NewJob("job", "/downloads/show.mkv", "show.mkv", "", "")); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, first, 1)
	deadline := time.Now().Add(time.Second)
	for {
		detail := q.GetJobDetail("job")
		if detail != nil && len(detail.Attempts) == 2 && detail.Attempts[1].Status == AttemptPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("automatic retry was not durably queued: %#v", detail)
		}
		time.Sleep(time.Millisecond)
	}
	q.Stop()
	if got := q.GetJob("job"); got == nil || got.Status != StatusPending {
		t.Fatalf("job after shutdown = %#v, want pending retry", got)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}

	second := newCountingProcessor(nil)
	q, err = Open(dbPath, second, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	q.Start()
	waitForStatus(t, q, "job", StatusCompleted)
	if second.callCount() != 1 {
		t.Fatalf("recovered retry calls = %d, want 1", second.callCount())
	}
}
