package config

import (
	"os"
	"path/filepath"
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
	if err := aMgr.v.ReadInConfig(); err != nil {
		t.Fatalf("read a config: %v", err)
	}
	aMgr.reload()

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
