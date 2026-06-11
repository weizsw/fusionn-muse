package fileops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMediaUsesFolderCodeForCompactFile(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	mustMkdir(t, folder)
	video := filepath.Join(folder, "ssni00083hhb.mp4")
	mustWriteSizedFile(t, video, MinVideoSize+1)

	got, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if got.SourcePath != video {
		t.Fatalf("SourcePath = %q, want %q", got.SourcePath, video)
	}
	if got.FileName != "SSNI-083.mp4" {
		t.Fatalf("FileName = %q, want SSNI-083.mp4", got.FileName)
	}
	if got.StagingPath != "" {
		t.Fatalf("StagingPath = %q, want empty for direct file", got.StagingPath)
	}
}

func TestResolveMediaFallsBackToTorrentName(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	mustMkdir(t, folder)
	video := filepath.Join(folder, "ssni00083hhb.mp4")
	mustWriteSizedFile(t, video, MinVideoSize+1)

	got, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "SSNI-083",
		StagingDir:  filepath.Join(root, "staging"),
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if got.SourcePath != video {
		t.Fatalf("SourcePath = %q, want %q", got.SourcePath, video)
	}
	if got.FileName != "SSNI-083.mp4" {
		t.Fatalf("FileName = %q, want SSNI-083.mp4", got.FileName)
	}
}

func TestResolveMediaRejectsFolderWithoutCode(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	mustMkdir(t, folder)
	mustWriteSizedFile(t, filepath.Join(folder, "movie.mp4"), MinVideoSize+1)

	_, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "no code here",
		StagingDir:  filepath.Join(root, "staging"),
	})
	if err == nil {
		t.Fatal("ResolveMedia returned nil error, want missing code error")
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteSizedFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatalf("truncate %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}
