package processor

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/queue"
)

func TestTranslationRateLimitUsesAttemptSnapshotOrExplicitCurrentSettings(t *testing.T) {
	for _, item := range []struct {
		name, snapshot string
		want           float64
	}{
		{"recorded rate", `{"Pipeline":{"Provider":"mlx_qwen3_asr"},"LLMSubtrans":{"TimeoutMinutes":180,"RateLimit":20}}`, 20},
		{"legacy snapshot without rate", `{"Pipeline":{"Provider":"mlx_qwen3_asr"},"LLMSubtrans":{"TimeoutMinutes":180}}`, 0},
		{"current settings", "", 80},
	} {
		t.Run(item.name, func(t *testing.T) {
			svc, manager, folders, job := newProcessFixture(t, "SNOS-411.mp4")
			mustWriteTestFile(t, filepath.Join(filepath.Dir(folders.Process), "config.yaml"), "pipeline:\n  provider: mlx_qwen3_asr\nllm_subtrans:\n  rate_limit: 80\n")
			if err := manager.Reload(); err != nil {
				t.Fatal(err)
			}
			job.StartStage = queue.StageTranslating
			job.SettingsSnapshot = item.snapshot
			job.SubtitlePath = filepath.Join(folders.Transcriptions, "SNOS-411.srt")
			mustWriteTestFile(t, job.SubtitlePath, "source subtitles")
			var got float64
			svc.resolveExecutors = func(cfg config.Config) (transcriber, subtitleTranslator, error) {
				got = cfg.LLMSubtrans.RateLimit
				return nil, subtitleTranslatorFunc(func(_ context.Context, input string) (string, error) {
					output := filepath.Join(filepath.Dir(input), "translated.srt")
					mustWriteTestFile(t, output, "translated subtitles")
					return output, nil
				}), nil
			}
			next, err := svc.ProcessStage(context.Background(), job)
			if err != nil || next != queue.StageDelivering || got != item.want {
				t.Fatalf("next=%q rate=%g want=%g error=%v", next, got, item.want, err)
			}
		})
	}
}
