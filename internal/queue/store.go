package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Register the SQLite driver.
)

const jobColumns = `id, media_id, content_signature, source_key, source_path, file_name, torrent_name, category,
	status, error, retries, created_at, updated_at, started_at, completed_at,
	is_light, subtitle_detection_reason, sidecar_subtitle_path, staging_path,
	processing_path, subtitle_path, translated_path, checkpoint, failure_stage,
	transcription_source`

var (
	ErrDuplicateMedia = errors.New("duplicate media")
	ErrNotFound       = errors.New("job not found")
	ErrInvalidAction  = errors.New("action is not valid for job state")
)

type store struct {
	db *sql.DB
}

func openStore(path string) (*store, error) {
	dsn := path
	if path != ":memory:" && len(path) >= 5 && path[:5] != "file:" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve state database: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			return nil, fmt.Errorf("create state directory: %w", err)
		}
		dsn = "file:" + filepath.ToSlash(absolute)
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	dsn += separator + "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	// ponytail: one connection matches the documented single-instance worker; raise only if DB contention is measured.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect state database: %w", err)
	}
	s := &store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.interruptAbandoned(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *store) migrate() error {
	const currentSchemaVersion = 2
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read state database version: %w", err)
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("state database version %d is newer than supported version %d", version, currentSchemaVersion)
	}
	const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id TEXT PRIMARY KEY,
	media_id TEXT NOT NULL UNIQUE,
	content_signature TEXT NOT NULL DEFAULT '',
	source_key TEXT NOT NULL DEFAULT '',
	source_path TEXT NOT NULL,
	file_name TEXT NOT NULL,
	torrent_name TEXT NOT NULL DEFAULT '',
	category TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT '',
	retries INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	started_at INTEGER NOT NULL DEFAULT 0,
	completed_at INTEGER NOT NULL DEFAULT 0,
	is_light INTEGER NOT NULL DEFAULT 0,
	subtitle_detection_reason TEXT NOT NULL DEFAULT '',
	sidecar_subtitle_path TEXT NOT NULL DEFAULT '',
	staging_path TEXT NOT NULL DEFAULT '',
	processing_path TEXT NOT NULL DEFAULT '',
	subtitle_path TEXT NOT NULL DEFAULT '',
	translated_path TEXT NOT NULL DEFAULT '',
	checkpoint TEXT NOT NULL DEFAULT '',
	failure_stage TEXT NOT NULL DEFAULT '',
	transcription_source TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS attempts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	job_id TEXT NOT NULL REFERENCES jobs(id),
	kind TEXT NOT NULL,
	status TEXT NOT NULL,
	retry_index INTEGER NOT NULL DEFAULT 0,
	start_stage TEXT NOT NULL,
	current_stage TEXT NOT NULL DEFAULT '',
	failure_stage TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	translation_cache_enabled INTEGER NOT NULL DEFAULT 1,
	settings_snapshot TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	started_at INTEGER NOT NULL DEFAULT 0,
	completed_at INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS attempts_one_active_per_job
	ON attempts(job_id) WHERE status IN ('pending', 'processing');
CREATE INDEX IF NOT EXISTS attempts_pending_order ON attempts(status, id);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("migrate state database: %w", err)
	}
	if version >= currentSchemaVersion {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin state database migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`ALTER TABLE jobs ADD COLUMN content_signature TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE jobs ADD COLUMN source_key TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := tx.Exec(statement); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
			return fmt.Errorf("migrate state database: %w", err)
		}
	}
	if _, err := tx.Exec(`
UPDATE jobs SET source_key=media_id WHERE source_key='';
CREATE UNIQUE INDEX IF NOT EXISTS jobs_content_signature_unique
	ON jobs(content_signature) WHERE content_signature<>'';
CREATE UNIQUE INDEX IF NOT EXISTS jobs_source_key_fallback_unique
	ON jobs(source_key) WHERE content_signature='' AND source_key<>'';
CREATE INDEX IF NOT EXISTS jobs_history_order ON jobs(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS attempts_job_history ON attempts(job_id, id);
PRAGMA user_version = 2;`); err != nil {
		return fmt.Errorf("migrate state database: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit state database migration: %w", err)
	}
	return nil
}

func (s *store) interruptAbandoned() error {
	now := time.Now().UnixNano()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE attempts SET status=?, error=?, completed_at=? WHERE status=?`, AttemptInterrupted, "service stopped during processing", now, AttemptProcessing); err != nil {
		return fmt.Errorf("interrupt attempts: %w", err)
	}
	if _, err := tx.Exec(`UPDATE jobs SET status=?, error=?, updated_at=? WHERE id IN (
		SELECT job_id FROM attempts WHERE status=? AND completed_at=?) AND status=?`, StatusInterrupted, "service stopped during processing", now, AttemptInterrupted, now, StatusProcessing); err != nil {
		return fmt.Errorf("interrupt jobs: %w", err)
	}
	return tx.Commit()
}

func (s *store) insertJob(job *Job) error {
	return s.insertJobWithStatus(job, AttemptPending)
}

func (s *store) reserveJob(job *Job) error {
	return s.insertJobWithStatus(job, AttemptProcessing)
}

func (s *store) insertJobWithStatus(job *Job, attemptStatus AttemptStatus) error {
	now := time.Now()
	if job.SourceKey == "" {
		job.SourceKey = MediaID(job.FileName)
	}
	if job.ContentSignature == "" {
		job.ContentSignature = fileSignature(job.SourcePath)
	}
	job.MediaID = job.ContentSignature
	if job.MediaID == "" {
		job.MediaID = job.SourceKey
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	job.Status = StatusPending
	job.Error = ""
	job.Checkpoint = ""
	start := StagePreparing
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if existing, getErr := queryJobByIdentity(tx, job.ContentSignature, job.SourceKey); getErr == nil {
		return &ConflictError{JobID: existing.ID, Status: existing.Status}
	} else if !errors.Is(getErr, ErrNotFound) {
		return fmt.Errorf("check duplicate media: %w", getErr)
	}
	if attemptStatus == AttemptProcessing {
		job.Status = StatusProcessing
		job.StartedAt = now
	}
	_, err = tx.Exec(`INSERT INTO jobs (`+jobColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		job.ID, job.MediaID, job.ContentSignature, job.SourceKey, job.SourcePath, job.FileName, job.TorrentName, job.Category,
		job.Status, job.Error, 0, unixNano(job.CreatedAt), unixNano(job.UpdatedAt), unixNano(job.StartedAt), 0,
		boolInt(job.IsLight), job.SubtitleDetectionReason, job.SidecarSubtitlePath, job.StagingPath,
		job.ProcessingPath, job.SubtitlePath, job.TranslatedPath, job.Checkpoint, job.FailureStage,
		job.TranscriptionSource)
	if err != nil {
		if existing, getErr := queryJobByMediaID(tx, job.MediaID); getErr == nil {
			return &ConflictError{JobID: existing.ID, Status: existing.Status}
		}
		if existing, getErr := queryJob(tx, job.ID); getErr == nil {
			return fmt.Errorf("%w: %s", ErrDuplicateJobID, existing.ID)
		}
		return fmt.Errorf("insert job: %w", err)
	}
	attemptID, _, err := s.insertAttemptTx(tx, job.ID, AttemptInitial, 0, start, true)
	if err != nil {
		return err
	}
	if attemptStatus == AttemptProcessing {
		if _, err := tx.Exec(`UPDATE attempts SET status=?, current_stage=?, started_at=? WHERE id=?`,
			AttemptProcessing, StagePreparing, now.UnixNano(), attemptID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE attempts SET settings_snapshot=? WHERE id=?`, job.SettingsSnapshot, attemptID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	setAttempt(job, Attempt{ID: attemptID, JobID: job.ID, Kind: AttemptInitial, Status: attemptStatus,
		StartStage: start, CurrentStage: StagePreparing, TranslationCacheEnabled: true,
		SettingsSnapshot: job.SettingsSnapshot, CreatedAt: now, StartedAt: job.StartedAt})
	return nil
}

func (s *store) completeAdmission(job *Job, staged bool) error {
	start, current, checkpoint := StagePreparing, Stage(""), Stage("")
	if staged {
		start, checkpoint = StageMoving, StagePrepared
	}
	now := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`UPDATE attempts SET status=?, start_stage=?, current_stage=?, started_at=0 WHERE id=? AND status=?`,
		AttemptPending, start, current, job.AttemptID, AttemptProcessing)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("complete admission: attempt %d is not processing", job.AttemptID)
	}
	if _, err := tx.Exec(`UPDATE jobs SET status=?, error='', failure_stage='', checkpoint=?, staging_path=?, started_at=0, updated_at=? WHERE id=?`,
		StatusPending, checkpoint, job.StagingPath, now.UnixNano(), job.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	job.Status = StatusPending
	job.Checkpoint = checkpoint
	job.StartedAt = time.Time{}
	job.StartStage = start
	job.UpdatedAt = now
	return nil
}

func (s *store) insertAttemptTx(tx *sql.Tx, jobID string, kind AttemptKind, retryIndex int, start Stage, cacheEnabled bool) (int64, time.Time, error) {
	createdAt := time.Now()
	result, err := tx.Exec(`INSERT INTO attempts (job_id, kind, status, retry_index, start_stage, translation_cache_enabled, created_at)
		VALUES (?,?,?,?,?,?,?)`, jobID, kind, AttemptPending, retryIndex, start, boolInt(cacheEnabled), createdAt.UnixNano())
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("insert attempt: %w", err)
	}
	id, err := result.LastInsertId()
	return id, createdAt, err
}

func (s *store) claimNext(light bool, automaticReadyBefore time.Time) (*Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var attempt Attempt
	var cache int
	err = tx.QueryRow(`SELECT a.id, a.job_id, a.kind, a.retry_index, a.start_stage, a.translation_cache_enabled, a.settings_snapshot
		FROM attempts a JOIN jobs j ON j.id=a.job_id
		WHERE a.status=? AND (a.retry_index=0 OR a.created_at<=?)
			AND (j.is_light=1 OR (a.retry_index=0 AND a.start_stage IN (?,?)))=?
		ORDER BY a.id LIMIT 1`,
		AttemptPending, automaticReadyBefore.UnixNano(), StageTranslating, StageDelivering, boolInt(light)).Scan(
		&attempt.ID, &attempt.JobID, &attempt.Kind, &attempt.RetryIndex, &attempt.StartStage, &cache, &attempt.SettingsSnapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select pending attempt: %w", err)
	}
	attempt.TranslationCacheEnabled = cache != 0
	job, err := startAttemptTx(tx, attempt)
	if err != nil || job == nil {
		return job, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *store) claimNextStage(lane workerLane, automaticReadyBefore time.Time) (*Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	const effectiveStage = `CASE WHEN a.current_stage='' THEN a.start_stage ELSE a.current_stage END`
	predicate := ""
	args := []any{AttemptPending, AttemptAutomatic, AttemptRetranslate, automaticReadyBefore.UnixNano()}
	switch lane {
	case laneOrchestration:
		predicate = `AND (j.is_light=1 OR ` + effectiveStage + ` IN (?,?,?))`
		args = append(args, StagePreparing, StageMoving, StageDelivering)
	case laneTranscription:
		predicate = `AND j.is_light=0 AND ` + effectiveStage + `=?`
		args = append(args, StageTranscribing)
	case laneTranslation:
		predicate = `AND j.is_light=0 AND ` + effectiveStage + `=?`
		args = append(args, StageTranslating)
	default:
		return nil, fmt.Errorf("unknown worker lane %d", lane)
	}

	var attempt Attempt
	var cache int
	var startedAt int64
	query := `SELECT a.id, a.job_id, a.kind, a.retry_index, a.start_stage, ` + effectiveStage + `,
		a.translation_cache_enabled, a.settings_snapshot, a.started_at
		FROM attempts a JOIN jobs j ON j.id=a.job_id
		WHERE a.status=? AND (NOT (a.kind=? OR (a.kind=? AND a.retry_index>0)) OR a.created_at<=?) ` + predicate + `
		ORDER BY a.id LIMIT 1`
	err = tx.QueryRow(query, args...).Scan(&attempt.ID, &attempt.JobID, &attempt.Kind, &attempt.RetryIndex,
		&attempt.StartStage, &attempt.CurrentStage, &cache, &attempt.SettingsSnapshot, &startedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select pending stage: %w", err)
	}
	attempt.TranslationCacheEnabled = cache != 0
	attempt.StartedAt = fromUnixNano(startedAt)
	job, err := startStagedAttemptTx(tx, attempt)
	if err != nil || job == nil {
		return job, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *store) claimAttempt(id int64) (*Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var attempt Attempt
	var cache int
	err = tx.QueryRow(`SELECT id, job_id, kind, retry_index, start_stage, translation_cache_enabled, settings_snapshot
		FROM attempts WHERE id=? AND status=?`, id, AttemptPending).Scan(
		&attempt.ID, &attempt.JobID, &attempt.Kind, &attempt.RetryIndex, &attempt.StartStage, &cache, &attempt.SettingsSnapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select pending attempt: %w", err)
	}
	attempt.TranslationCacheEnabled = cache != 0
	job, err := startAttemptTx(tx, attempt)
	if err != nil || job == nil {
		return job, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

func startAttemptTx(tx *sql.Tx, attempt Attempt) (*Job, error) {
	now := time.Now()
	result, err := tx.Exec(`UPDATE attempts SET status=?, current_stage=?, started_at=? WHERE id=? AND status=?`,
		AttemptProcessing, attempt.StartStage, now.UnixNano(), attempt.ID, AttemptPending)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, nil
	}
	if _, err := tx.Exec(`UPDATE jobs SET status=?, error='', failure_stage='', started_at=CASE WHEN started_at=0 THEN ? ELSE started_at END, updated_at=?
		WHERE id=? AND NOT(status=? AND ?=?)`,
		StatusProcessing, now.UnixNano(), now.UnixNano(), attempt.JobID, StatusCompleted, attempt.Kind, AttemptRetranslate); err != nil {
		return nil, err
	}
	job, err := queryJob(tx, attempt.JobID)
	if err != nil {
		return nil, err
	}
	attempt.Status = AttemptProcessing
	attempt.CurrentStage = attempt.StartStage
	attempt.StartedAt = now
	setAttempt(job, attempt)
	return job, nil
}

func startStagedAttemptTx(tx *sql.Tx, attempt Attempt) (*Job, error) {
	now := time.Now()
	result, err := tx.Exec(`UPDATE attempts SET status=?, current_stage=?,
		started_at=CASE WHEN started_at=0 THEN ? ELSE started_at END WHERE id=? AND status=?`,
		AttemptProcessing, attempt.CurrentStage, now.UnixNano(), attempt.ID, AttemptPending)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, nil
	}
	if _, err := tx.Exec(`UPDATE jobs SET status=?, error='', failure_stage='',
		started_at=CASE WHEN started_at=0 THEN ? ELSE started_at END, updated_at=?
		WHERE id=? AND NOT(status=? AND ?=?)`, StatusProcessing, now.UnixNano(), now.UnixNano(),
		attempt.JobID, StatusCompleted, attempt.Kind, AttemptRetranslate); err != nil {
		return nil, err
	}
	job, err := queryJob(tx, attempt.JobID)
	if err != nil {
		return nil, err
	}
	attempt.Status = AttemptProcessing
	if attempt.StartedAt.IsZero() {
		attempt.StartedAt = now
	}
	setAttempt(job, attempt)
	return job, nil
}

func (s *store) updateProgress(ctx context.Context, job *Job, stage Stage, checkpoint bool) error {
	now := time.Now().UnixNano()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE attempts SET current_stage=? WHERE id=? AND status=?`, stage, job.AttemptID, AttemptProcessing)
	if err != nil {
		return fmt.Errorf("persist progress: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("persist progress: attempt %d is not processing", job.AttemptID)
	}
	if checkpoint {
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET file_name=?, staging_path=?, processing_path=?, subtitle_path=?, translated_path=?,
			checkpoint=CASE WHEN ?='retranslation' THEN checkpoint ELSE ? END, transcription_source=?, is_light=?, subtitle_detection_reason=?, sidecar_subtitle_path=?, updated_at=? WHERE id=?`,
			job.FileName, job.StagingPath, job.ProcessingPath, job.SubtitlePath, job.TranslatedPath,
			job.AttemptKind, stage, job.TranscriptionSource, boolInt(job.IsLight), job.SubtitleDetectionReason, job.SidecarSubtitlePath, now, job.ID)
		if err != nil {
			return fmt.Errorf("persist progress: %w", err)
		}
	}
	return tx.Commit()
}

func (s *store) handoff(job *Job, next Stage) error {
	now := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var kind AttemptKind
	if err := tx.QueryRow(`SELECT kind FROM attempts WHERE id=? AND status=?`, job.AttemptID, AttemptProcessing).Scan(&kind); err != nil {
		return fmt.Errorf("handoff attempt %d: %w", job.AttemptID, err)
	}
	result, err := tx.Exec(`UPDATE attempts SET status=?, current_stage=? WHERE id=? AND status=?`,
		AttemptPending, next, job.AttemptID, AttemptProcessing)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return fmt.Errorf("handoff attempt %d: no longer processing", job.AttemptID)
	}
	if kind != AttemptRetranslate || job.Status != StatusCompleted {
		if _, err := tx.Exec(`UPDATE jobs SET status=?, error='', failure_stage='', updated_at=? WHERE id=?`,
			StatusPending, now.UnixNano(), job.ID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	job.AttemptStatus = AttemptPending
	job.AttemptStage = next
	if kind != AttemptRetranslate || job.Status != StatusCompleted {
		job.Status = StatusPending
	}
	job.UpdatedAt = now
	return nil
}

func (s *store) saveSettings(ctx context.Context, attemptID int64, snapshot string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE attempts SET settings_snapshot=? WHERE id=?`, snapshot, attemptID)
	if err != nil {
		return fmt.Errorf("persist settings snapshot: %w", err)
	}
	return nil
}

func (s *store) finish(job *Job, processErr error, maxAttempts int) (Stage, *Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var current Stage
	var kind AttemptKind
	var attemptStatus AttemptStatus
	if err := tx.QueryRow(`SELECT current_stage, kind, status FROM attempts WHERE id=?`, job.AttemptID).Scan(&current, &kind, &attemptStatus); err != nil {
		return "", nil, err
	}
	if attemptStatus != AttemptProcessing {
		return current, nil, fmt.Errorf("finish attempt %d: status is %s", job.AttemptID, attemptStatus)
	}
	now := time.Now()
	if processErr == nil {
		result, err := tx.Exec(`UPDATE attempts SET status=?, error='', failure_stage='', completed_at=? WHERE id=? AND status=?`, AttemptCompleted, now.UnixNano(), job.AttemptID, AttemptProcessing)
		if err != nil {
			return "", nil, err
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return current, nil, fmt.Errorf("finish attempt %d: no longer processing", job.AttemptID)
		}
		if _, err := tx.Exec(`UPDATE jobs SET status=?, error='', failure_stage='', retries=?, file_name=?, staging_path=?, processing_path=?, subtitle_path=?, translated_path=?, checkpoint=?, transcription_source=?, completed_at=?, updated_at=? WHERE id=?`,
			StatusCompleted, job.Retries, job.FileName, job.StagingPath, job.ProcessingPath, job.SubtitlePath, job.TranslatedPath, job.Checkpoint, job.TranscriptionSource, now.UnixNano(), now.UnixNano(), job.ID); err != nil {
			return "", nil, err
		}
		return current, nil, tx.Commit()
	}

	status := AttemptFailed
	jobStatus := StatusFailed
	if errors.Is(processErr, context.Canceled) {
		status = AttemptInterrupted
		jobStatus = StatusInterrupted
	}
	result, err := tx.Exec(`UPDATE attempts SET status=?, error=?, failure_stage=?, completed_at=? WHERE id=? AND status=?`, status, processErr.Error(), current, now.UnixNano(), job.AttemptID, AttemptProcessing)
	if err != nil {
		return "", nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return current, nil, fmt.Errorf("finish attempt %d: no longer processing", job.AttemptID)
	}
	if status == AttemptFailed && canAutomaticallyRetry(job, current, processErr, maxAttempts) {
		next, err := createAutomaticTx(tx, job, now)
		if err != nil {
			return current, nil, err
		}
		return current, next, tx.Commit()
	}
	if kind != AttemptRetranslate || job.Status != StatusCompleted {
		if _, err := tx.Exec(`UPDATE jobs SET status=?, error=?, failure_stage=?, retries=?, file_name=?, staging_path=?, processing_path=?, subtitle_path=?, translated_path=?, checkpoint=?, transcription_source=?, completed_at=?, updated_at=? WHERE id=?`,
			jobStatus, processErr.Error(), current, job.Retries, job.FileName, job.StagingPath, job.ProcessingPath, job.SubtitlePath, job.TranslatedPath, job.Checkpoint, job.TranscriptionSource, now.UnixNano(), now.UnixNano(), job.ID); err != nil {
			return "", nil, err
		}
	}
	return current, nil, tx.Commit()
}

func createAutomaticTx(tx *sql.Tx, previous *Job, now time.Time) (*Job, error) {
	kind := AttemptAutomatic
	if previous.AttemptKind == AttemptRetranslate {
		kind = AttemptRetranslate
	}
	settings := attemptSettings(tx, previous.AttemptID)
	result, err := tx.Exec(`INSERT INTO attempts (job_id, kind, status, retry_index, start_stage,
		translation_cache_enabled, settings_snapshot, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		previous.ID, kind, AttemptPending, previous.Retries+1, StageTranslating,
		boolInt(previous.TranslationCacheEnabled), settings, now.UnixNano())
	if err != nil {
		return nil, fmt.Errorf("insert automatic retry: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	if kind != AttemptRetranslate || previous.Status != StatusCompleted {
		if _, err := tx.Exec(`UPDATE jobs SET status=?, error='', failure_stage='', retries=?, completed_at=0, updated_at=? WHERE id=?`,
			StatusPending, previous.Retries+1, now.UnixNano(), previous.ID); err != nil {
			return nil, err
		}
	}
	job, err := queryJob(tx, previous.ID)
	if err != nil {
		return nil, err
	}
	setAttempt(job, Attempt{ID: id, JobID: previous.ID, Kind: kind, Status: AttemptPending,
		RetryIndex: previous.Retries + 1, StartStage: StageTranslating,
		TranslationCacheEnabled: previous.TranslationCacheEnabled, SettingsSnapshot: settings, CreatedAt: now})
	return job, nil
}

func attemptSettings(tx *sql.Tx, attemptID int64) string {
	var snapshot string
	_ = tx.QueryRow(`SELECT settings_snapshot FROM attempts WHERE id=?`, attemptID).Scan(&snapshot)
	return snapshot
}

func (s *store) createAction(jobID string, kind AttemptKind, settings, replacementPath string) (*Attempt, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	job, err := queryJob(tx, jobID)
	if err != nil {
		return nil, err
	}

	var active Attempt
	var activeCache int
	var activeCreated, activeStarted, activeCompleted int64
	err = tx.QueryRow(`SELECT id, job_id, kind, status, retry_index, start_stage, current_stage, failure_stage,
		error, translation_cache_enabled, settings_snapshot, created_at, started_at, completed_at
		FROM attempts WHERE job_id=? AND status IN (?, ?) ORDER BY id DESC LIMIT 1`,
		jobID, AttemptPending, AttemptProcessing).Scan(
		&active.ID, &active.JobID, &active.Kind, &active.Status, &active.RetryIndex, &active.StartStage,
		&active.CurrentStage, &active.FailureStage, &active.Error, &activeCache, &active.SettingsSnapshot,
		&activeCreated, &activeStarted, &activeCompleted)
	if err == nil {
		if active.Kind != kind {
			return nil, fmt.Errorf("%w: another action is active", ErrInvalidAction)
		}
		active.TranslationCacheEnabled = activeCache != 0
		active.CreatedAt = fromUnixNano(activeCreated)
		active.StartedAt = fromUnixNano(activeStarted)
		active.CompletedAt = fromUnixNano(activeCompleted)
		return &active, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	var start Stage
	cache := true
	settingsSnapshot := settings
	switch kind {
	case AttemptManual:
		if job.Status != StatusFailed {
			return nil, fmt.Errorf("%w: retry requires failed job", ErrInvalidAction)
		}
		start = nextStage(job.Checkpoint, job.IsLight)
		if replacementPath != "" {
			now := time.Now().UnixNano()
			switch start {
			case StagePreparing:
				job.SourcePath = replacementPath
				_, err = tx.Exec(`UPDATE jobs SET source_path=?, updated_at=? WHERE id=?`, replacementPath, now, jobID)
			case StageMoving:
				job.StagingPath = replacementPath
				_, err = tx.Exec(`UPDATE jobs SET staging_path=?, updated_at=? WHERE id=?`, replacementPath, now, jobID)
			default:
				job.ProcessingPath = replacementPath
				_, err = tx.Exec(`UPDATE jobs SET processing_path=?, updated_at=? WHERE id=?`, replacementPath, now, jobID)
			}
			if err != nil {
				return nil, err
			}
		}
		if err := tx.QueryRow(`SELECT settings_snapshot FROM attempts WHERE job_id=? ORDER BY id LIMIT 1`, jobID).Scan(&settingsSnapshot); err != nil {
			return nil, err
		}
	case AttemptResume:
		if job.Status != StatusInterrupted {
			return nil, fmt.Errorf("%w: resume requires interrupted job", ErrInvalidAction)
		}
		start = nextStage(job.Checkpoint, job.IsLight)
		if err := tx.QueryRow(`SELECT settings_snapshot FROM attempts WHERE job_id=? ORDER BY id DESC LIMIT 1`, jobID).Scan(&settingsSnapshot); err != nil {
			return nil, err
		}
	case AttemptRetranslate:
		if job.Status != StatusCompleted && (job.Status != StatusFailed || job.Checkpoint != StageTranscribed) {
			return nil, fmt.Errorf("%w: retranslation requires terminal job with transcription", ErrInvalidAction)
		}
		if job.SubtitlePath == "" {
			return nil, fmt.Errorf("%w: persisted transcription is missing", ErrInvalidAction)
		}
		file, err := os.Open(job.SubtitlePath)
		if err != nil {
			return nil, fmt.Errorf("%w: persisted transcription is unreadable: %w", ErrInvalidAction, err)
		}
		file.Close()
		start = StageTranslating
		cache = false
	default:
		return nil, fmt.Errorf("%w: unknown action", ErrInvalidAction)
	}
	id, createdAt, err := s.insertAttemptTx(tx, jobID, kind, 0, start, cache)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE attempts SET settings_snapshot=? WHERE id=?`, settingsSnapshot, id); err != nil {
		return nil, err
	}
	if kind != AttemptRetranslate || job.Status != StatusCompleted {
		if _, err := tx.Exec(`UPDATE jobs SET status=?, error='', failure_stage='', completed_at=0, updated_at=? WHERE id=?`, StatusPending, time.Now().UnixNano(), jobID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Attempt{ID: id, JobID: jobID, Kind: kind, Status: AttemptPending, StartStage: start, TranslationCacheEnabled: cache, SettingsSnapshot: settingsSnapshot, CreatedAt: createdAt}, nil
}

func nextStage(checkpoint Stage, light bool) Stage {
	switch checkpoint {
	case StagePrepared:
		return StageMoving
	case StageMoved:
		if light {
			return StageDelivering
		}
		return StageTranscribing
	case StageTranscribed:
		return StageTranslating
	case StageTranslated:
		return StageDelivering
	case StageDelivered:
		return StageDelivered
	default:
		return StagePreparing
	}
}

func (s *store) job(id string) (*Job, error) { return queryJob(s.db, id) }

func (s *store) jobByIdentity(signature, sourceKey string) (*Job, error) {
	return queryJobByIdentity(s.db, signature, sourceKey)
}

func queryJob(q interface{ QueryRow(string, ...any) *sql.Row }, id string) (*Job, error) {
	return scanJob(q.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE id=?`, id))
}

func queryJobByMediaID(q interface{ QueryRow(string, ...any) *sql.Row }, mediaID string) (*Job, error) {
	return scanJob(q.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE media_id=?`, mediaID))
}

func queryJobByIdentity(q interface{ QueryRow(string, ...any) *sql.Row }, signature, sourceKey string) (*Job, error) {
	if signature != "" {
		return scanJob(q.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE content_signature=? OR (content_signature='' AND source_key=?) ORDER BY CASE WHEN content_signature=? THEN 0 ELSE 1 END LIMIT 1`, signature, sourceKey, signature))
	}
	return scanJob(q.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE source_key=? LIMIT 1`, sourceKey))
}

func setAttempt(job *Job, attempt Attempt) {
	job.AttemptID = attempt.ID
	job.AttemptKind = attempt.Kind
	job.StartStage = attempt.StartStage
	job.AttemptStatus = attempt.Status
	job.AttemptStage = attempt.CurrentStage
	job.AttemptFailureStage = attempt.FailureStage
	job.AttemptError = attempt.Error
	job.TranslationCacheEnabled = attempt.TranslationCacheEnabled
	job.SettingsSnapshot = attempt.SettingsSnapshot
	job.Retries = attempt.RetryIndex
}

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	var job Job
	var created, updated, started, completed int64
	var light int
	err := row.Scan(&job.ID, &job.MediaID, &job.ContentSignature, &job.SourceKey, &job.SourcePath, &job.FileName, &job.TorrentName, &job.Category,
		&job.Status, &job.Error, &job.Retries, &created, &updated, &started, &completed,
		&light, &job.SubtitleDetectionReason, &job.SidecarSubtitlePath, &job.StagingPath,
		&job.ProcessingPath, &job.SubtitlePath, &job.TranslatedPath, &job.Checkpoint, &job.FailureStage,
		&job.TranscriptionSource)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	job.IsLight = light != 0
	job.CreatedAt = fromUnixNano(created)
	job.UpdatedAt = fromUnixNano(updated)
	job.StartedAt = fromUnixNano(started)
	job.CompletedAt = fromUnixNano(completed)
	return &job, nil
}

func (s *store) list(where string) ([]*Job, error) {
	rows, err := s.db.Query(`SELECT ` + jobColumns + ` FROM jobs ` + where + ` ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	var jobs []*Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := s.attachLatestAttempts(jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *store) listPage(before int64, beforeID string, limit int) ([]*Job, error) {
	query := `SELECT ` + jobColumns + ` FROM jobs`
	args := []any{}
	if beforeID != "" {
		query += ` WHERE created_at < ? OR (created_at = ? AND id < ?)`
		args = append(args, before, before, beforeID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var jobs []*Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := s.attachLatestAttempts(jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *store) attachLatestAttempts(jobs []*Job) error {
	byID := make(map[string]*Job, len(jobs))
	for _, job := range jobs {
		byID[job.ID] = job
	}
	const chunkSize = 500
	for start := 0; start < len(jobs); start += chunkSize {
		end := min(start+chunkSize, len(jobs))
		args := make([]any, 0, end-start)
		for _, job := range jobs[start:end] {
			args = append(args, job.ID)
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
		rows, err := s.db.Query(`SELECT id, job_id, kind, status, retry_index, start_stage, current_stage, failure_stage,
			error, translation_cache_enabled, settings_snapshot, created_at, started_at, completed_at
			FROM attempts WHERE id IN (SELECT MAX(id) FROM attempts WHERE job_id IN (`+placeholders+`) GROUP BY job_id)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			attempt, err := scanAttempt(rows)
			if err != nil {
				rows.Close()
				return err
			}
			if job := byID[attempt.JobID]; job != nil {
				setAttempt(job, attempt)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) attempts(jobID string) ([]Attempt, error) {
	rows, err := s.db.Query(`SELECT id, job_id, kind, status, retry_index, start_stage, current_stage, failure_stage,
		error, translation_cache_enabled, settings_snapshot, created_at, started_at, completed_at
		FROM attempts WHERE job_id=? ORDER BY id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attempts []Attempt
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

func scanAttempt(row interface{ Scan(...any) error }) (Attempt, error) {
	var attempt Attempt
	var cache int
	var created, started, completed int64
	err := row.Scan(&attempt.ID, &attempt.JobID, &attempt.Kind, &attempt.Status, &attempt.RetryIndex,
		&attempt.StartStage, &attempt.CurrentStage, &attempt.FailureStage, &attempt.Error, &cache,
		&attempt.SettingsSnapshot, &created, &started, &completed)
	attempt.TranslationCacheEnabled = cache != 0
	attempt.CreatedAt = fromUnixNano(created)
	attempt.StartedAt = fromUnixNano(started)
	attempt.CompletedAt = fromUnixNano(completed)
	return attempt, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func unixNano(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}

func fromUnixNano(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(0, value)
}
