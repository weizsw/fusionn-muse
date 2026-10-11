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

var ErrInvalidTaskQuery = errors.New("invalid task query")

// Task is an observation of a Job or an unresolved Submission, not a new identity.
// Status describes the latest execution; JobStatus describes the retained result.
type Task struct {
	Kind             string    `json:"kind"`
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	SourcePath       string    `json:"source_path"`
	Status           string    `json:"status"`
	JobStatus        JobStatus `json:"job_status,omitempty"`
	Stage            Stage     `json:"stage,omitempty"`
	Error            string    `json:"error,omitempty"`
	PersistenceError string    `json:"persistence_error,omitempty"`
	ExecutionID      int64     `json:"execution_id"`
	ExecutionKind    string    `json:"execution_kind"`
	IsLight          bool      `json:"is_light"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type TaskFilter struct {
	Kind, Status, Cursor string
	Limit                int
}

type TaskPage struct {
	Tasks      []*Task `json:"tasks"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

type TaskStatistics struct {
	Total    int            `json:"total"`
	Statuses map[string]int `json:"statuses"`
}

// Both list and statistics use this exact projection. No settings or subtitle
// content is read, and resolved submissions do not duplicate their linked Job.
const taskRows = `SELECT 'job' AS kind, j.id, j.file_name AS name, j.source_path,
	COALESCE(a.status,j.status) AS status, j.status AS job_status,
	CASE WHEN a.status='completed' THEN j.checkpoint ELSE COALESCE(NULLIF(a.failure_stage,''),NULLIF(a.current_stage,''),a.start_stage,j.checkpoint,'') END AS stage,
	COALESCE(a.error,j.error) AS error, COALESCE(a.id,0) AS execution_id,
	COALESCE(a.kind,'') AS execution_kind, j.is_light, j.created_at, j.updated_at
	FROM jobs j LEFT JOIN attempts a ON a.id=(SELECT MAX(id) FROM attempts WHERE job_id=j.id)
	UNION ALL
	SELECT 'submission', s.id, CASE WHEN s.torrent_name='' THEN s.source_path ELSE s.torrent_name END,
	s.source_path, CASE WHEN r.status='preparing' THEN 'processing' ELSE r.status END, '',
	'preparing', r.error, r.id, 'preparation', 0, s.created_at,
	MAX(r.created_at,r.started_at,r.completed_at)
	FROM submissions s JOIN submission_runs r ON r.id=(SELECT MAX(id) FROM submission_runs WHERE submission_id=s.id)
	WHERE r.job_id IS NULL`

const taskFields = `kind,id,name,source_path,status,job_status,stage,error,execution_id,execution_kind,is_light,created_at,updated_at`

type taskCursor struct {
	Created      int64  `json:"created"`
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	FilterKind   string `json:"filter_kind"`
	FilterStatus string `json:"filter_status"`
}

func validateTaskFilter(filter TaskFilter) error {
	switch filter.Kind {
	case "", "job", "submission":
	default:
		return fmt.Errorf("%w: unknown kind", ErrInvalidTaskQuery)
	}
	switch filter.Status {
	case "", "pending", "processing", "completed", "failed", "interrupted", "ignored":
	default:
		return fmt.Errorf("%w: unknown status", ErrInvalidTaskQuery)
	}
	return nil
}

func taskLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	return min(limit, 200)
}

func decodeTaskCursor(value string, filter TaskFilter) (taskCursor, error) {
	var cursor taskCursor
	if value == "" {
		return cursor, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if len(value) > 2048 || err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Created <= 0 || cursor.ID == "" ||
		(cursor.Kind != "job" && cursor.Kind != "submission") || cursor.FilterKind != filter.Kind || cursor.FilterStatus != filter.Status {
		return cursor, fmt.Errorf("%w: invalid cursor or changed filter", ErrInvalidTaskQuery)
	}
	return cursor, nil
}

func encodeTaskCursor(task *Task, filter TaskFilter) string {
	value, _ := json.Marshal(taskCursor{Created: task.CreatedAt.UnixNano(), Kind: task.Kind, ID: task.ID, FilterKind: filter.Kind, FilterStatus: filter.Status})
	return base64.RawURLEncoding.EncodeToString(value)
}

// ListTasks uses stable creation-time keyset pagination over both kinds.
func (q *Queue) ListTasks(ctx context.Context, filter TaskFilter) (TaskPage, error) {
	page := TaskPage{Tasks: []*Task{}}
	if err := validateTaskFilter(filter); err != nil {
		return page, err
	}
	cursor, err := decodeTaskCursor(filter.Cursor, filter)
	if err != nil {
		return page, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	query := `WITH task_rows AS (` + taskRows + `) SELECT ` + taskFields + ` FROM task_rows WHERE 1=1`
	args := []any{}
	if filter.Kind != "" {
		query += ` AND kind=?`
		args = append(args, filter.Kind)
	}
	if filter.Status != "" {
		query += ` AND status=?`
		args = append(args, filter.Status)
	}
	if cursor.ID != "" {
		query += ` AND (created_at<? OR (created_at=? AND (kind<? OR (kind=? AND id<?))))`
		args = append(args, cursor.Created, cursor.Created, cursor.Kind, cursor.Kind, cursor.ID)
	}
	limit := taskLimit(filter.Limit)
	query += ` ORDER BY created_at DESC,kind DESC,id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := q.store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return page, err
		}
		if task.Kind == "job" {
			job := &Job{ID: task.ID}
			q.attachPersistenceError(job)
			task.PersistenceError = job.PersistenceError
		}
		page.Tasks = append(page.Tasks, task)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Tasks) > limit {
		page.Tasks = page.Tasks[:limit]
		page.NextCursor = encodeTaskCursor(page.Tasks[limit-1], filter)
	}
	return page, nil
}

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var task Task
	var light int
	var created, updated int64
	err := row.Scan(&task.Kind, &task.ID, &task.Name, &task.SourcePath, &task.Status, &task.JobStatus, &task.Stage,
		&task.Error, &task.ExecutionID, &task.ExecutionKind, &light, &created, &updated)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	task.IsLight = light != 0
	task.CreatedAt, task.UpdatedAt = fromUnixNano(created), fromUnixNano(updated)
	return &task, nil
}

// TaskStats aggregates in SQLite, rather than loading all Job snapshots.
func (q *Queue) TaskStats(ctx context.Context, filter TaskFilter) (TaskStatistics, error) {
	stats := TaskStatistics{Statuses: map[string]int{"pending": 0, "processing": 0, "completed": 0, "failed": 0, "interrupted": 0, "ignored": 0}}
	if err := validateTaskFilter(filter); err != nil {
		return stats, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	query := `WITH task_rows AS (` + taskRows + `) SELECT status,COUNT(*) FROM task_rows WHERE 1=1`
	args := []any{}
	if filter.Kind != "" {
		query += ` AND kind=?`
		args = append(args, filter.Kind)
	}
	if filter.Status != "" {
		query += ` AND status=?`
		args = append(args, filter.Status)
	}
	rows, err := q.store.db.QueryContext(ctx, query+` GROUP BY status`, args...)
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return stats, err
		}
		stats.Statuses[status] = count
		stats.Total += count
	}
	return stats, rows.Err()
}
