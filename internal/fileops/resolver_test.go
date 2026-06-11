package fileops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	video := filepath.Join(folder, "movie.mp4")
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

func TestResolveMediaReturnsCancelledContextError(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	mustWriteSizedFile(t, filepath.Join(folder, "movie.mp4"), MinVideoSize+1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ResolveMedia(ResolveRequest{
		Context:     ctx,
		Path:        folder,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveMedia error = %v, want context.Canceled", err)
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

func TestResolveMediaUsesRootFolderCodeForNestedVideo(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	video := filepath.Join(folder, "BDMV", "STREAM", "00001.m2ts")
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
	if got.FileName != "SSNI-083.m2ts" {
		t.Fatalf("FileName = %q, want SSNI-083.m2ts", got.FileName)
	}
}

func TestResolveMediaAcceptsUppercaseM2TS(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	video := filepath.Join(folder, "BDMV", "STREAM", "00001.M2TS")
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
	if got.FileName != "SSNI-083.m2ts" {
		t.Fatalf("FileName = %q, want SSNI-083.m2ts", got.FileName)
	}
}

func TestResolveMediaDirectImageReturnsPlaceholderError(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1)

	_, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "SSNI-083",
		StagingDir:  filepath.Join(root, "staging"),
	})
	if err == nil {
		t.Fatal("ResolveMedia returned nil error, want image placeholder error")
	}
	if !strings.Contains(err.Error(), "image preparation not implemented") {
		t.Fatalf("error = %q, want image preparation placeholder", err)
	}
}

func TestIsImageFileRecognizesImageExtensions(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "disc.iso", want: true},
		{path: "disc.nrg", want: true},
		{path: "disc.img", want: true},
		{path: "disc.mdf", want: true},
		{path: "disc.bin", want: true},
		{path: "disc.txt", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsImageFile(tt.path); got != tt.want {
				t.Fatalf("IsImageFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestFindMediaCandidatesCollectsImages(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1)

	_, images, err := findMediaCandidates(context.Background(), root)
	if err != nil {
		t.Fatalf("findMediaCandidates returned error: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("len(images) = %d, want 1", len(images))
	}
	if images[0].Path != image {
		t.Fatalf("image path = %q, want %q", images[0].Path, image)
	}
	if images[0].Name != "SSNI-083.iso" {
		t.Fatalf("image name = %q, want SSNI-083.iso", images[0].Name)
	}
}

func TestResolveMediaPreparesMultipartToStaging(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	files := []string{"ABC-001-part1.wmv", "ABC-001-part2.wmv"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(folder, name), MinVideoSize+1)
	}
	staging := filepath.Join(root, "staging")
	runner := &fakeRunner{}

	got, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "fallback-name",
		StagingDir:  staging,
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}

	wantPath := filepath.Join(staging, "ABC-001.mkv")
	if got.SourcePath != wantPath {
		t.Fatalf("SourcePath = %q, want %q", got.SourcePath, wantPath)
	}
	if got.StagingPath != wantPath {
		t.Fatalf("StagingPath = %q, want %q", got.StagingPath, wantPath)
	}
	if got.FileName != "ABC-001.mkv" {
		t.Fatalf("FileName = %q, want ABC-001.mkv", got.FileName)
	}
	if len(runner.calls) != 1 || runner.calls[0].name != "ffmpeg" {
		t.Fatalf("runner calls = %#v, want one ffmpeg call", runner.calls)
	}
}

func TestResolveMediaPreparesNestedMultipartWithRootCode(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	files := []string{"movie-part1.m2ts", "movie-part2.m2ts"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(folder, "BDMV", "STREAM", name), MinVideoSize+1)
	}
	staging := filepath.Join(root, "staging")
	runner := &fakeRunner{}

	got, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "fallback-name",
		StagingDir:  staging,
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}

	wantPath := filepath.Join(staging, "SSNI-083.mkv")
	if got.SourcePath != wantPath {
		t.Fatalf("SourcePath = %q, want %q", got.SourcePath, wantPath)
	}
	if got.FileName != "SSNI-083.mkv" {
		t.Fatalf("FileName = %q, want SSNI-083.mkv", got.FileName)
	}
}

func TestResolveMediaRejectsMultipartMissingPartOne(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	files := []string{"movie-part2.m2ts", "movie-part3.m2ts"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(folder, "BDMV", "STREAM", name), MinVideoSize+1)
	}

	_, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      &fakeRunner{},
	})
	if err == nil {
		t.Fatal("ResolveMedia returned nil error, want incomplete multipart error")
	}
	if !strings.Contains(err.Error(), "incomplete multipart") {
		t.Fatalf("error = %q, want incomplete multipart error", err)
	}
}

func TestResolveMediaRejectsLoneNonFirstMultipart(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	mustWriteSizedFile(t, filepath.Join(folder, "movie-part2.m2ts"), MinVideoSize+1)

	_, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      &fakeRunner{},
	})
	if err == nil {
		t.Fatal("ResolveMedia returned nil error, want incomplete multipart error")
	}
	if !strings.Contains(err.Error(), "incomplete multipart") {
		t.Fatalf("error = %q, want incomplete multipart error", err)
	}
}

func TestFindMultipartSetLetterOrder(t *testing.T) {
	root := t.TempDir()
	files := []string{"pppd176A.FHD.wmv", "pppd176B.FHD.wmv", "pppd176C.FHD.wmv"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(root, name), MinVideoSize+1)
	}
	videos, _, err := findMediaCandidates(context.Background(), root)
	if err != nil {
		t.Fatalf("findMediaCandidates: %v", err)
	}

	got := findMultipartSet(videos, root, "")
	if len(got) != 3 {
		t.Fatalf("len(parts) = %d, want 3", len(got))
	}
	want := []string{"pppd176A.FHD.wmv", "pppd176B.FHD.wmv", "pppd176C.FHD.wmv"}
	for i := range want {
		if filepath.Base(got[i]) != want[i] {
			t.Fatalf("part %d = %q, want %q", i, filepath.Base(got[i]), want[i])
		}
	}
}

func TestFindMultipartSetHyphenatedLetterOrder(t *testing.T) {
	root := t.TempDir()
	files := []string{"ABC-001B.wmv", "ABC-001A.wmv"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(root, name), MinVideoSize+1)
	}
	videos, _, err := findMediaCandidates(context.Background(), root)
	if err != nil {
		t.Fatalf("findMediaCandidates: %v", err)
	}

	got := findMultipartSet(videos, root, "")
	if len(got) != 2 {
		t.Fatalf("len(parts) = %d, want 2", len(got))
	}
	if filepath.Base(got[0]) != "ABC-001A.wmv" || filepath.Base(got[1]) != "ABC-001B.wmv" {
		t.Fatalf("parts order = %v, want hyphenated letter order", got)
	}
}

func TestFindMultipartSetRejectsMixedExtensions(t *testing.T) {
	root := t.TempDir()
	files := []string{"ABC-001-part1.wmv", "ABC-001-part2.mp4"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(root, name), MinVideoSize+1)
	}
	videos, _, err := findMediaCandidates(context.Background(), root)
	if err != nil {
		t.Fatalf("findMediaCandidates: %v", err)
	}

	got := findMultipartSet(videos, root, "")
	if len(got) != 0 {
		t.Fatalf("len(parts) = %d, want 0 for mixed extensions", len(got))
	}
}

func TestFindMultipartSetNumericOrder(t *testing.T) {
	root := t.TempDir()
	files := []string{"soe00967hhb2.wmv", "soe00967hhb1.wmv"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(root, name), MinVideoSize+1)
	}
	videos, _, err := findMediaCandidates(context.Background(), root)
	if err != nil {
		t.Fatalf("findMediaCandidates: %v", err)
	}

	got := findMultipartSet(videos, root, "")
	if len(got) != 2 {
		t.Fatalf("len(parts) = %d, want 2", len(got))
	}
	if filepath.Base(got[0]) != "soe00967hhb1.wmv" || filepath.Base(got[1]) != "soe00967hhb2.wmv" {
		t.Fatalf("parts order = %v, want numeric order", got)
	}
}

func TestFindMultipartSetExplicitMarkers(t *testing.T) {
	tests := []struct {
		name  string
		files []string
	}{
		{name: "part", files: []string{"ABC-001-part2.wmv", "ABC-001-part1.wmv"}},
		{name: "cd", files: []string{"ABC-001-cd2.wmv", "ABC-001-cd1.wmv"}},
		{name: "disc", files: []string{"ABC-001-disc2.wmv", "ABC-001-disc1.wmv"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range tt.files {
				mustWriteSizedFile(t, filepath.Join(root, name), MinVideoSize+1)
			}
			videos, _, err := findMediaCandidates(context.Background(), root)
			if err != nil {
				t.Fatalf("findMediaCandidates: %v", err)
			}

			got := findMultipartSet(videos, root, "")
			if len(got) != 2 {
				t.Fatalf("len(parts) = %d, want 2", len(got))
			}
			if filepath.Base(got[0]) != tt.files[1] || filepath.Base(got[1]) != tt.files[0] {
				t.Fatalf("parts order = %v, want explicit marker order", got)
			}
		})
	}
}

func TestFindMultipartSetUsesTorrentNameCodeFallback(t *testing.T) {
	root := t.TempDir()
	files := []string{"movie-part2.wmv", "movie-part1.wmv"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(root, name), MinVideoSize+1)
	}
	videos, _, err := findMediaCandidates(context.Background(), root)
	if err != nil {
		t.Fatalf("findMediaCandidates: %v", err)
	}

	got := findMultipartSet(videos, root, "ABC-001")
	if len(got) != 2 {
		t.Fatalf("len(parts) = %d, want 2", len(got))
	}
	if filepath.Base(got[0]) != "movie-part1.wmv" || filepath.Base(got[1]) != "movie-part2.wmv" {
		t.Fatalf("parts order = %v, want torrent fallback order", got)
	}
}

func TestFindMultipartSetRejectsDuplicateMarkers(t *testing.T) {
	root := t.TempDir()
	files := []string{"soe00967hhb1.wmv", "SOE-967-part1.wmv"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(root, name), MinVideoSize+1)
	}
	videos, _, err := findMediaCandidates(context.Background(), root)
	if err != nil {
		t.Fatalf("findMediaCandidates: %v", err)
	}

	got := findMultipartSet(videos, root, "")
	if len(got) != 0 {
		t.Fatalf("len(parts) = %d, want 0 for duplicate markers", len(got))
	}
}

func TestFindMultipartSetRejectsMissingPartOne(t *testing.T) {
	root := t.TempDir()
	files := []string{"ABC-001-part2.wmv", "ABC-001-part3.wmv"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(root, name), MinVideoSize+1)
	}
	videos, _, err := findMediaCandidates(context.Background(), root)
	if err != nil {
		t.Fatalf("findMediaCandidates: %v", err)
	}

	got := findMultipartSet(videos, root, "")
	if len(got) != 0 {
		t.Fatalf("len(parts) = %d, want 0 for missing part one", len(got))
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
