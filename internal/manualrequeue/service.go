package manualrequeue

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	Err      error
}

type Result struct {
	Accepted []Outcome
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
		Failed:   make([]Outcome, 0),
	}
	for _, fileName := range files {
		outcome := s.requeueOne(ctx, location, fileName)
		if outcome.Err != nil {
			result.Failed = append(result.Failed, outcome)
		} else {
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
	staged := location == Staging
	if location == Failed {
		stagingPath := filepath.Join(s.folders.Staging, fileName)
		if err := moveNoReplace(path, stagingPath); err != nil {
			return Outcome{FileName: fileName, Err: err}
		}
		path = stagingPath
		staged = true
	}

	resolved, err := mediaintake.ResolveMedia(mediaintake.ResolveRequest{
		Context:    ctx,
		Path:       path,
		StagingDir: s.folders.Staging,
		Runner:     s.runner,
	})
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			err = fmt.Errorf("%w: %s", ErrNotFound, fileName)
		case errors.Is(err, mediaintake.ErrNoValidMedia):
			err = fmt.Errorf("%w: %s", ErrInvalidMedia, fileName)
		}
		return Outcome{FileName: fileName, Staged: staged, Err: err}
	}

	jobID := uuid.NewString()
	job := queue.NewJob(jobID, path, fileName, "", "")
	job.StagingPath = path
	job.IsLight = resolved.HasChineseSubtitle
	job.SubtitleDetectionReason = resolved.SubtitleDetectionReason
	job.SidecarSubtitlePath = resolved.SidecarSubtitlePath
	if err := s.queue.Accept(job); err != nil {
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

func moveNoReplace(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("create staging: %w", err)
	}
	if err := os.Link(src, dst); err == nil {
		if err := os.Remove(src); err != nil {
			_ = os.Remove(dst)
			return fmt.Errorf("remove failed media: %w", err)
		}
		return nil
	} else if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%w: %s", ErrConflict, filepath.Base(dst))
	} else if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, filepath.Base(src))
	}

	source, err := os.Open(src)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, filepath.Base(src))
	}
	if err != nil {
		return fmt.Errorf("open failed media: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return fmt.Errorf("stat failed media: %w", err)
	}
	destination, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode())
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%w: %s", ErrConflict, filepath.Base(dst))
	}
	if err != nil {
		return fmt.Errorf("create staging media: %w", err)
	}
	removeDestination := true
	defer func() {
		_ = destination.Close()
		if removeDestination {
			_ = os.Remove(dst)
		}
	}()
	if _, err := io.Copy(destination, source); err != nil {
		return fmt.Errorf("copy to staging: %w", err)
	}
	if err := destination.Close(); err != nil {
		return fmt.Errorf("close staging media: %w", err)
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("remove failed media: %w", err)
	}
	removeDestination = false
	return nil
}
