package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/fusionn-muse/internal/mediaintake"
)

var ErrInvalidTaskAction = errors.New("invalid task action")

type TaskAction struct {
	Action             string `json:"action"`
	UseCurrentSettings bool   `json:"use_current_settings,omitempty"`
	OutputName         string `json:"output_name,omitempty"`
}

type TaskActionResult struct {
	TaskReference
	Attempt    *Attempt    `json:"attempt,omitempty"`
	Submission *Submission `json:"submission,omitempty"`
}

type TaskActionRecord struct {
	TaskAction
	PreviousOutputName string `json:"previous_output_name,omitempty"`
}

type attemptRequest struct {
	TaskActionRecord
	TranslatedPath string `json:"translated_path,omitempty"`
	Checkpoint     Stage  `json:"checkpoint,omitempty"`
	ReceiptPath    string `json:"receipt_path,omitempty"`
}

func (q *Queue) ExecuteTaskAction(ctx context.Context, kind, id string, request TaskAction) (*TaskActionResult, error) {
	if (kind != "job" && kind != "submission") || (request.Action != "retry" && request.Action != "resume" && request.Action != "retranslate") {
		return nil, ErrInvalidTaskAction
	}
	if kind == "submission" && (request.Action == "retranslate" || request.OutputName != "") {
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
	var current *string
	if request.UseCurrentSettings {
		settings, err := q.submissionSettings()
		if err != nil {
			return nil, err
		}
		current = &settings
	}
	return q.executeTaskAction(ctx, kind, id, request, current, 0)
}

func (q *Queue) executeTaskAction(ctx context.Context, kind, id string, request TaskAction, current *string, expectedExecutionID int64) (*TaskActionResult, error) {
	result := &TaskActionResult{TaskReference: TaskReference{Kind: kind, ID: id}}
	if kind == "submission" {
		ctx, cancel, err := q.submissionOperation(ctx)
		if err != nil {
			return nil, err
		}
		defer cancel()
		input, err := q.store.recoverSubmission(ctx, id, current, request.Action, expectedExecutionID)
		if err != nil {
			return nil, err
		}
		result.Submission = input
		q.wake(nil)
		return result, nil
	}
	planner, _ := q.processor.(outputPlanner)
	attempt, err := q.store.createTaskAction(ctx, id, request, current, planner, expectedExecutionID)
	if err != nil {
		return nil, err
	}
	result.ID = attempt.JobID
	result.Attempt = &attempt
	q.wake(nil)
	return result, nil
}

func checkSubmissionAction(ctx context.Context, tx *sql.Tx, input *Submission, action string) error {
	status := input.Status
	if status == SubmissionPending || status == SubmissionPreparing {
		err := tx.QueryRowContext(ctx, `SELECT status FROM submission_runs WHERE submission_id=? AND id<? ORDER BY id DESC LIMIT 1`, input.ID, input.RunID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s requires a terminal submission in the matching state", ErrInvalidSubmissionAction, action)
		}
		if err != nil {
			return err
		}
	}
	if (action == "retry" && (status == SubmissionFailed || status == SubmissionIgnored)) || (action == "resume" && status == SubmissionInterrupted) {
		return nil
	}
	return fmt.Errorf("%w: %s requires a terminal submission in the matching state", ErrInvalidSubmissionAction, action)
}

func (s *store) createTaskAction(ctx context.Context, id string, request TaskAction, current *string, planner outputPlanner, expectedExecutionID int64) (Attempt, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attempt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err = resolveJobReference(ctx, tx, id)
	if err != nil {
		return Attempt{}, err
	}
	job, err := queryJob(tx, id)
	if err != nil {
		return Attempt{}, err
	}
	latest, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT id,job_id,kind,status,retry_index,start_stage,current_stage,failure_stage,error,
		translation_cache_enabled,settings_snapshot,created_at,started_at,completed_at FROM attempts WHERE job_id=? ORDER BY id DESC LIMIT 1`, id))
	if err != nil {
		return Attempt{}, err
	}
	if expectedExecutionID != 0 && latest.ID != expectedExecutionID {
		return Attempt{}, fmt.Errorf("%w: latest attempt changed after bulk selection", ErrInvalidAction)
	}
	var encodedPrevious string
	if err := tx.QueryRowContext(ctx, `SELECT request_meta FROM attempts WHERE id=?`, latest.ID).Scan(&encodedPrevious); err != nil {
		return Attempt{}, err
	}
	var previous attemptRequest
	if err := json.Unmarshal([]byte(encodedPrevious), &previous); err != nil {
		return Attempt{}, err
	}
	invalidState := fmt.Errorf("%w: %s is not valid while latest attempt is %s", ErrInvalidAction, request.Action, latest.Status)
	if latest.Status == AttemptPending || latest.Status == AttemptProcessing {
		if previous.TaskAction != request || (current != nil && latest.SettingsSnapshot != *current) {
			return Attempt{}, invalidState
		}
		return latest, tx.Commit()
	}
	kind, start, cache := AttemptManual, nextStage(job.Checkpoint, job.IsLight), true
	switch request.Action {
	case "retry":
		if latest.Status != AttemptFailed {
			return Attempt{}, invalidState
		}
	case "resume":
		if latest.Status != AttemptInterrupted {
			return Attempt{}, invalidState
		}
		kind = AttemptResume
	case "retranslate":
		if job.IsLight || job.SubtitlePath == "" {
			return Attempt{}, invalidState
		}
		kind, start, cache = AttemptRetranslate, StageTranslating, false
	}
	if latest.Kind == AttemptRetranslate && request.Action != "retranslate" {
		kind, start = AttemptRetranslate, StageTranslating
	}
	if kind == AttemptRetranslate {
		info, err := os.Lstat(job.SubtitlePath)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return Attempt{}, invalidState
		}
	}
	settings := latest.SettingsSnapshot
	if current != nil {
		settings = *current
		cache = false
	}
	metadata := attemptRequest{TaskActionRecord: TaskActionRecord{TaskAction: request}}
	if kind == AttemptRetranslate && request.Action != "retranslate" && current == nil {
		metadata.TranslatedPath, metadata.Checkpoint, metadata.ReceiptPath = previous.TranslatedPath, previous.Checkpoint, previous.ReceiptPath
		if previous.Checkpoint == StageTranslated {
			start = StageDelivering
		}
		if previous.Checkpoint == StageDelivered && previous.ReceiptPath != "" {
			// Publication succeeded but its outcome did not commit. Recheck the
			// receipt's source before finalizing, including edits made while stopped.
			metadata.TranslatedPath, metadata.Checkpoint = previous.ReceiptPath, StageTranslated
			start = StageDelivering
		}
	}
	if latest.CurrentStage == StageTranslating && start == StageDelivering {
		start = StageTranslating
	}
	if request.OutputName != "" {
		if !safeOutputName(job.FileName, request.OutputName) {
			return Attempt{}, fmt.Errorf("%w: provide a safe filename with the original extension", ErrInvalidTaskAction)
		}
		if request.Action == "retranslate" || !canRenameJob(job) || planner == nil {
			return Attempt{}, fmt.Errorf("%w: output_name requires a failed/interrupted Job without a complete transcription", ErrInvalidTaskAction)
		}
		paths, err := planner.PlanOutputPaths(job, settings, request.OutputName)
		if err != nil {
			return Attempt{}, err
		}
		if err := claimArtifactsTx(ctx, tx, job, paths); err != nil {
			return Attempt{}, err
		}
		source := ""
		for _, candidate := range []string{job.SourcePath, job.StagingPath, job.ProcessingPath} {
			if candidate != "" && job.ValidateMedia(candidate) == nil {
				source = candidate
				break
			}
		}
		if source == "" {
			return Attempt{}, fmt.Errorf("%w: no verified media is available for naming repair", ErrArtifactUnavailable)
		}
		if request.OutputName != job.FileName {
			metadata.PreviousOutputName = job.FileName
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET file_name=?,source_path=?,staging_path='',processing_path='',subtitle_path='',translated_path='',checkpoint='',transcription_source='' WHERE id=?`, request.OutputName, source, id); err != nil {
			return Attempt{}, err
		}
		start = StagePreparing
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return Attempt{}, err
	}
	now := time.Now()
	row, err := tx.ExecContext(ctx, `INSERT INTO attempts(job_id,kind,status,start_stage,translation_cache_enabled,settings_snapshot,created_at,request_meta)
		VALUES(?,?,?,?,?,?,?,?)`, id, kind, AttemptPending, start, boolInt(cache), settings, now.UnixNano(), string(encoded))
	if err != nil {
		return Attempt{}, err
	}
	attemptID, err := row.LastInsertId()
	if err != nil {
		return Attempt{}, err
	}
	if job.Status != StatusCompleted || kind != AttemptRetranslate {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status=?,error='',failure_stage='',retries=0,completed_at=0,updated_at=? WHERE id=?`, StatusPending, now.UnixNano(), id); err != nil {
			return Attempt{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Attempt{}, err
	}
	return Attempt{ID: attemptID, JobID: id, Kind: kind, Status: AttemptPending, StartStage: start, TranslationCacheEnabled: cache,
		SettingsSnapshot: settings, CreatedAt: now}, nil
}

func loadAttemptWork(tx *sql.Tx, job *Job) error {
	var encoded string
	if err := tx.QueryRow(`SELECT request_meta FROM attempts WHERE id=?`, job.AttemptID).Scan(&encoded); err != nil {
		return err
	}
	var metadata attemptRequest
	if err := json.Unmarshal([]byte(encoded), &metadata); err != nil {
		return err
	}
	if job.AttemptKind == AttemptRetranslate && metadata.Action != "retranslate" {
		job.RecoveryTranslatedPath = metadata.TranslatedPath
	}
	return nil
}

type outputPlanner interface {
	PlanOutputPaths(*Job, string, string) ([]string, error)
}

func safeOutputName(original, name string) bool {
	return name != "" && len(name) <= 255 && name == strings.TrimSpace(name) && filepath.Base(name) == name &&
		!strings.ContainsAny(name, "/\\") && strings.IndexFunc(name, unicode.IsControl) < 0 &&
		filepath.Ext(name) != "" && strings.EqualFold(filepath.Ext(name), filepath.Ext(original)) &&
		mediaintake.CleanVideoFilename(name) == name
}

func canRenameJob(job *Job) bool {
	if job.ContentSignature == "" || job.TranscriptionSource != "" || job.Checkpoint == StageTranscribed || job.Checkpoint == StageTranslated || job.Checkpoint == StageDelivered {
		return false
	}
	if job.SubtitlePath == "" {
		return true
	}
	info, err := os.Lstat(job.SubtitlePath)
	return errors.Is(err, os.ErrNotExist) || (err == nil && info.Mode().IsRegular() && info.Size() == 0)
}
