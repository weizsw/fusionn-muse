package processor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/queue"
)

func TestPreparationCachesAreIsolatedAndCleanupPreservesOtherInputs(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "input", "SSNI-083-C.iso")
	mustWriteTestFile(t, image, "original image")
	runner := &processorCommandRunner{onRun: func(name string, args ...string) error {
		switch name {
		case "bsdtar":
			media := filepath.Join(args[len(args)-1], "VIDEO_TS", "VTS_01_1.VOB")
			if err := os.MkdirAll(filepath.Dir(media), 0755); err != nil {
				return err
			}
			file, err := os.Create(media)
			if err != nil {
				return err
			}
			if err := file.Truncate(300 * 1024 * 1024); err != nil {
				_ = file.Close()
				return err
			}
			return file.Close()
		case "ffmpeg":
			return os.WriteFile(args[len(args)-1], []byte("prepared media"), 0644)
		default:
			t.Fatalf("unexpected preparation command: %s %v", name, args)
			return nil
		}
	}}
	svc := &Service{folders: config.FoldersConfig{Staging: filepath.Join(root, "staging")}, runner: runner}
	first, cleanupFirst, err := svc.PrepareSubmission(context.Background(), &queue.Submission{ID: "first", RunID: 1, SourcePath: image})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupFirst()
	second, cleanupSecond, err := svc.PrepareSubmission(context.Background(), &queue.Submission{ID: "second", RunID: 2, SourcePath: image})
	if err != nil {
		t.Fatal(err)
	}
	if first.SourcePath == second.SourcePath {
		t.Fatalf("shared cache: %+v / %+v", first, second)
	}
	canonical := filepath.Join(svc.folders.Staging, first.FileName)
	if first.StagingPath != canonical || second.StagingPath != canonical {
		t.Fatalf("prepared media bypasses no-replace admission: %+v / %+v", first, second)
	}
	if err := cleanupSecond(); err != nil {
		t.Fatal(err)
	}
	for _, preserved := range []string{image, first.SourcePath} {
		if _, err := os.Stat(preserved); err != nil {
			t.Fatalf("cleanup removed %s: %v", preserved, err)
		}
	}
	if _, err := os.Stat(second.SourcePath); !os.IsNotExist(err) {
		t.Fatalf("second cache was not removed: %v", err)
	}
}
