package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/fusionn-muse/pkg/logger"
)

func init() {
	logger.Init(true)
}

func TestManagerReloadUsesOwnConfigFile(t *testing.T) {
	root := t.TempDir()
	aPath := filepath.Join(root, "a.yaml")
	bPath := filepath.Join(root, "b.yaml")
	writeConfig(t, aPath, false)
	writeConfig(t, bPath, false)

	aMgr, err := NewManager(aPath)
	if err != nil {
		t.Fatalf("NewManager(a): %v", err)
	}
	defer aMgr.Stop()
	bMgr, err := NewManager(bPath)
	if err != nil {
		t.Fatalf("NewManager(b): %v", err)
	}
	defer bMgr.Stop()

	writeConfig(t, aPath, true)
	if err := aMgr.Reload(); err != nil {
		t.Fatalf("reload a config: %v", err)
	}

	if !aMgr.Get().DryRun {
		t.Fatal("a manager reloaded the wrong config file")
	}
	if bMgr.Get().DryRun {
		t.Fatal("b manager changed while reloading a manager")
	}
}

func TestLoadParsesMLXQwen3ASRPipelineConfig(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
pipeline:
  provider: mlx_qwen3_asr
mlx_qwen3_asr:
  server_url: http://host.docker.internal:8765
  host_prefix: /Volumes/media/data
  container_prefix: /data
  model: Qwen/Qwen3-ASR-1.7B
  language: ja
  timeout_minutes: 180
llm_subtrans:
  timeout_minutes: 180
`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Pipeline.Provider != "mlx_qwen3_asr" {
		t.Fatalf("pipeline provider = %q", cfg.Pipeline.Provider)
	}
	if cfg.MLXQwen3ASR.ServerURL != "http://host.docker.internal:8765" {
		t.Fatalf("mlx server url = %q", cfg.MLXQwen3ASR.ServerURL)
	}
	if cfg.MLXQwen3ASR.HostPrefix != "/Volumes/media/data" {
		t.Fatalf("mlx host prefix = %q", cfg.MLXQwen3ASR.HostPrefix)
	}
	if cfg.MLXQwen3ASR.ContainerPrefix != "/data" {
		t.Fatalf("mlx container prefix = %q", cfg.MLXQwen3ASR.ContainerPrefix)
	}
	if cfg.MLXQwen3ASR.Model != "Qwen/Qwen3-ASR-1.7B" {
		t.Fatalf("mlx model = %q", cfg.MLXQwen3ASR.Model)
	}
	if cfg.MLXQwen3ASR.Language != "ja" {
		t.Fatalf("mlx language = %q", cfg.MLXQwen3ASR.Language)
	}
	if cfg.MLXQwen3ASR.TimeoutMinutes != 180 {
		t.Fatalf("mlx timeout = %d", cfg.MLXQwen3ASR.TimeoutMinutes)
	}
	if cfg.LLMSubtrans.TimeoutMinutes != 180 {
		t.Fatalf("llm-subtrans timeout = %d", cfg.LLMSubtrans.TimeoutMinutes)
	}
	if cfg.Queue.DBPath != "/data/automation/meta/fusionn-muse.db" || cfg.Queue.MaxRetries != 4 || cfg.Queue.RetryDelayMs != 10000 {
		t.Fatalf("queue defaults = %+v", cfg.Queue)
	}
}

func TestQueueDBPathDefaultAndExplicitPathsSurviveReload(t *testing.T) {
	for _, item := range []struct{ name, setting, want string }{
		{"default", "", "/data/automation/meta/fusionn-muse.db"},
		{"explicit legacy", "queue:\n  db_path: /data/meta/fusionn-muse.db\n", "/data/meta/fusionn-muse.db"},
		{"explicit custom", "queue:\n  db_path: /custom/state.db\n", "/custom/state.db"},
	} {
		t.Run(item.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(item.setting+"dry_run: false\n"), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil || cfg.Queue.DBPath != item.want {
				t.Fatalf("load=%+v error=%v", cfg, err)
			}
			manager, err := NewManager(path)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Stop()
			if err := os.WriteFile(path, []byte(item.setting+"dry_run: true\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := manager.Reload(); err != nil {
				t.Fatal(err)
			}
			if manager.Get().Queue.DBPath != item.want || !manager.Get().DryRun {
				t.Fatalf("reload=%+v", manager.Get())
			}
		})
	}
}

func writeConfig(t *testing.T, path string, dryRun bool) {
	t.Helper()
	value := "false"
	if dryRun {
		value = "true"
	}
	if err := os.WriteFile(path, []byte("dry_run: "+value+"\n"), 0644); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
}

func TestExampleRecommendsMLXWithoutChangingLegacyOmittedProvider(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Pipeline.Provider != "mlx_qwen3_asr" {
		t.Fatalf("example recommends deprecated pipeline %q", cfg.Pipeline.Provider)
	}
	path := filepath.Join(t.TempDir(), "legacy.yaml")
	writeConfig(t, path, false)
	legacy, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Pipeline.Provider != "" {
		t.Fatalf("legacy omitted provider was silently changed to %q", legacy.Pipeline.Provider)
	}
}

func TestLoadAndReloadTranslationRateLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("llm_subtrans:\n  rate_limit: 80\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.LLMSubtrans.RateLimit != 80 {
		t.Fatalf("load=%+v error=%v", cfg, err)
	}
	manager, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	if err := os.WriteFile(path, []byte("llm_subtrans:\n  rate_limit: 0.5\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(); err != nil || manager.Get().LLMSubtrans.RateLimit != 0.5 {
		t.Fatalf("reload=%+v error=%v", manager.Get(), err)
	}
}

func TestFormatValueDereferencesPointers(t *testing.T) {
	val := 3.5
	var nilPtr *float64
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"float pointer", &val, "3.5"},
		{"nil pointer", nilPtr, "<nil>"},
		{"direct float", 42.0, "42"},
		{"string", "hello", "hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatValue(reflect.ValueOf(tt.v))
			if got != tt.want {
				t.Fatalf("formatValue(%v) = %q, want %q", tt.v, got, tt.want)
			}
		})
	}
}

func TestLoadAndReloadTranslationBatchSizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig(t, path, false)
	cfg, err := Load(path)
	if err != nil || cfg.LLMSubtrans.MinBatchSize != 10 || cfg.LLMSubtrans.MaxBatchSize != 100 {
		t.Fatalf("load default=%+v error=%v", cfg, err)
	}
	manager, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	for _, setting := range []struct {
		minStr, maxStr string
		wantMin        int
		wantMax        int
	}{
		{"20", "50", 20, 50},
		{"1", "1", 1, 1},
		{"", "", 10, 100},
	} {
		content := "dry_run: false\n"
		if setting.minStr != "" {
			content += "llm_subtrans:\n  min_batch_size: " + setting.minStr + "\n  max_batch_size: " + setting.maxStr + "\n"
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil || cfg.LLMSubtrans.MinBatchSize != setting.wantMin || cfg.LLMSubtrans.MaxBatchSize != setting.wantMax {
			t.Fatalf("load=%+v wantMin=%d wantMax=%d error=%v", cfg, setting.wantMin, setting.wantMax, err)
		}
		if err := manager.Reload(); err != nil || manager.Get().LLMSubtrans.MinBatchSize != setting.wantMin || manager.Get().LLMSubtrans.MaxBatchSize != setting.wantMax {
			t.Fatalf("reload=%+v wantMin=%d wantMax=%d error=%v", manager.Get(), setting.wantMin, setting.wantMax, err)
		}
	}
}

func TestTranslationBatchSizesRejectInvalidValuesWithoutReplacingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("llm_subtrans:\n  min_batch_size: 10\n  max_batch_size: 100\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	for _, setting := range []struct {
		name    string
		content string
	}{
		{"zero min", "llm_subtrans:\n  min_batch_size: 0\n  max_batch_size: 100\n"},
		{"negative min", "llm_subtrans:\n  min_batch_size: -1\n  max_batch_size: 100\n"},
		{"float min", "llm_subtrans:\n  min_batch_size: 1.5\n  max_batch_size: 100\n"},
		{"zero max", "llm_subtrans:\n  min_batch_size: 10\n  max_batch_size: 0\n"},
		{"negative max", "llm_subtrans:\n  min_batch_size: 10\n  max_batch_size: -5\n"},
		{"float max", "llm_subtrans:\n  min_batch_size: 10\n  max_batch_size: 50.5\n"},
		{"max smaller than min", "llm_subtrans:\n  min_batch_size: 50\n  max_batch_size: 20\n"},
	} {
		t.Run(setting.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(setting.content), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid batch size accepted by Load")
			}
			if bad, err := NewManager(path); err == nil {
				bad.Stop()
				t.Fatal("invalid batch size accepted by NewManager")
			}
			if err := manager.Reload(); err == nil || manager.Get().LLMSubtrans.MinBatchSize != 10 || manager.Get().LLMSubtrans.MaxBatchSize != 100 {
				t.Fatalf("invalid reload replaced valid config: %+v error=%v", manager.Get(), err)
			}
		})
	}
}

func TestLoadAndReloadTranslationRetryBackoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig(t, path, false)
	cfg, err := Load(path)
	if err != nil || cfg.LLMSubtrans.RetryBackoffSeconds == nil || *cfg.LLMSubtrans.RetryBackoffSeconds != 3.0 {
		t.Fatalf("load default=%+v error=%v", cfg, err)
	}
	manager, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	for _, setting := range []struct {
		value string
		want  float64
	}{{"0", 0.0}, {"0.5", 0.5}, {"3", 3.0}, {"", 3.0}} {
		content := "dry_run: false\n"
		if setting.value != "" {
			content += "llm_subtrans:\n  retry_backoff_seconds: " + setting.value + "\n"
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil || cfg.LLMSubtrans.RetryBackoffSeconds == nil || *cfg.LLMSubtrans.RetryBackoffSeconds != setting.want {
			t.Fatalf("load=%+v want=%v error=%v", cfg, setting.want, err)
		}
		if err := manager.Reload(); err != nil || manager.Get().LLMSubtrans.RetryBackoffSeconds == nil || *manager.Get().LLMSubtrans.RetryBackoffSeconds != setting.want {
			t.Fatalf("reload=%+v want=%v error=%v", manager.Get(), setting.want, err)
		}
	}
}

func TestTranslationRetryBackoffRejectsInvalidValuesWithoutReplacingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("llm_subtrans:\n  retry_backoff_seconds: 3.0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	for _, value := range []string{"-1", "-0.1", "NaN", "Inf", "invalid"} {
		t.Run(value, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("llm_subtrans:\n  retry_backoff_seconds: "+value+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid retry backoff accepted by Load")
			}
			if bad, err := NewManager(path); err == nil {
				bad.Stop()
				t.Fatal("invalid retry backoff accepted by NewManager")
			}
			if err := manager.Reload(); err == nil || manager.Get().LLMSubtrans.RetryBackoffSeconds == nil || *manager.Get().LLMSubtrans.RetryBackoffSeconds != 3.0 {
				t.Fatalf("invalid reload replaced valid config: %+v error=%v", manager.Get(), err)
			}
		})
	}
}

func TestLoadAndReloadTranslationRequestTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig(t, path, false)
	cfg, err := Load(path)
	if err != nil || cfg.LLMSubtrans.RequestTimeoutSeconds != 180 {
		t.Fatalf("load default=%+v error=%v", cfg, err)
	}
	manager, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	for _, setting := range []struct {
		value string
		want  int
	}{{"300", 300}, {"180", 180}, {"", 180}} {
		content := "dry_run: false\n"
		if setting.value != "" {
			content += "llm_subtrans:\n  request_timeout_seconds: " + setting.value + "\n"
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil || cfg.LLMSubtrans.RequestTimeoutSeconds != setting.want {
			t.Fatalf("load=%+v want=%d error=%v", cfg, setting.want, err)
		}
		if err := manager.Reload(); err != nil || manager.Get().LLMSubtrans.RequestTimeoutSeconds != setting.want {
			t.Fatalf("reload=%+v want=%d error=%v", manager.Get(), setting.want, err)
		}
	}
}

func TestTranslationRequestTimeoutRejectsInvalidValuesWithoutReplacingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("llm_subtrans:\n  request_timeout_seconds: 300\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	for _, value := range []string{"0", "-1", "1.5", "invalid"} {
		t.Run(value, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("llm_subtrans:\n  request_timeout_seconds: "+value+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid request timeout accepted by Load")
			}
			if bad, err := NewManager(path); err == nil {
				bad.Stop()
				t.Fatal("invalid request timeout accepted by NewManager")
			}
			if err := manager.Reload(); err == nil || manager.Get().LLMSubtrans.RequestTimeoutSeconds != 300 {
				t.Fatalf("invalid reload replaced valid config: %+v error=%v", manager.Get(), err)
			}
		})
	}
	for _, value := range []uint16{1, 600, 0} {
		t.Run("normalized "+strconv.Itoa(int(value)), func(t *testing.T) {
			if err := os.WriteFile(path, []byte("llm_subtrans:\n  request_timeout_seconds: "+strconv.Itoa(int(value))+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if value == 0 && err == nil {
				t.Fatal("zero request timeout accepted by Load")
			}
			if value != 0 && err != nil {
				t.Fatalf("integer request timeout rejected: %v", err)
			}
		})
	}
}

func TestLoadAndReloadTranslationMaxRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig(t, path, false)
	cfg, err := Load(path)
	if err != nil || cfg.LLMSubtrans.MaxRetries != 1 {
		t.Fatalf("load default=%+v error=%v", cfg, err)
	}
	manager, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	for _, setting := range []struct {
		value string
		want  int
	}{{"5", 5}, {"1", 1}, {"", 1}} {
		content := "dry_run: false\n"
		if setting.value != "" {
			content += "llm_subtrans:\n  max_retries: " + setting.value + "\n"
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(path)
		if err != nil || cfg.LLMSubtrans.MaxRetries != setting.want {
			t.Fatalf("load=%+v want=%d error=%v", cfg, setting.want, err)
		}
		if err := manager.Reload(); err != nil || manager.Get().LLMSubtrans.MaxRetries != setting.want {
			t.Fatalf("reload=%+v want=%d error=%v", manager.Get(), setting.want, err)
		}
	}
}

func TestTranslationMaxRetriesRejectsInvalidValuesWithoutReplacingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("llm_subtrans:\n  max_retries: 5\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	for _, value := range []string{"0", "-1", "1.5", "invalid"} {
		t.Run(value, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("llm_subtrans:\n  max_retries: "+value+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid retry count accepted by Load")
			}
			if bad, err := NewManager(path); err == nil {
				bad.Stop()
				t.Fatal("invalid retry count accepted by NewManager")
			}
			if err := manager.Reload(); err == nil || manager.Get().LLMSubtrans.MaxRetries != 5 {
				t.Fatalf("invalid reload replaced valid config: %+v error=%v", manager.Get(), err)
			}
		})
	}
	for _, value := range []uint16{1, 400, 0} {
		t.Run("normalized "+strconv.Itoa(int(value)), func(t *testing.T) {
			if err := os.WriteFile(path, []byte("llm_subtrans:\n  max_retries: "+strconv.Itoa(int(value))+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if value == 0 && err == nil {
				t.Fatal("zero retries accepted by Load")
			}
			if value != 0 && err != nil {
				t.Fatalf("integer retries rejected: %v", err)
			}
		})
	}
}

func TestTranslationRateLimitRejectsInvalidValuesWithoutReplacingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("llm_subtrans:\n  rate_limit: 80\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(path)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	for _, value := range []string{"-1", ".nan", ".inf", "-.inf"} {
		t.Run(value, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("llm_subtrans:\n  rate_limit: "+value+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("invalid rate accepted by Load")
			}
			if bad, err := NewManager(path); err == nil {
				bad.Stop()
				t.Fatal("invalid rate accepted by NewManager")
			}
			if err := manager.Reload(); err == nil || manager.Get().LLMSubtrans.RateLimit != 80 {
				t.Fatalf("invalid reload replaced valid config: %+v error=%v", manager.Get(), err)
			}
		})
	}
}

// log_level is read from the file; logger.SetConfigLevel validates and applies it.
func TestLogLevelIsLoaded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("log_level: debug\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}
