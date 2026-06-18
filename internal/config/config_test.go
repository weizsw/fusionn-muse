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
