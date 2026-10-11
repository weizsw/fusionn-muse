package processor

import (
	"context"
	"os"
	"path/filepath"
	"strconv"

	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/queue"
)

// PrepareSubmission isolates image/multipart output by Submission and run.
// Its cleanup never removes the submitted source or another Job's artifacts.
func (s *Service) PrepareSubmission(ctx context.Context, input *queue.Submission) (*mediaintake.ResolvedMedia, func() error, error) {
	work := filepath.Join(s.folders.Staging, ".submissions", input.ID, strconv.FormatInt(input.RunID, 10))
	cleanup := func() error { return os.RemoveAll(work) }
	media, err := mediaintake.ResolveMedia(mediaintake.ResolveRequest{
		Context: ctx, Path: input.SourcePath, TorrentName: input.TorrentName,
		StagingDir: filepath.Join(work, "prepared"), Runner: s.runner,
	})
	if err != nil {
		return nil, cleanup, err
	}
	// Prepared source caches remain isolated, but Job admission still uses the
	// canonical no-replace destination to detect collisions with existing work.
	media.StagingPath = filepath.Join(s.folders.Staging, media.FileName)
	return media, cleanup, nil
}
