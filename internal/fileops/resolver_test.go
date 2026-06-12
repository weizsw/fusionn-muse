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

func TestResolveMediaUsesDelimitedCodeInMessyFilename(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	name := "azumi+mizushima+havd+837+reduced+mosaic+new+wife+and+stepfather_720p.mp4"
	video := filepath.Join(folder, name)
	mustWriteSizedFile(t, video, MinVideoSize+1)

	got, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: name,
		StagingDir:  filepath.Join(root, "staging"),
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if got.SourcePath != video {
		t.Fatalf("SourcePath = %q, want %q", got.SourcePath, video)
	}
	if got.FileName != "HAVD-837.mp4" {
		t.Fatalf("FileName = %q, want HAVD-837.mp4", got.FileName)
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

func TestResolveMediaUsesAncestorCodeForDirectNestedVideo(t *testing.T) {
	root := t.TempDir()
	video := filepath.Join(root, "SSNI-083", "BDMV", "STREAM", "00001.m2ts")
	mustWriteSizedFile(t, video, MinVideoSize+1)

	got, err := ResolveMedia(ResolveRequest{
		Path:        video,
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

func TestResolveMediaClassifiesMissingCodeAsNoValidMedia(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	mustMkdir(t, folder)
	mustWriteSizedFile(t, filepath.Join(folder, "movie.mp4"), MinVideoSize+1)

	_, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "no code here",
		StagingDir:  filepath.Join(root, "staging"),
	})
	if !errors.Is(err, ErrNoValidMedia) {
		t.Fatalf("ResolveMedia error = %v, want ErrNoValidMedia", err)
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

func TestResolveMediaUsesCandidateParentCodeForNestedVideo(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	video := filepath.Join(folder, "SSNI-083", "movie.mp4")
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
}

func TestResolveMediaPrefersFilenameCodeOverLargerFallbackVideo(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	coded := filepath.Join(folder, "JUR-123.mp4")
	fallback := filepath.Join(folder, "movie.mp4")
	mustWriteSizedFile(t, coded, MinVideoSize+1)
	mustWriteSizedFile(t, fallback, MinVideoSize+2)

	got, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if got.SourcePath != coded {
		t.Fatalf("SourcePath = %q, want filename-coded video %q", got.SourcePath, coded)
	}
	if got.FileName != "JUR-123.mp4" {
		t.Fatalf("FileName = %q, want JUR-123.mp4", got.FileName)
	}
}

func TestResolveMediaDoesNotTreatCompactCodeAsNumericMultipart(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	video := filepath.Join(folder, "abcd1234.mp4")
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
	if got.FileName != "ABCD-1234.mp4" {
		t.Fatalf("FileName = %q, want ABCD-1234.mp4", got.FileName)
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

func TestResolveMediaPreparesDirectImageToStaging(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1024)
	staging := filepath.Join(root, "staging")
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "BDMV", "STREAM", "00001.m2ts"), MinVideoSize+1)
	})

	got, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "SSNI-083",
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
	if got.StagingPath != wantPath {
		t.Fatalf("StagingPath = %q, want %q", got.StagingPath, wantPath)
	}
	if got.FileName != "SSNI-083.mkv" {
		t.Fatalf("FileName = %q, want SSNI-083.mkv", got.FileName)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want extraction and remux", len(runner.calls))
	}
	if runner.calls[1].name != "ffmpeg" {
		t.Fatalf("second command = %q, want ffmpeg", runner.calls[1].name)
	}
}

func TestResolveMediaUsesParentCodeForDirectImage(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	image := filepath.Join(folder, "disc.iso")
	mustWriteSizedFile(t, image, 1024)
	staging := filepath.Join(root, "staging")
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "BDMV", "STREAM", "00001.m2ts"), MinVideoSize+1)
	})

	got, err := ResolveMedia(ResolveRequest{
		Path:        image,
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
	if got.Code != "SSNI-083" {
		t.Fatalf("Code = %q, want SSNI-083", got.Code)
	}
}

func TestResolveMediaUsesAncestorCodeForDirectNestedImage(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083", "DVD", "disc.iso")
	mustWriteSizedFile(t, image, 1024)
	staging := filepath.Join(root, "staging")
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "feature.mp4"), MinVideoSize+1)
	})

	got, err := ResolveMedia(ResolveRequest{
		Path:        image,
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
	if got.Code != "SSNI-083" {
		t.Fatalf("Code = %q, want SSNI-083", got.Code)
	}
}

func TestIsImageFileRecognizesImageExtensions(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "disc.iso", want: true},
		{path: "disc.ISO", want: true},
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

func TestResolveMediaUsesImageWhenFolderHasNoVideoCandidate(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	image := filepath.Join(folder, "disc.iso")
	mustWriteSizedFile(t, image, 1024)
	staging := filepath.Join(root, "staging")
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "BDMV", "STREAM", "00001.m2ts"), MinVideoSize+1)
	})

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
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want extraction and remux", len(runner.calls))
	}
}

func TestResolveMediaExtractsImageOutsideStagingAndCleansUp(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1024)
	staging := filepath.Join(root, "staging")
	var extractDir string
	runner := fakeImageRunner(t, func(outDir string) {
		extractDir = outDir
		mustWriteSizedFile(t, filepath.Join(outDir, "feature.mp4"), MinVideoSize+1)
	})

	_, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "fallback-name",
		StagingDir:  staging,
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if extractDir == "" {
		t.Fatal("fake image runner did not record extraction dir")
	}
	if pathContains(staging, extractDir) {
		t.Fatalf("extractDir = %q, want outside staging %q", extractDir, staging)
	}
	if Exists(extractDir) {
		t.Fatalf("extractDir = %q still exists after preparation", extractDir)
	}
}

func TestResolveMediaPrefersFilenameCodeOverLargerFallbackImage(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	coded := filepath.Join(folder, "JUR-123.iso")
	fallback := filepath.Join(folder, "disc.iso")
	mustWriteSizedFile(t, coded, 1024)
	mustWriteSizedFile(t, fallback, 2048)
	staging := filepath.Join(root, "staging")
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "feature.mp4"), MinVideoSize+1)
	})

	got, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "fallback-name",
		StagingDir:  staging,
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if got.Code != "JUR-123" {
		t.Fatalf("Code = %q, want JUR-123 from filename-coded image", got.Code)
	}
}

func TestResolveMediaUsesRootCodeForNestedFolderImage(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	image := filepath.Join(folder, "DVD", "disc.iso")
	mustWriteSizedFile(t, image, 1024)
	staging := filepath.Join(root, "staging")
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "BDMV", "STREAM", "00001.m2ts"), MinVideoSize+1)
	})

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
	if got.Code != "SSNI-083" {
		t.Fatalf("Code = %q, want SSNI-083", got.Code)
	}
}

func TestResolveMediaUsesImageParentCodeUnderGenericFolder(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	image := filepath.Join(folder, "SSNI-083", "disc.iso")
	mustWriteSizedFile(t, image, 1024)
	staging := filepath.Join(root, "staging")
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "BDMV", "STREAM", "00001.m2ts"), MinVideoSize+1)
	})

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
	if got.Code != "SSNI-083" {
		t.Fatalf("Code = %q, want SSNI-083", got.Code)
	}
}

func TestResolveMediaRejectsImageExtractionSourceOverlap(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "media-extract", "SSNI-083-image")
	image := filepath.Join(folder, "disc.iso")
	mustWriteSizedFile(t, image, 1024)

	_, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      fakeImageRunner(t, func(outDir string) {}),
	})
	if err == nil {
		t.Fatal("ResolveMedia returned nil error, want extraction overlap error")
	}
	if !strings.Contains(err.Error(), "image extraction dir overlaps source path") {
		t.Fatalf("error = %q, want extraction overlap error", err)
	}
	if !Exists(image) {
		t.Fatal("source image was removed")
	}
}

func TestResolveMediaPrefersNormalVideoOverFolderImage(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	video := filepath.Join(folder, "SSNI-083.mp4")
	image := filepath.Join(folder, "SSNI-083.iso")
	mustWriteSizedFile(t, video, MinVideoSize+1)
	mustWriteSizedFile(t, image, 1024)
	runner := &fakeRunner{}

	got, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if got.SourcePath != video {
		t.Fatalf("SourcePath = %q, want normal video %q", got.SourcePath, video)
	}
	if got.StagingPath != "" {
		t.Fatalf("StagingPath = %q, want empty for normal video", got.StagingPath)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %d, want no image preparation", len(runner.calls))
	}
}

func TestResolveMediaSelectsPlainExtractedMediaFromImage(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1024)
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "feature.mp4"), MinVideoSize+1)
	})

	_, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	wantInput := filepath.Join(imageExtractionDir(filepath.Join(root, "staging"), "SSNI-083"), "feature.mp4")
	if runner.calls[1].args[2] != wantInput {
		t.Fatalf("remux input = %q, want plain extracted media %q", runner.calls[1].args[2], wantInput)
	}
}

func TestResolveMediaDetectsChineseSubtitleInExtractedMedia(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1024)
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "feature-C.mp4"), MinVideoSize+1)
	})

	got, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if !got.HasChineseSubtitle {
		t.Fatal("HasChineseSubtitle = false, want true from extracted media name")
	}
}

func TestResolveMediaClearsStaleImageExtractionDir(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1024)
	staging := filepath.Join(root, "staging")
	stale := filepath.Join(imageExtractionDir(staging, "SSNI-083"), "BDMV", "STREAM", "stale.m2ts")
	mustWriteSizedFile(t, stale, MinVideoSize+100)
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "feature.mp4"), MinVideoSize+1)
	})

	_, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "fallback-name",
		StagingDir:  staging,
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale extracted media still exists, stat error = %v", err)
	}
	wantInput := filepath.Join(imageExtractionDir(staging, "SSNI-083"), "feature.mp4")
	if runner.calls[1].args[2] != wantInput {
		t.Fatalf("remux input = %q, want current extracted media %q", runner.calls[1].args[2], wantInput)
	}
}

func TestResolveMediaIgnoresTinyBluRayStreamFromImage(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1024)
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "BDMV", "STREAM", "00001.m2ts"), MinVideoSize)
	})

	_, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      runner,
	})
	if err == nil {
		t.Fatal("ResolveMedia returned nil error, want no extracted media error")
	}
	if !strings.Contains(err.Error(), "no media found in extracted image") {
		t.Fatalf("error = %q, want no extracted media error", err)
	}
}

func TestResolveMediaIgnoresTinyDVDTitleChainFromImage(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1024)
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "VIDEO_TS", "VTS_01_1.VOB"), MinVideoSize)
	})

	_, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      runner,
	})
	if err == nil {
		t.Fatal("ResolveMedia returned nil error, want no extracted media error")
	}
	if !strings.Contains(err.Error(), "no media found in extracted image") {
		t.Fatalf("error = %q, want no extracted media error", err)
	}
}

func TestResolveMediaReturnsImageExtractionError(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1024)
	runner := &fakeRunner{errors: []error{errors.New("bsdtar failed"), errors.New("7z failed")}}

	_, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      runner,
	})
	if err == nil {
		t.Fatal("ResolveMedia returned nil error, want extraction error")
	}
	if !strings.Contains(err.Error(), "image extraction failed") {
		t.Fatalf("error = %q, want image extraction failed", err)
	}
	if errors.Is(err, ErrNoValidMedia) {
		t.Fatalf("error = %v, want preparation failure not ErrNoValidMedia", err)
	}
}

func TestResolveMediaReturnsNoExtractedMediaError(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1024)
	runner := fakeImageRunner(t, func(outDir string) {})

	_, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      runner,
	})
	if err == nil {
		t.Fatal("ResolveMedia returned nil error, want no extracted media error")
	}
	if !strings.Contains(err.Error(), "no media found in extracted image") {
		t.Fatalf("error = %q, want no extracted media error", err)
	}
}

func TestResolveMediaSelectsLargestBluRayStreamFromImage(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1024)
	runner := fakeImageRunner(t, func(outDir string) {
		mustWriteSizedFile(t, filepath.Join(outDir, "BDMV", "STREAM", "00001.m2ts"), MinVideoSize+1)
		mustWriteSizedFile(t, filepath.Join(outDir, "BDMV", "STREAM", "00002.m2ts"), MinVideoSize+2)
	})

	_, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "SSNI-083",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want extraction and remux", len(runner.calls))
	}
	wantInput := filepath.Join(imageExtractionDir(filepath.Join(root, "staging"), "SSNI-083"), "BDMV", "STREAM", "00002.m2ts")
	if runner.calls[1].args[2] != wantInput {
		t.Fatalf("remux input = %q, want largest stream %q", runner.calls[1].args[2], wantInput)
	}
}

func TestResolveMediaSelectsLargestDVDTitleChainFromImage(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "SSNI-083.iso")
	mustWriteSizedFile(t, image, 1024)
	var concatList string
	runner := fakeImageRunner(t, func(outDir string) {
		videoTS := filepath.Join(outDir, "VIDEO_TS")
		mustWriteSizedFile(t, filepath.Join(videoTS, "VTS_01_0.VOB"), MinVideoSize+50)
		mustWriteSizedFile(t, filepath.Join(videoTS, "VTS_01_1.VOB"), MinVideoSize+1)
		mustWriteSizedFile(t, filepath.Join(videoTS, "VTS_02_0.VOB"), 1)
		mustWriteSizedFile(t, filepath.Join(videoTS, "VTS_02_1.VOB"), MinVideoSize+2)
		mustWriteSizedFile(t, filepath.Join(videoTS, "VTS_02_2.VOB"), MinVideoSize+2)
	})
	runner.onRun = captureConcatList(t, runner.onRun, &concatList)

	_, err := ResolveMedia(ResolveRequest{
		Path:        image,
		TorrentName: "SSNI-083",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want extraction and concat", len(runner.calls))
	}
	got := concatList
	extractDir := imageExtractionDir(filepath.Join(root, "staging"), "SSNI-083")
	first := filepath.Join(extractDir, "VIDEO_TS", "VTS_02_1.VOB")
	second := filepath.Join(extractDir, "VIDEO_TS", "VTS_02_2.VOB")
	if !strings.Contains(got, first) || !strings.Contains(got, second) {
		t.Fatalf("concat list = %q, want largest VTS_02 chain", got)
	}
	if strings.Contains(got, "VTS_01_0.VOB") || strings.Contains(got, "VTS_02_0.VOB") {
		t.Fatalf("concat list = %q, want menu VOBs ignored", got)
	}
	if strings.Index(got, "VTS_02_1.VOB") > strings.Index(got, "VTS_02_2.VOB") {
		t.Fatalf("concat list = %q, want VTS_02 parts ordered by filename", got)
	}
}

func TestSelectDVDTitleChainBreaksEqualSizeTieByTitle(t *testing.T) {
	root := t.TempDir()
	videoTS := filepath.Join(root, "VIDEO_TS")
	mustWriteSizedFile(t, filepath.Join(videoTS, "VTS_02_1.VOB"), MinVideoSize+1)
	mustWriteSizedFile(t, filepath.Join(videoTS, "VTS_01_1.VOB"), MinVideoSize+1)

	got := selectDVDTitleChain(root)
	if len(got) != 1 {
		t.Fatalf("len(parts) = %d, want 1", len(got))
	}
	if filepath.Base(got[0]) != "VTS_01_1.VOB" {
		t.Fatalf("selected part = %q, want VTS_01_1.VOB", filepath.Base(got[0]))
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

func TestResolveMediaPreparesMultipartWithCandidateParentCode(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "download")
	files := []string{"movie-part1.m2ts", "movie-part2.m2ts"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(folder, "SSNI-083", name), MinVideoSize+1)
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
	if got.Code != "SSNI-083" {
		t.Fatalf("Code = %q, want SSNI-083", got.Code)
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

func TestResolveMediaIgnoresIncompleteMultipartWhenValidNormalCandidateExists(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	valid := filepath.Join(folder, "SSNI-083.mp4")
	incomplete := filepath.Join(folder, "bonus-part2.mkv")
	mustWriteSizedFile(t, valid, MinVideoSize+1)
	mustWriteSizedFile(t, incomplete, MinVideoSize+2)

	got, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if got.SourcePath != valid {
		t.Fatalf("SourcePath = %q, want valid normal candidate %q", got.SourcePath, valid)
	}
}

func TestResolveMediaPrefersFilenameCodedNormalVideoOverFallbackMultipart(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "SSNI-083")
	valid := filepath.Join(folder, "SSNI-083.mp4")
	mustWriteSizedFile(t, valid, MinVideoSize+1)
	mustWriteSizedFile(t, filepath.Join(folder, "bonus-part1.mkv"), MinVideoSize+2)
	mustWriteSizedFile(t, filepath.Join(folder, "bonus-part2.mkv"), MinVideoSize+3)
	runner := &fakeRunner{}

	got, err := ResolveMedia(ResolveRequest{
		Path:        folder,
		TorrentName: "fallback-name",
		StagingDir:  filepath.Join(root, "staging"),
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("ResolveMedia returned error: %v", err)
	}
	if got.SourcePath != valid {
		t.Fatalf("SourcePath = %q, want filename-coded normal video %q", got.SourcePath, valid)
	}
	if got.StagingPath != "" {
		t.Fatalf("StagingPath = %q, want empty for normal video", got.StagingPath)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %d, want no multipart preparation", len(runner.calls))
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

func TestFindMultipartSetRejectsDifferentPartBases(t *testing.T) {
	root := t.TempDir()
	files := []string{"ABC-001-feature-part1.wmv", "ABC-001-bonus-part2.wmv"}
	for _, name := range files {
		mustWriteSizedFile(t, filepath.Join(root, name), MinVideoSize+1)
	}
	videos, _, err := findMediaCandidates(context.Background(), root)
	if err != nil {
		t.Fatalf("findMediaCandidates: %v", err)
	}

	got := findMultipartSet(videos, root, "")
	if len(got) != 0 {
		t.Fatalf("len(parts) = %d, want 0 for different part bases", len(got))
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

func TestFindMultipartSetNumericOrderWithReleaseNoise(t *testing.T) {
	root := t.TempDir()
	files := []string{"soe00967hhb2.FHD.wmv", "soe00967hhb1.FHD.wmv"}
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
	if filepath.Base(got[0]) != "soe00967hhb1.FHD.wmv" || filepath.Base(got[1]) != "soe00967hhb2.FHD.wmv" {
		t.Fatalf("parts order = %v, want numeric order with release noise", got)
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

func fakeImageRunner(t *testing.T, writeExtractedMedia func(outDir string)) *fakeRunner {
	t.Helper()
	return &fakeRunner{
		onRun: func(name string, args ...string) error {
			switch name {
			case "bsdtar":
				for i := 0; i < len(args)-1; i++ {
					if args[i] == "-C" {
						writeExtractedMedia(args[i+1])
						return nil
					}
				}
			case "7z":
				for _, arg := range args {
					if strings.HasPrefix(arg, "-o") {
						writeExtractedMedia(strings.TrimPrefix(arg, "-o"))
						return nil
					}
				}
			}
			return nil
		},
	}
}

func captureConcatList(t *testing.T, next func(name string, args ...string) error, dest *string) func(name string, args ...string) error {
	t.Helper()
	return func(name string, args ...string) error {
		if name == "ffmpeg" {
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "-i" {
					data, err := os.ReadFile(args[i+1])
					if err != nil {
						t.Fatalf("read concat list: %v", err)
					}
					*dest = string(data)
					break
				}
			}
		}
		if next != nil {
			return next(name, args...)
		}
		return nil
	}
}
