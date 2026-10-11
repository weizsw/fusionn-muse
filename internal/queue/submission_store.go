package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const submissionSchema = `
CREATE TABLE IF NOT EXISTS submissions (
	id TEXT PRIMARY KEY,
	source_path TEXT NOT NULL,
	torrent_name TEXT NOT NULL DEFAULT '',
	category TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS submission_runs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	submission_id TEXT NOT NULL REFERENCES submissions(id),
	status TEXT NOT NULL,
	error TEXT NOT NULL DEFAULT '',
	job_id TEXT REFERENCES jobs(id),
	settings_snapshot TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	started_at INTEGER NOT NULL DEFAULT 0,
	completed_at INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS submission_runs_one_active
	ON submission_runs(submission_id) WHERE status IN ('pending', 'preparing');
CREATE INDEX IF NOT EXISTS submission_runs_pending ON submission_runs(status, id);
CREATE INDEX IF NOT EXISTS submission_runs_history ON submission_runs(submission_id, id);
CREATE INDEX IF NOT EXISTS submissions_history ON submissions(created_at DESC, id DESC);
`

const submissionColumns = `s.id, s.source_path, s.torrent_name, s.category, s.created_at,
	r.id, r.status, r.error, COALESCE(r.job_id,''), COALESCE(j.status,''), r.settings_snapshot,
	r.created_at, r.started_at, r.completed_at`

const submissionFrom = ` FROM submissions s
	JOIN submission_runs r ON r.id=(SELECT MAX(id) FROM submission_runs WHERE submission_id=s.id)
	LEFT JOIN jobs j ON j.id=r.job_id`

func (s *store) insertSubmission(ctx context.Context, input *Submission, snapshot string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixNano()
	if _, err := tx.ExecContext(ctx, `INSERT INTO submissions (id, source_path, torrent_name, category, created_at)
		VALUES (?,?,?,?,?)`, input.ID, input.SourcePath, input.TorrentName, input.Category, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO submission_runs (submission_id, status, settings_snapshot, created_at)
		VALUES (?,?,?,?)`, input.ID, SubmissionPending, snapshot, now)
	if err != nil {
		return err
	}
	runID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	input.Status, input.RunID, input.SettingsSnapshot = SubmissionPending, runID, snapshot
	input.CreatedAt, input.UpdatedAt = fromUnixNano(now), fromUnixNano(now)
	return nil
}

func (s *store) submission(id string) (*Submission, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return querySubmission(ctx, s.db, id)
}

func querySubmission(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (*Submission, error) {
	return scanSubmission(q.QueryRowContext(ctx, `SELECT `+submissionColumns+submissionFrom+` WHERE s.id=?`, id))
}

func (s *store) submissionDetail(ctx context.Context, id string) (*SubmissionDetail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	submission, err := querySubmission(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	runs, err := querySubmissionRuns(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &SubmissionDetail{Submission: *submission, Runs: runs}, nil
}

func scanSubmission(row interface{ Scan(...any) error }) (*Submission, error) {
	var submission Submission
	var created, runCreated, started, completed int64
	err := row.Scan(&submission.ID, &submission.SourcePath, &submission.TorrentName, &submission.Category, &created,
		&submission.RunID, &submission.Status, &submission.Error, &submission.JobID, &submission.JobStatus,
		&submission.SettingsSnapshot, &runCreated, &started, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSubmissionNotFound
	}
	if err != nil {
		return nil, err
	}
	submission.CreatedAt = fromUnixNano(created)
	submission.StartedAt = fromUnixNano(started)
	submission.CompletedAt = fromUnixNano(completed)
	submission.UpdatedAt = fromUnixNano(max(runCreated, started, completed))
	return &submission, nil
}

func (s *store) claimSubmission(ctx context.Context) (*Submission, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT submission_id FROM submission_runs WHERE status=? ORDER BY id LIMIT 1`, SubmissionPending).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	submission, err := querySubmission(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if _, err := tx.ExecContext(ctx, `UPDATE submission_runs SET status=?, started_at=? WHERE id=? AND status=?`,
		SubmissionPreparing, now.UnixNano(), submission.RunID, SubmissionPending); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	submission.Status = SubmissionPreparing
	submission.StartedAt, submission.UpdatedAt = now, now
	return submission, nil
}

func (s *store) finishSubmission(ctx context.Context, submission *Submission, status SubmissionStatus, message string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE submission_runs SET status=?, error=?, completed_at=?
		WHERE id=? AND submission_id=? AND status=?`, status, message, time.Now().UnixNano(), submission.RunID, submission.ID, SubmissionPreparing)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		var savedStatus SubmissionStatus
		var savedError string
		if err := s.db.QueryRowContext(ctx, `SELECT status, error FROM submission_runs WHERE id=? AND submission_id=?`,
			submission.RunID, submission.ID).Scan(&savedStatus, &savedError); err == nil && savedStatus == status && savedError == message {
			return nil
		}
		return fmt.Errorf("%w: preparation is no longer active", ErrInvalidSubmissionAction)
	}
	return nil
}

// resolveSubmission commits the Job, its first Attempt and the Submission link together.
func (s *store) resolveSubmission(ctx context.Context, submission *Submission, job *Job) (*Job, error) {
	prepareStoredJob(job)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := querySubmission(ctx, tx, submission.ID)
	if err != nil {
		return nil, err
	}
	if current.RunID != submission.RunID {
		return nil, fmt.Errorf("%w: preparation is no longer active", ErrInvalidSubmissionAction)
	}
	if current.Status == SubmissionResolved {
		linked, err := queryJob(tx, current.JobID)
		if err != nil {
			return nil, err
		}
		attempt, err := scanAttempt(tx.QueryRow(`SELECT id, job_id, kind, status, retry_index, start_stage, current_stage, failure_stage,
			error, translation_cache_enabled, settings_snapshot, created_at, started_at, completed_at
			FROM attempts WHERE job_id=? ORDER BY id DESC LIMIT 1`, linked.ID))
		if err != nil {
			return nil, err
		}
		setAttempt(linked, attempt)
		return linked, nil
	}
	if current.Status != SubmissionPreparing {
		return nil, fmt.Errorf("%w: preparation is no longer active", ErrInvalidSubmissionAction)
	}
	job.SettingsSnapshot = current.SettingsSnapshot
	linked, err := queryJobByIdentity(tx, job.ContentSignature, job.SourceKey)
	if errors.Is(err, ErrNotFound) {
		if err := s.insertJobTx(tx, job, AttemptProcessing); err != nil {
			return nil, err
		}
		linked = job
	} else if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE submission_runs SET status=?, job_id=?, completed_at=? WHERE id=?`,
		SubmissionResolved, linked.ID, time.Now().UnixNano(), submission.RunID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return linked, nil
}

func (s *store) recoverSubmission(ctx context.Context, id string, currentSettings *string, action string) (*Submission, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	submission, err := querySubmission(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if action != "" {
		if err := checkSubmissionAction(ctx, tx, submission, action); err != nil {
			return nil, err
		}
	}
	switch submission.Status {
	case SubmissionPending, SubmissionPreparing:
		if currentSettings != nil && *currentSettings != submission.SettingsSnapshot {
			return nil, fmt.Errorf("%w: another settings profile is active", ErrInvalidSubmissionAction)
		}
		return submission, nil
	case SubmissionFailed, SubmissionIgnored, SubmissionInterrupted:
	default:
		return nil, fmt.Errorf("%w: resolved submissions use Job recovery", ErrInvalidSubmissionAction)
	}
	settings := submission.SettingsSnapshot
	if currentSettings != nil {
		settings = *currentSettings
	}
	now := time.Now()
	result, err := tx.ExecContext(ctx, `INSERT INTO submission_runs (submission_id, status, settings_snapshot, created_at) VALUES (?,?,?,?)`,
		id, SubmissionPending, settings, now.UnixNano())
	if err != nil {
		return nil, err
	}
	runID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	submission.Status, submission.Error, submission.RunID = SubmissionPending, "", runID
	submission.SettingsSnapshot, submission.UpdatedAt = settings, now
	submission.StartedAt, submission.CompletedAt = time.Time{}, time.Time{}
	return submission, nil
}

func (s *store) submissionRuns(id string) ([]SubmissionRun, error) {
	return querySubmissionRuns(context.Background(), s.db, id)
}

func querySubmissionRuns(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, id string) ([]SubmissionRun, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, submission_id, status, error, COALESCE(job_id,''), settings_snapshot,
		created_at, started_at, completed_at FROM submission_runs WHERE submission_id=? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runs := []SubmissionRun{}
	for rows.Next() {
		var run SubmissionRun
		var created, started, completed int64
		if err := rows.Scan(&run.ID, &run.SubmissionID, &run.Status, &run.Error, &run.JobID, &run.SettingsSnapshot, &created, &started, &completed); err != nil {
			return nil, err
		}
		run.CreatedAt, run.StartedAt, run.CompletedAt = fromUnixNano(created), fromUnixNano(started), fromUnixNano(completed)
		runs = append(runs, run)
	}
	return runs, rows.Err()
}
