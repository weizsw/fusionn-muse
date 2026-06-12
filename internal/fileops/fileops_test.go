package fileops

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fusionn-muse/pkg/logger"
)

func init() {
	logger.Init(true)
}

func TestHardlinkOrCopyDoesNotTruncateSourceWhenDestinationIsStaleHardlink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.mp4")
	staging := filepath.Join(root, "staging", "source.mp4")
	content := []byte("video content")
	if err := os.WriteFile(source, content, 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(staging), 0755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.Link(source, staging); err != nil {
		t.Fatalf("create stale staging hardlink: %v", err)
	}

	if err := HardlinkOrCopy(source, staging); err != nil {
		t.Fatalf("HardlinkOrCopy returned error: %v", err)
	}

	got, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("source content = %q, want %q", got, content)
	}
	if !sameInode(t, source, staging) {
		t.Fatal("staging is not linked to source after HardlinkOrCopy")
	}
}

func TestMoveRemovesSourceWhenDestinationIsSameInode(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "processing", "source.mp4")
	failed := filepath.Join(root, "failed", "source.mp4")
	if err := os.MkdirAll(filepath.Dir(source), 0755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(failed), 0755); err != nil {
		t.Fatalf("mkdir failed dir: %v", err)
	}
	if err := os.WriteFile(source, []byte("video content"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.Link(source, failed); err != nil {
		t.Fatalf("create stale failed hardlink: %v", err)
	}

	if err := Move(source, failed); err != nil {
		t.Fatalf("Move returned error: %v", err)
	}

	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists after same-inode move, stat error = %v", err)
	}
	if _, err := os.Stat(failed); err != nil {
		t.Fatalf("failed destination missing: %v", err)
	}
}

func sameInode(t *testing.T, a, b string) bool {
	t.Helper()
	aInfo, err := os.Stat(a)
	if err != nil {
		t.Fatalf("stat %s: %v", a, err)
	}
	bInfo, err := os.Stat(b)
	if err != nil {
		t.Fatalf("stat %s: %v", b, err)
	}
	return os.SameFile(aInfo, bInfo)
}
