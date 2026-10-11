package queue

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type TaskReference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type TaskDetail struct {
	Task
	Job                 *Job               `json:"job,omitempty"`
	Submission          *Submission        `json:"submission,omitempty"`
	CanonicalTask       *TaskReference     `json:"canonical_task,omitempty"`
	AllowedActions      []string           `json:"allowed_actions"`
	CanChangeOutputName bool               `json:"can_change_output_name"`
	History             []TaskHistoryEntry `json:"history"`
	NextHistoryCursor   string             `json:"next_history_cursor,omitempty"`
}

type TaskHistoryEntry struct {
	Kind             string            `json:"kind"`
	ID               int64             `json:"id"`
	SubmissionID     string            `json:"submission_id,omitempty"`
	ExecutionKind    string            `json:"execution_kind"`
	Status           string            `json:"status"`
	StartStage       Stage             `json:"start_stage,omitempty"`
	CurrentStage     Stage             `json:"current_stage,omitempty"`
	FailureStage     Stage             `json:"failure_stage,omitempty"`
	Error            string            `json:"error,omitempty"`
	Request          *TaskActionRecord `json:"request,omitempty"`
	SettingsSnapshot string            `json:"settings_snapshot,omitempty"`
	SourcePath       string            `json:"source_path,omitempty"`
	TorrentName      string            `json:"torrent_name,omitempty"`
	Category         string            `json:"category,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	StartedAt        time.Time         `json:"started_at,omitempty"`
	CompletedAt      time.Time         `json:"completed_at,omitempty"`
}

const attemptHistory = `SELECT 'attempt' AS kind,a.id,'' AS submission_id,a.kind AS execution_kind,a.status,
	a.start_stage,a.current_stage,a.failure_stage,a.error,a.settings_snapshot,'' AS source_path,'' AS torrent_name,'' AS category,
	a.created_at,a.started_at,a.completed_at,a.request_meta FROM attempts a WHERE a.job_id=?`
const preparationHistory = `SELECT 'preparation' AS kind,r.id,s.id,'preparation',r.status,
	'','preparing',CASE WHEN r.status='failed' THEN 'preparing' ELSE '' END,r.error,r.settings_snapshot,s.source_path,s.torrent_name,s.category,
	r.created_at,r.started_at,r.completed_at,'' AS request_meta FROM submission_runs r JOIN submissions s ON s.id=r.submission_id`

// GetTaskDetail observes metadata and a bounded history page in one read transaction.
func (q *Queue) GetTaskDetail(ctx context.Context, kind, id, cursor string, limit int) (*TaskDetail, error) {
	if kind != "job" && kind != "submission" {
		return nil, fmt.Errorf("%w: unknown kind", ErrInvalidTaskQuery)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := q.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	detail := &TaskDetail{AllowedActions: []string{}, History: []TaskHistoryEntry{}}
	if kind == "job" {
		id, err = resolveJobReference(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		task, err := scanTask(tx.QueryRowContext(ctx, `WITH task_rows AS (`+taskRows+`) SELECT `+taskFields+` FROM task_rows WHERE kind='job' AND id=?`, id))
		if err != nil {
			return nil, err
		}
		detail.Task = *task
		job, err := queryJob(tx, id)
		if err != nil {
			return nil, err
		}
		attempt, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT id,job_id,kind,status,retry_index,start_stage,current_stage,failure_stage,error,
			translation_cache_enabled,settings_snapshot,created_at,started_at,completed_at FROM attempts WHERE job_id=? ORDER BY id DESC LIMIT 1`, id))
		if err == nil {
			setAttempt(job, attempt)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		job.SettingsSnapshot = "" // Configurations belong to the paginated history, not another duplicate.
		detail.Job = job
		detail.AllowedActions = jobTaskActions(job)

	} else {
		submission, err := querySubmission(ctx, tx, id)
		if err != nil {
			if errors.Is(err, ErrSubmissionNotFound) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		detail.Submission = submission
		detail.Task = Task{Kind: kind, ID: id, Name: submission.TorrentName, SourcePath: submission.SourcePath,
			Status: string(submission.Status), Stage: StagePreparing, Error: submission.Error, ExecutionID: submission.RunID,
			ExecutionKind: "preparation", CreatedAt: submission.CreatedAt, UpdatedAt: submission.UpdatedAt}
		if submission.Status == SubmissionPreparing {
			detail.Status = "processing"
		}
		switch submission.Status {
		case SubmissionFailed, SubmissionIgnored:
			detail.AllowedActions = []string{"retry"}
		case SubmissionInterrupted:
			detail.AllowedActions = []string{"resume"}
		case SubmissionResolved:
			detail.CanonicalTask = &TaskReference{Kind: "job", ID: submission.JobID}
			task, err := scanTask(tx.QueryRowContext(ctx, `WITH task_rows AS (`+taskRows+`) SELECT `+taskFields+` FROM task_rows WHERE kind='job' AND id=?`, submission.JobID))
			if err != nil {
				return nil, err
			}
			detail.Status, detail.JobStatus, detail.Stage = task.Status, task.JobStatus, task.Stage
			detail.Error, detail.ExecutionID, detail.ExecutionKind, detail.UpdatedAt = task.Error, task.ExecutionID, task.ExecutionKind, task.UpdatedAt
			linked := &Job{ID: submission.JobID, AttemptID: task.ExecutionID}
			q.attachPersistenceError(linked)
			detail.PersistenceError = linked.PersistenceError
		}
	}
	history, next, err := queryTaskHistory(ctx, tx, kind, id, cursor, limit)
	if err != nil {
		return nil, err
	}
	detail.History, detail.NextHistoryCursor = history, next
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if kind == "job" {
		_, hasPlanner := q.processor.(outputPlanner)
		detail.CanChangeOutputName = hasPlanner && (detail.Status == "failed" || detail.Status == "interrupted") && detail.Job.Status != StatusCompleted && canRenameJob(detail.Job)
		q.attachPersistenceError(detail.Job)
		detail.PersistenceError = detail.Job.PersistenceError
	}
	return detail, nil
}

func jobTaskActions(job *Job) []string {
	if job.AttemptStatus == AttemptPending || job.AttemptStatus == AttemptProcessing {
		return []string{}
	}
	var actions []string
	switch job.AttemptStatus {
	case AttemptFailed:
		actions = append(actions, "retry")
	case AttemptInterrupted:
		actions = append(actions, "resume")
	}
	if job.SubtitlePath != "" && (job.Status == StatusCompleted || job.Status == StatusFailed && job.Checkpoint == StageTranscribed) {
		actions = append(actions, "retranslate")
	}
	if actions == nil {
		return []string{}
	}
	return actions
}

type historyCursor struct {
	Created  int64  `json:"created"`
	Kind     string `json:"kind"`
	ID       int64  `json:"id"`
	TaskKind string `json:"task_kind"`
	TaskID   string `json:"task_id"`
}

func queryTaskHistory(ctx context.Context, tx *sql.Tx, kind, id, value string, limit int) ([]TaskHistoryEntry, string, error) {
	var cursor historyCursor
	if value != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if len(value) > 2048 || err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Created <= 0 || cursor.ID <= 0 ||
			(cursor.Kind != "attempt" && cursor.Kind != "preparation") || cursor.TaskKind != kind || cursor.TaskID != id {
			return nil, "", fmt.Errorf("%w: invalid history cursor", ErrInvalidTaskQuery)
		}
	}
	rowsQuery := preparationHistory + ` WHERE s.id=?`
	args := []any{id}
	if kind == "job" {
		rowsQuery = attemptHistory + ` UNION ALL ` + preparationHistory + ` WHERE EXISTS(SELECT 1 FROM submission_runs linked WHERE linked.submission_id=s.id AND linked.job_id=?)`
		args = []any{id, id}
	}
	query := `WITH history AS (` + rowsQuery + `) SELECT * FROM history WHERE 1=1`
	if cursor.ID != 0 {
		query += ` AND (created_at<? OR (created_at=? AND (kind<? OR (kind=? AND id<?))))`
		args = append(args, cursor.Created, cursor.Created, cursor.Kind, cursor.Kind, cursor.ID)
	}
	limit = taskLimit(limit)
	query += ` ORDER BY created_at DESC,kind DESC,id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	history := []TaskHistoryEntry{}
	for rows.Next() {
		var entry TaskHistoryEntry
		var created, started, completed int64
		var request string
		if err := rows.Scan(&entry.Kind, &entry.ID, &entry.SubmissionID, &entry.ExecutionKind, &entry.Status, &entry.StartStage,
			&entry.CurrentStage, &entry.FailureStage, &entry.Error, &entry.SettingsSnapshot, &entry.SourcePath, &entry.TorrentName,
			&entry.Category, &created, &started, &completed, &request); err != nil {
			return nil, "", err
		}
		if request != "" && request != "{}" {
			entry.Request = &TaskActionRecord{}
			if err := json.Unmarshal([]byte(request), entry.Request); err != nil {
				return nil, "", err
			}
			if entry.Request.Action == "" {
				entry.Request = nil
			}
		}
		entry.CreatedAt, entry.StartedAt, entry.CompletedAt = fromUnixNano(created), fromUnixNano(started), fromUnixNano(completed)
		history = append(history, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var next string
	if len(history) > limit {
		history = history[:limit]
		last := history[limit-1]
		encoded, _ := json.Marshal(historyCursor{Created: last.CreatedAt.UnixNano(), Kind: last.Kind, ID: last.ID, TaskKind: kind, TaskID: id})
		next = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return history, next, nil
}
