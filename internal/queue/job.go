package queue

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fusionn-muse/internal/mediaintake"
)

// JobStatus represents the durable state of a Job.
type JobStatus string

const (
	StatusPending     JobStatus = "pending"
	StatusProcessing  JobStatus = "processing"
	StatusCompleted   JobStatus = "completed"
	StatusFailed      JobStatus = "failed"
	StatusInterrupted JobStatus = "interrupted"
)

// Stage identifies a durable processing checkpoint or stage in progress.

type Stage string

const (
	StagePreparing    Stage = "preparing"
	StagePrepared     Stage = "prepared"
	StageMoving       Stage = "moving"
	StageMoved        Stage = "moved"
	StageTranscribing Stage = "transcribing"
	StageTranscribed  Stage = "transcribed"
	StageTranslating  Stage = "translating"
	StageTranslated   Stage = "translated"
	StageDelivering   Stage = "delivering"
	StageDelivered    Stage = "delivered"
)

// AttemptStatus is the durable lifecycle state of an Attempt.

type AttemptStatus string

const (
	AttemptPending     AttemptStatus = "pending"
	AttemptProcessing  AttemptStatus = "processing"
	AttemptCompleted   AttemptStatus = "completed"
	AttemptFailed      AttemptStatus = "failed"
	AttemptInterrupted AttemptStatus = "interrupted"
)

// AttemptKind identifies why an Attempt was created.

type AttemptKind string

const (
	AttemptInitial     AttemptKind = "initial"
	AttemptAutomatic   AttemptKind = "automatic_retry"
	AttemptManual      AttemptKind = "manual_retry"
	AttemptResume      AttemptKind = "resume"
	AttemptRetranslate AttemptKind = "retranslation"
)

// TranscriptionGenerated marks a transcription created by the configured transcriber.

const TranscriptionGenerated = "generated"

// Attempt records one execution of a durable Job.

type Attempt struct {
	ID                      int64         `json:"id"`
	JobID                   string        `json:"job_id"`
	Kind                    AttemptKind   `json:"kind"`
	Status                  AttemptStatus `json:"status"`
	RetryIndex              int           `json:"retry_index"`
	StartStage              Stage         `json:"start_stage"`
	CurrentStage            Stage         `json:"current_stage,omitempty"`
	FailureStage            Stage         `json:"failure_stage,omitempty"`
	Error                   string        `json:"error,omitempty"`
	TranslationCacheEnabled bool          `json:"translation_cache_enabled"`
	SettingsSnapshot        string        `json:"settings_snapshot,omitempty"`
	CreatedAt               time.Time     `json:"created_at"`
	StartedAt               time.Time     `json:"started_at,omitempty"`
	CompletedAt             time.Time     `json:"completed_at,omitempty"`
}

// Job represents one durable subtitle processing identity.
type Job struct {
	ID               string    `json:"id"`
	MediaID          string    `json:"media_id"`
	ContentSignature string    `json:"content_signature,omitempty"`
	SourceKey        string    `json:"source_key"`
	SourcePath       string    `json:"source_path"`
	FileName         string    `json:"file_name"`
	TorrentName      string    `json:"torrent_name"`
	Category         string    `json:"category"`
	Status           JobStatus `json:"status"`
	Error            string    `json:"error,omitempty"`
	Retries          int       `json:"retries"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	CompletedAt      time.Time `json:"completed_at,omitempty"`

	IsLight                 bool   `json:"is_light"`
	SubtitleDetectionReason string `json:"subtitle_detection_reason,omitempty"`
	SidecarSubtitlePath     string `json:"sidecar_subtitle_path,omitempty"`

	StagingPath         string `json:"staging_path,omitempty"`
	ProcessingPath      string `json:"processing_path,omitempty"`
	SubtitlePath        string `json:"subtitle_path,omitempty"`
	TranslatedPath      string `json:"translated_path,omitempty"`
	Checkpoint          Stage  `json:"checkpoint,omitempty"`
	FailureStage        Stage  `json:"failure_stage,omitempty"`
	TranscriptionSource string `json:"transcription_source,omitempty"`

	AttemptID               int64         `json:"attempt_id,omitempty"`
	AttemptKind             AttemptKind   `json:"attempt_kind,omitempty"`
	StartStage              Stage         `json:"start_stage,omitempty"`
	AttemptStatus           AttemptStatus `json:"attempt_status,omitempty"`
	AttemptStage            Stage         `json:"attempt_stage,omitempty"`
	AttemptFailureStage     Stage         `json:"attempt_failure_stage,omitempty"`
	AttemptError            string        `json:"attempt_error,omitempty"`
	TranslationCacheEnabled bool          `json:"translation_cache_enabled"`
	SettingsSnapshot        string        `json:"settings_snapshot,omitempty"`

	progress func(context.Context, *Job, Stage, bool) error
	settings func(context.Context, int64, string) error
}

// JobDetail combines a Job with its Attempt history.

type JobDetail struct {
	Job
	Attempts []Attempt `json:"attempts"`
}

// NewJob creates a pending Job identified by content when available, then by cleaned filename.
func NewJob(id, sourcePath, fileName, torrentName, category string) *Job {
	now := time.Now()
	sourceKey := MediaID(fileName)
	signature := fileSignature(sourcePath)
	mediaID := signature
	if mediaID == "" {
		mediaID = sourceKey
	}
	return &Job{
		ID:                      id,
		MediaID:                 mediaID,
		ContentSignature:        signature,
		SourceKey:               sourceKey,
		SourcePath:              sourcePath,
		FileName:                fileName,
		TorrentName:             torrentName,
		Category:                category,
		Status:                  StatusPending,
		CreatedAt:               now,
		UpdatedAt:               now,
		StartStage:              StagePreparing,
		TranslationCacheEnabled: true,
	}
}

// BeginStage records the stage that is about to run.
func (j *Job) BeginStage(ctx context.Context, stage Stage) error {
	if j.progress == nil {
		return nil
	}
	return j.progress(ctx, j, stage, false)
}

// SaveCheckpoint persists the Job fields after a stage succeeds.
func (j *Job) SaveCheckpoint(ctx context.Context, checkpoint Stage) error {
	if j.progress != nil {
		if err := j.progress(ctx, j, checkpoint, true); err != nil {
			return err
		}
	}
	j.Checkpoint = checkpoint
	return nil
}

// SaveSettings persists the non-secret configuration snapshot for this Attempt.
func (j *Job) SaveSettings(ctx context.Context, snapshot string) error {
	if j.settings == nil {
		return nil
	}
	return j.settings(ctx, j.AttemptID, snapshot)
}

// MediaID returns the stable Job key for a media filename.
func MediaID(name string) string {
	return mediaintake.CleanVideoFilename(filepath.Base(name))
}

func fileSignature(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}

	const sampleSize int64 = 1 << 20
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%d:", info.Size())
	_, _ = io.CopyN(hash, file, min(info.Size(), sampleSize))
	if info.Size() > sampleSize {
		_, _ = file.Seek(max(sampleSize, info.Size()-sampleSize), io.SeekStart)
		_, _ = io.CopyN(hash, file, sampleSize)
	}
	return fmt.Sprintf("sha256-sample:%x", hash.Sum(nil))
}

// ConflictError identifies the existing Job that rejected admission.
type ConflictError struct {
	JobID  string    `json:"job_id"`
	Status JobStatus `json:"status"`
}

// Error reports the duplicate media conflict.

func (e *ConflictError) Error() string {
	return fmt.Sprintf("media already belongs to job %s (%s)", e.JobID, e.Status)
}

// Unwrap exposes ErrDuplicateMedia for errors.Is.

func (e *ConflictError) Unwrap() error { return ErrDuplicateMedia }
