package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/fusionn-muse/internal/mediaintake"
)

type JobCandidate struct {
	TaskReference
	Name       string `json:"name"`
	SourcePath string `json:"source_path"`
	Status     string `json:"status"`
}

type AmbiguousJobReferenceError struct {
	Reference  string
	Candidates []JobCandidate
}

func (e *AmbiguousJobReferenceError) Error() string {
	return fmt.Sprintf("job code %s matches multiple Jobs; choose a candidate ID", e.Reference)
}

// resolveJobReference keeps code lookup and its subsequent read/write in the
// same transaction. SourceKey retains the original code after output renaming.
func resolveJobReference(ctx context.Context, tx *sql.Tx, reference string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM jobs WHERE id=?`, reference).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	code, ok := mediaintake.ExtractVideoCode(reference)
	if !ok || !strings.EqualFold(code, reference) {
		return "", ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, `SELECT j.id,j.source_key,j.file_name,j.source_path,
		COALESCE((SELECT status FROM attempts WHERE job_id=j.id ORDER BY id DESC LIMIT 1),j.status)
		FROM jobs j WHERE UPPER(j.source_key) LIKE ? OR UPPER(j.file_name) LIKE ? ORDER BY j.id`, code+"%", code+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var candidates []JobCandidate
	for rows.Next() {
		candidate := JobCandidate{TaskReference: TaskReference{Kind: "job"}}
		var sourceKey string
		if err := rows.Scan(&candidate.ID, &sourceKey, &candidate.Name, &candidate.SourcePath, &candidate.Status); err != nil {
			return "", err
		}
		storedCode, ok := mediaintake.ExtractVideoCode(sourceKey)
		if !ok {
			storedCode, _ = mediaintake.ExtractVideoCode(candidate.Name)
		}
		if storedCode != code {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", ErrNotFound
	}
	if len(candidates) > 1 {
		return "", &AmbiguousJobReferenceError{Reference: code, Candidates: candidates}
	}
	return candidates[0].ID, nil
}
