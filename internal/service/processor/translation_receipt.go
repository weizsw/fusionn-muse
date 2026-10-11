package processor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fusionn-muse/internal/queue"
)

func (s *Service) translationReceiptPath(job *queue.Job) string {
	return filepath.Join(s.folders.Transcriptions, ".translations", job.ID, strconv.FormatInt(job.AttemptID, 10), "translated.srt")
}

// Only a complete translation is published to this Attempt-owned receipt.
// A subprocess's partial output is never recovery evidence.
func (s *Service) translateWithReceipt(ctx context.Context, job *queue.Job, translator subtitleTranslator) (string, error) {
	candidate := job.TranslatedPath
	if (job.AttemptKind != queue.AttemptRetranslate || job.TranslationCacheEnabled) && candidate != "" {
		if s.isTranslationReceipt(job, candidate) && validateArtifact(candidate) == nil {
			input := filepath.Join(filepath.Dir(candidate), "source", filepath.Base(job.SubtitlePath))
			same, err := fileOperationDone(job.SubtitlePath, input)
			if err == nil && same {
				if err := job.BeginStage(ctx, queue.StageTranslating); err != nil {
					return "", err
				}
				return candidate, nil
			}
		}
	}
	output := s.translationReceiptPath(job)
	input := filepath.Join(filepath.Dir(output), "source", filepath.Base(job.SubtitlePath))
	if err := job.ClaimArtifacts(ctx, output); err != nil {
		return "", err
	}
	job.TranslatedPath = output
	if err := job.BeginStage(ctx, queue.StageTranslating); err != nil {
		return "", err
	}
	if err := atomicCopy(ctx, job.SubtitlePath, input); err != nil {
		return "", fmt.Errorf("prepare translation source: %w", err)
	}
	generated, err := translator.Translate(ctx, input)
	if err != nil {
		return "", err
	}
	if err := validateArtifact(generated); err != nil {
		return "", err
	}
	if err := atomicCopy(ctx, generated, output); err != nil {
		return "", fmt.Errorf("persist translation receipt: %w", err)
	}
	if !samePath(generated, job.SubtitlePath) && !samePath(generated, output) && !samePath(generated, input) {
		_ = os.Remove(generated)
	}
	return output, nil
}

func (s *Service) isTranslationReceipt(job *queue.Job, path string) bool {
	root := filepath.Join(s.folders.Transcriptions, ".translations", job.ID)
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) && filepath.Base(path) == "translated.srt"
}
