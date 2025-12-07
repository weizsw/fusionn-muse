package queue

import (
	"time"
)

// JobStatus represents the current state of a job.
type JobStatus string

const (
	StatusPending    JobStatus = "pending"
	StatusProcessing JobStatus = "processing"
	StatusCompleted  JobStatus = "completed"
	StatusFailed     JobStatus = "failed"
)

// Job represents a subtitle processing job.
type Job struct {
	ID          string    `json:"id"`
	SourcePath  string    `json:"source_path"`  // Original torrent file path
	FileName    string    `json:"file_name"`    // Just the filename
	TorrentName string    `json:"torrent_name"` // Torrent name from qBittorrent
	Category    string    `json:"category"`     // Category from qBittorrent
	Status      JobStatus `json:"status"`
	Error       string    `json:"error,omitempty"`
	Retries     int       `json:"retries"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`

	// IsLight indicates this job has Chinese subtitle detected and can skip transcription
	IsLight bool `json:"is_light"`

	// Paths set during processing
	StagingPath    string `json:"staging_path,omitempty"`
	ProcessingPath string `json:"processing_path,omitempty"`
	SubtitlePath   string `json:"subtitle_path,omitempty"`
	TranslatedPath string `json:"translated_path,omitempty"`
}

// NewJob creates a new job with the given parameters.
func NewJob(id, sourcePath, fileName, torrentName, category string) *Job {
	return &Job{
		ID:          id,
		SourcePath:  sourcePath,
		FileName:    fileName,
		TorrentName: torrentName,
		Category:    category,
		Status:      StatusPending,
		CreatedAt:   time.Now(),
	}
}
