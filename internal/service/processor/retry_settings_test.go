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

func TestTranslationRetryBackoffUsesAttemptSnapshotOrExplicitCurrentSettings(t *testing.T) {
	for _, item := range []struct {
		name, snapshot string
		want           float64
	}{
		{"recorded backoff zero", `{"Pipeline":{"Provider":"mlx_qwen3_asr"},"LLMSubtrans":{"TimeoutMinutes":180,"RetryBackoffSeconds":0}}`, 0.0},
		{"recorded backoff fractional", `{"Pipeline":{"Provider":"mlx_qwen3_asr"},"LLMSubtrans":{"TimeoutMinutes":180,"RetryBackoffSeconds":1.5}}`, 1.5},
		{"legacy snapshot without backoff", `{"Pipeline":{"Provider":"mlx_qwen3_asr"},"LLMSubtrans":{"TimeoutMinutes":180}}`, 3.0},
		{"legacy snapshot without llm_subtrans", `{"Pipeline":{"Provider":"mlx_qwen3_asr"}}`, 3.0},
		{"current settings", "", 0.0},
	} {
		t.Run(item.name, func(t *testing.T) {
			svc, manager, folders, job := newProcessFixture(t, "SNOS-411.mp4")
			mustWriteTestFile(t, filepath.Join(filepath.Dir(folders.Process), "config.yaml"), "pipeline:\n  provider: mlx_qwen3_asr\nllm_subtrans:\n  retry_backoff_seconds: 0\n")
			if err := manager.Reload(); err != nil {
				t.Fatal(err)
			}
			job.StartStage = queue.StageTranslating
			job.SettingsSnapshot = item.snapshot
			job.SubtitlePath = filepath.Join(folders.Transcriptions, "SNOS-411.srt")
			mustWriteTestFile(t, job.SubtitlePath, "source subtitles")
			var got float64
			svc.resolveExecutors = func(cfg config.Config) (transcriber, subtitleTranslator, error) {
				if cfg.LLMSubtrans.RetryBackoffSeconds != nil {
					got = *cfg.LLMSubtrans.RetryBackoffSeconds
				} else {
					got = -1
				}
				return nil, subtitleTranslatorFunc(func(_ context.Context, input string) (string, error) {
					output := filepath.Join(filepath.Dir(input), "translated.srt")
					mustWriteTestFile(t, output, "translated subtitles")
					return output, nil
				}), nil
			}
			next, err := svc.ProcessStage(context.Background(), job)
			if err != nil || next != queue.StageDelivering || got != item.want {
				t.Fatalf("next=%q retry_backoff_seconds=%v want=%v error=%v", next, got, item.want, err)
			}
			if item.snapshot == "" {
				return
			}
			// A retry of a recorded Attempt keeps its backoff unaffected by current settings.
			zero := 0.0
			cfg, err := configForAttempt(config.Config{LLMSubtrans: config.LLMSubtransConfig{RetryBackoffSeconds: &zero}}, item.snapshot)
			if err != nil || cfg.LLMSubtrans.RetryBackoffSeconds == nil || *cfg.LLMSubtrans.RetryBackoffSeconds != item.want {
				t.Fatalf("snapshot retry_backoff_seconds=%v want=%v error=%v", cfg.LLMSubtrans.RetryBackoffSeconds, item.want, err)
			}
		})
	}
}

func TestTranslationBatchSizesUseAttemptSnapshotOrExplicitCurrentSettings(t *testing.T) {
	for _, item := range []struct {
		name, snapshot   string
		wantMin, wantMax int
	}{
		{"recorded batch sizes", `{"Pipeline":{"Provider":"mlx_qwen3_asr"},"LLMSubtrans":{"TimeoutMinutes":180,"MinBatchSize":20,"MaxBatchSize":50}}`, 20, 50},
		{"legacy snapshot without batch sizes", `{"Pipeline":{"Provider":"mlx_qwen3_asr"},"LLMSubtrans":{"TimeoutMinutes":180}}`, 10, 100},
		{"legacy snapshot without llm_subtrans", `{"Pipeline":{"Provider":"mlx_qwen3_asr"}}`, 10, 100},
		{"current settings", "", 25, 75},
	} {
		t.Run(item.name, func(t *testing.T) {
			svc, manager, folders, job := newProcessFixture(t, "SNOS-411.mp4")
			mustWriteTestFile(t, filepath.Join(filepath.Dir(folders.Process), "config.yaml"), "pipeline:\n  provider: mlx_qwen3_asr\nllm_subtrans:\n  min_batch_size: 25\n  max_batch_size: 75\n")
			if err := manager.Reload(); err != nil {
				t.Fatal(err)
			}
			job.StartStage = queue.StageTranslating
			job.SettingsSnapshot = item.snapshot
			job.SubtitlePath = filepath.Join(folders.Transcriptions, "SNOS-411.srt")
			mustWriteTestFile(t, job.SubtitlePath, "source subtitles")
			var gotMin, gotMax int
			svc.resolveExecutors = func(cfg config.Config) (transcriber, subtitleTranslator, error) {
				gotMin = cfg.LLMSubtrans.MinBatchSize
				gotMax = cfg.LLMSubtrans.MaxBatchSize
				return nil, subtitleTranslatorFunc(func(_ context.Context, input string) (string, error) {
					output := filepath.Join(filepath.Dir(input), "translated.srt")
					mustWriteTestFile(t, output, "translated subtitles")
					return output, nil
				}), nil
			}
			next, err := svc.ProcessStage(context.Background(), job)
			if err != nil || next != queue.StageDelivering || gotMin != item.wantMin || gotMax != item.wantMax {
				t.Fatalf("next=%q min=%d wantMin=%d max=%d wantMax=%d error=%v", next, gotMin, item.wantMin, gotMax, item.wantMax, err)
			}
			if item.snapshot == "" {
				return
			}
			cfg, err := configForAttempt(config.Config{LLMSubtrans: config.LLMSubtransConfig{MinBatchSize: 25, MaxBatchSize: 75}}, item.snapshot)
			if err != nil || cfg.LLMSubtrans.MinBatchSize != item.wantMin || cfg.LLMSubtrans.MaxBatchSize != item.wantMax {
				t.Fatalf("snapshot min=%d wantMin=%d max=%d wantMax=%d error=%v", cfg.LLMSubtrans.MinBatchSize, item.wantMin, cfg.LLMSubtrans.MaxBatchSize, item.wantMax, err)
			}
		})
	}
}
