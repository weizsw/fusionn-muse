package queue

import (
	"context"
	"fmt"
	"time"
)

// BulkTaskAction recovers all tasks in the action's matching terminal state.
type BulkTaskAction struct {
	Action             string `json:"action"`
	UseCurrentSettings bool   `json:"use_current_settings,omitempty"`
}

type BulkTaskActionItem struct {
	TaskReference
	ExecutionID int64  `json:"execution_id,omitempty"`
	Error       string `json:"error,omitempty"`
}

type BulkTaskActionResult struct {
	Action   string               `json:"action"`
	Matched  int                  `json:"matched"`
	Accepted int                  `json:"accepted"`
	Failed   int                  `json:"failed"`
	Results  []BulkTaskActionItem `json:"results"`
}

type bulkTaskTarget struct {
	TaskReference
	ExecutionID int64
}

// ExecuteBulkTaskAction fixes the target executions before admitting any work.
// Each recovery commits independently and keeps the single-task action rules.
func (q *Queue) ExecuteBulkTaskAction(ctx context.Context, request BulkTaskAction) (*BulkTaskActionResult, error) {
	var status string
	switch request.Action {
	case "retry":
		status = "failed"
	case "resume":
		status = "interrupted"
	default:
		return nil, ErrInvalidTaskAction
	}
	q.mu.Lock()
	if q.state != queueRunning {
		q.mu.Unlock()
		return nil, ErrQueueNotRunning
	}
	q.wg.Add(1)
	q.mu.Unlock()
	defer q.wg.Done()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(q.ctx, cancel)
	defer func() { stop(); cancel() }()

	var current *string
	if request.UseCurrentSettings {
		settings, err := q.submissionSettings()
		if err != nil {
			return nil, err
		}
		current = &settings
	}
	targets, err := q.store.bulkTaskTargets(ctx, status)
	if err != nil {
		return nil, err
	}
	result := &BulkTaskActionResult{Action: request.Action, Matched: len(targets), Results: []BulkTaskActionItem{}}
	action := TaskAction{Action: request.Action, UseCurrentSettings: request.UseCurrentSettings}
	for _, target := range targets {
		item := BulkTaskActionItem{TaskReference: target.TaskReference}
		itemCtx, itemCancel := context.WithTimeout(ctx, 5*time.Second)
		recovered, err := q.executeTaskAction(itemCtx, target.Kind, target.ID, action, current, target.ExecutionID)
		itemCancel()
		if err != nil {
			item.Error = err.Error()
			result.Failed++
		} else {
			if recovered.Attempt != nil {
				item.ExecutionID = recovered.Attempt.ID
			} else {
				item.ExecutionID = recovered.Submission.RunID
			}
			result.Accepted++
		}
		result.Results = append(result.Results, item)
	}
	return result, nil
}

func (s *store) bulkTaskTargets(ctx context.Context, status string) ([]bulkTaskTarget, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `WITH task_rows AS (`+taskRows+`) SELECT kind,id,execution_id
		FROM task_rows WHERE status=? ORDER BY created_at DESC,kind DESC,id DESC`, status)
	if err != nil {
		return nil, fmt.Errorf("select bulk task targets: %w", err)
	}
	defer rows.Close()
	var targets []bulkTaskTarget
	for rows.Next() {
		var target bulkTaskTarget
		if err := rows.Scan(&target.Kind, &target.ID, &target.ExecutionID); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}
