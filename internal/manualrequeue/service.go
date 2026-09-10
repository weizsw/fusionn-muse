package manualrequeue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/queue"
)

var (
	ErrInvalidLocation = errors.New("invalid managed location")
	ErrInvalidName     = errors.New("invalid file name")
	ErrInvalidMedia    = errors.New("invalid media")
	ErrConflict        = errors.New("staging destination exists")
	ErrNotFound        = errors.New("media not found")
)

type Location string

const (
	Staging Location = "staging"
	Failed  Location = "failed"
)

type Request struct {
	Location Location
	FileName *string
}

type Outcome struct {
	FileName string
	JobID    string
	Staged   bool
	Skipped  bool
	Err      error
}

type Result struct {
	Accepted []Outcome
	Skipped  []Outcome
	Failed   []Outcome
	Err      error
}

type accepter interface {
	Accept(*queue.Job) error
}

type Service struct {
	queue   accepter
	folders config.FoldersConfig
	runner  mediaintake.CommandRunner
}

func New(queue accepter, folders config.FoldersConfig, runner mediaintake.CommandRunner) *Service {
	if runner == nil {
		runner = mediaintake.ExecCommandRunner{}
	}
	return &Service{queue: queue, folders: folders, runner: runner}
}

// Requeue manually returns one or all managed media files to processing.
func (s *Service) Requeue(ctx context.Context, req Request) Result {
	if ctx == nil {
		ctx = context.Background()
	}
	if req.FileName != nil {
		if err := validateFileName(*req.FileName); err != nil {
			return Result{Failed: []Outcome{{FileName: *req.FileName, Err: err}}}
		}
		return s.requeueFiles(ctx, req.Location, []string{*req.FileName})
	}

	files, err := s.List(req.Location)
	if err != nil {
		return Result{Err: err}
	}
	return s.requeueFiles(ctx, req.Location, files)
}

// List returns media file names in a managed location.
func (s *Service) List(location Location) ([]string, error) {
	dir, err := s.directory(location)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", location, err)
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && mediaintake.IsVideoFile(entry.Name()) {
			files = append(files, entry.Name())
		}
	}
	return files, nil
}

func (s *Service) requeueFiles(ctx context.Context, location Location, files []string) Result {
	result := Result{
		Accepted: make([]Outcome, 0, len(files)),
		Skipped:  make([]Outcome, 0),
		Failed:   make([]Outcome, 0),
	}
	for _, fileName := range files {
		outcome := s.requeueOne(ctx, location, fileName)
		switch {
		case outcome.Err != nil:
			result.Failed = append(result.Failed, outcome)
		case outcome.Skipped:
			result.Skipped = append(result.Skipped, outcome)
		default:
			result.Accepted = append(result.Accepted, outcome)
		}
	}
	return result
}

func (s *Service) requeueOne(ctx context.Context, location Location, fileName string) Outcome {
	dir, err := s.directory(location)
	if err != nil {
		return Outcome{FileName: fileName, Err: err}
	}
	path := filepath.Join(dir, fileName)
	if !mediaintake.IsVideoFile(fileName) {
		return Outcome{FileName: fileName, Staged: location == Staging, Err: fmt.Errorf("%w: %s", ErrInvalidMedia, fileName)}
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("%w: %s", ErrNotFound, fileName)
		}
		return Outcome{FileName: fileName, Staged: location == Staging, Err: err}
	}

	if location == Failed {
		var jobID string
		if finder, ok := s.queue.(interface {
			FindJobByMedia(string, string) *queue.Job
		}); ok {
			if job := finder.FindJobByMedia(path, fileName); job != nil {
				jobID = job.ID
			}
		}
		return Outcome{FileName: fileName, JobID: jobID, Skipped: true}
	}

	resolved, err := mediaintake.ResolveMedia(mediaintake.ResolveRequest{
		Context:    ctx,
		Path:       path,
		StagingDir: s.folders.Staging,
		Runner:     s.runner,
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("%w: %s", ErrNotFound, fileName)
		} else if errors.Is(err, mediaintake.ErrNoValidMedia) {
			err = fmt.Errorf("%w: %s", ErrInvalidMedia, fileName)
		}
		return Outcome{FileName: fileName, Staged: true, Err: err}
	}

	jobID := uuid.NewString()
	job := queue.NewJob(jobID, resolved.SourcePath, fileName, "", "")
	job.StagingPath = path
	job.IsLight = resolved.HasChineseSubtitle
	job.SubtitleDetectionReason = resolved.SubtitleDetectionReason
	job.SidecarSubtitlePath = resolved.SidecarSubtitlePath
	if err := s.queue.Accept(job); err != nil {
		var conflict *queue.ConflictError
		if errors.As(err, &conflict) {
			return Outcome{FileName: fileName, JobID: conflict.JobID, Staged: true, Skipped: true}
		}
		return Outcome{FileName: fileName, Staged: true, Err: err}
	}
	return Outcome{FileName: fileName, JobID: jobID, Staged: true}
}

func (s *Service) directory(location Location) (string, error) {
	switch location {
	case Staging:
		return s.folders.Staging, nil
	case Failed:
		return s.folders.Failed, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidLocation, location)
	}
}

func validateFileName(name string) error {
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") || filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) || filepath.Base(name) != name || filepath.Clean(name) != name {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return nil
}
