package queue

import (
	"errors"
	"time"
)

// SubmissionStatus describes preparation before a Submission is linked to a Job.
type SubmissionStatus string

const (
	SubmissionPending     SubmissionStatus = "pending"
	SubmissionPreparing   SubmissionStatus = "preparing"
	SubmissionResolved    SubmissionStatus = "resolved"
	SubmissionFailed      SubmissionStatus = "failed"
	SubmissionInterrupted SubmissionStatus = "interrupted"
	SubmissionIgnored     SubmissionStatus = "ignored"
)

var (
	ErrSubmissionNotFound      = errors.New("submission not found")
	ErrInvalidSubmissionAction = errors.New("action is not valid for submission state")
)

// Submission preserves an input even when no Media item can yet be resolved.
type Submission struct {
	ID          string           `json:"id"`
	SourcePath  string           `json:"source_path"`
	TorrentName string           `json:"torrent_name"`
	Category    string           `json:"category"`
	Status      SubmissionStatus `json:"status"`
	Error       string           `json:"error,omitempty"`
	JobID       string           `json:"job_id,omitempty"`
	JobStatus   JobStatus        `json:"job_status,omitempty"`
	RunID       int64            `json:"run_id"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	StartedAt   time.Time        `json:"started_at,omitempty"`
	CompletedAt time.Time        `json:"completed_at,omitempty"`

	SettingsSnapshot string `json:"-"`
}

// SubmissionRun records one preparation without changing the submitted input.
type SubmissionRun struct {
	ID               int64            `json:"id"`
	SubmissionID     string           `json:"submission_id"`
	Status           SubmissionStatus `json:"status"`
	Error            string           `json:"error,omitempty"`
	JobID            string           `json:"job_id,omitempty"`
	SettingsSnapshot string           `json:"settings_snapshot,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	StartedAt        time.Time        `json:"started_at,omitempty"`
	CompletedAt      time.Time        `json:"completed_at,omitempty"`
}

// SubmissionDetail separates preparation history and settings from list rows.
type SubmissionDetail struct {
	Submission
	Runs []SubmissionRun `json:"runs"`
}
