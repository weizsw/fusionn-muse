package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	ErrArtifactConflict    = errors.New("artifact conflict")
	ErrArtifactUnavailable = errors.New("artifact unavailable")
)

// ValidateMedia checks the actual artifact, not just a recorded checkpoint.
func (j *Job) ValidateMedia(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrArtifactUnavailable, path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("%w: media must be a nonempty regular file: %s", ErrArtifactUnavailable, path)
	}
	if j.ContentSignature != "" && fileSignature(path) != j.ContentSignature {
		return fmt.Errorf("%w: media identity changed: %s", ErrArtifactConflict, path)
	}
	return nil
}

const artifactSchema = `CREATE TABLE IF NOT EXISTS artifact_claims (
	path TEXT PRIMARY KEY,
	job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
	conflicted INTEGER NOT NULL DEFAULT 0
);`

// ClaimArtifacts reserves output paths before any file mutation. A claim is
// ownership, not a successful Stage checkpoint or a copy of subtitle content.
func (j *Job) ClaimArtifacts(ctx context.Context, paths ...string) error {
	if j.artifacts == nil {
		return nil // Standalone, non-durable processing, like SaveCheckpoint.
	}
	return j.artifacts(ctx, j, paths)
}

func (s *store) claimArtifacts(ctx context.Context, job *Job, paths []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	persisted, err := queryJob(tx, job.ID)
	if err != nil {
		return err
	}
	if err := claimArtifactsTx(ctx, tx, persisted, paths); err != nil {
		return err
	}
	return tx.Commit()
}

func claimArtifactsTx(ctx context.Context, tx *sql.Tx, persisted *Job, paths []string) error {
	for _, path := range paths {
		path, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		var owner string
		var conflicted int
		err = tx.QueryRowContext(ctx, `SELECT job_id, conflicted FROM artifact_claims WHERE path=?`, path).Scan(&owner, &conflicted)
		if err == nil {
			if owner != persisted.ID || conflicted != 0 {
				return fmt.Errorf("%w: %s belongs to another or ambiguous Job", ErrArtifactConflict, path)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := os.Lstat(path); err == nil {
			known := false
			for _, recorded := range []string{persisted.StagingPath, persisted.ProcessingPath, persisted.SubtitlePath, persisted.TranslatedPath} {
				if recorded == "" {
					continue
				}
				absolute, err := filepath.Abs(recorded)
				if err != nil {
					return err
				}
				known = known || absolute == path
			}
			if !known {
				return fmt.Errorf("%w: %s has no recorded Job ownership", ErrArtifactConflict, path)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s: %w", ErrArtifactUnavailable, path, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_claims(path, job_id) VALUES (?,?)`, path, persisted.ID); err != nil {
			return err
		}
	}
	return nil
}
