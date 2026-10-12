package config

import (
	"os"
	"path/filepath"
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
