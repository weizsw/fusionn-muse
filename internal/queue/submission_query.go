package queue

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ListSubmissions returns lightweight, newest-first inputs in every state.
// Settings and Preparation run history are available only through detail.
func (q *Queue) ListSubmissions(ctx context.Context, cursor string, limit int, status SubmissionStatus) ([]*Submission, string, error) {
	switch status {
	case "", SubmissionPending, SubmissionPreparing, SubmissionResolved, SubmissionFailed, SubmissionInterrupted, SubmissionIgnored:
	default:
		return nil, "", fmt.Errorf("%w: unknown status", ErrInvalidSubmission)
	}
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}
	var before int64
	var beforeID string
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid cursor", ErrInvalidSubmission)
		}
		value, id, ok := strings.Cut(string(decoded), ":")
		if !ok || id == "" {
			return nil, "", fmt.Errorf("%w: invalid cursor", ErrInvalidSubmission)
		}
		before, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid cursor", ErrInvalidSubmission)
		}
		beforeID = id
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Do not load the potentially large snapshot just to remove it from JSON.
	query := `SELECT ` + strings.Replace(submissionColumns, "r.settings_snapshot", "''", 1) + submissionFrom + ` WHERE 1=1`
	args := []any{}
	if beforeID != "" {
		query += ` AND (s.created_at < ? OR (s.created_at = ? AND s.id < ?))`
		args = append(args, before, before, beforeID)
	}
	if status != "" {
		query += ` AND r.status=?`
		args = append(args, status)
	}
	query += ` ORDER BY s.created_at DESC, s.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := q.store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	submissions := []*Submission{}
	for rows.Next() {
		input, err := scanSubmission(rows)
		if err != nil {
			return nil, "", err
		}
		submissions = append(submissions, input)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(submissions) <= limit {
		return submissions, "", nil
	}
	submissions = submissions[:limit]
	last := submissions[len(submissions)-1]
	next := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%s", last.CreatedAt.UnixNano(), last.ID)))
	return submissions, next, nil
}
