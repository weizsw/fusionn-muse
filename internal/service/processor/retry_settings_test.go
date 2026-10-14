package processor

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/queue"
)

func TestTranslationMaxRetriesUsesAttemptSnapshotOrExplicitCurrentSettings(t *testing.T) {
	for _, item := range []struct {
		name, snapshot string
		want           int
	}{
		{"recorded retries", `{"Pipeline":{"Provider":"mlx_qwen3_asr"},"LLMSubtrans":{"TimeoutMinutes":180,"MaxRetries":3}}`, 3},
		{"legacy snapshot without retries", `{"Pipeline":{"Provider":"mlx_qwen3_asr"},"LLMSubtrans":{"TimeoutMinutes":180}}`, 1},
		{"legacy snapshot without llm_subtrans", `{"Pipeline":{"Provider":"mlx_qwen3_asr"}}`, 1},
		{"current settings", "", 5},
	} {
		t.Run(item.name, func(t *testing.T) {
			svc, manager, folders, job := newProcessFixture(t, "SNOS-411.mp4")
			mustWriteTestFile(t, filepath.Join(filepath.Dir(folders.Process), "config.yaml"), "pipeline:\n  provider: mlx_qwen3_asr\nllm_subtrans:\n  max_retries: 5\n")
			if err := manager.Reload(); err != nil {
				t.Fatal(err)
			}
			job.StartStage = queue.StageTranslating
			job.SettingsSnapshot = item.snapshot
			job.SubtitlePath = filepath.Join(folders.Transcriptions, "SNOS-411.srt")
			mustWriteTestFile(t, job.SubtitlePath, "source subtitles")
			var got int
			svc.resolveExecutors = func(cfg config.Config) (transcriber, subtitleTranslator, error) {
				got = cfg.LLMSubtrans.MaxRetries
				return nil, subtitleTranslatorFunc(func(_ context.Context, input string) (string, error) {
					output := filepath.Join(filepath.Dir(input), "translated.srt")
					mustWriteTestFile(t, output, "translated subtitles")
					return output, nil
				}), nil
			}
			next, err := svc.ProcessStage(context.Background(), job)
			if err != nil || next != queue.StageDelivering || got != item.want {
				t.Fatalf("next=%q retries=%d want=%d error=%v", next, got, item.want, err)
			}
			if item.snapshot == "" {
				return
			}
			// A retry of a recorded Attempt keeps its retry count unaffected by current settings.
			cfg, err := configForAttempt(config.Config{LLMSubtrans: config.LLMSubtransConfig{MaxRetries: 5}}, item.snapshot)
			if err != nil || cfg.LLMSubtrans.MaxRetries != item.want {
				t.Fatalf("snapshot retries=%d want=%d error=%v", cfg.LLMSubtrans.MaxRetries, item.want, err)
			}
		})
	}
}

func TestTranslationRequestTimeoutUsesAttemptSnapshotOrExplicitCurrentSettings(t *testing.T) {
	for _, item := range []struct {
		name, snapshot string
		want           int
	}{
		{"recorded timeout", `{"Pipeline":{"Provider":"mlx_qwen3_asr"},"LLMSubtrans":{"TimeoutMinutes":180,"RequestTimeoutSeconds":300}}`, 300},
		{"legacy snapshot without request timeout", `{"Pipeline":{"Provider":"mlx_qwen3_asr"},"LLMSubtrans":{"TimeoutMinutes":180}}`, 180},
		{"legacy snapshot without llm_subtrans", `{"Pipeline":{"Provider":"mlx_qwen3_asr"}}`, 180},
		{"current settings", "", 240},
	} {
		t.Run(item.name, func(t *testing.T) {
			svc, manager, folders, job := newProcessFixture(t, "SNOS-411.mp4")
			mustWriteTestFile(t, filepath.Join(filepath.Dir(folders.Process), "config.yaml"), "pipeline:\n  provider: mlx_qwen3_asr\nllm_subtrans:\n  request_timeout_seconds: 240\n")
			if err := manager.Reload(); err != nil {
				t.Fatal(err)
			}
			job.StartStage = queue.StageTranslating
			job.SettingsSnapshot = item.snapshot
			job.SubtitlePath = filepath.Join(folders.Transcriptions, "SNOS-411.srt")
			mustWriteTestFile(t, job.SubtitlePath, "source subtitles")
			var got int
			svc.resolveExecutors = func(cfg config.Config) (transcriber, subtitleTranslator, error) {
				got = cfg.LLMSubtrans.RequestTimeoutSeconds
				return nil, subtitleTranslatorFunc(func(_ context.Context, input string) (string, error) {
					output := filepath.Join(filepath.Dir(input), "translated.srt")
					mustWriteTestFile(t, output, "translated subtitles")
					return output, nil
				}), nil
			}
			next, err := svc.ProcessStage(context.Background(), job)
			if err != nil || next != queue.StageDelivering || got != item.want {
				t.Fatalf("next=%q request_timeout_seconds=%d want=%d error=%v", next, got, item.want, err)
			}
			if item.snapshot == "" {
				return
			}
			// A retry of a recorded Attempt keeps its timeout unaffected by current settings.
			cfg, err := configForAttempt(config.Config{LLMSubtrans: config.LLMSubtransConfig{RequestTimeoutSeconds: 240}}, item.snapshot)
			if err != nil || cfg.LLMSubtrans.RequestTimeoutSeconds != item.want {
				t.Fatalf("snapshot request_timeout_seconds=%d want=%d error=%v", cfg.LLMSubtrans.RequestTimeoutSeconds, item.want, err)
			}
		})
	}
}
