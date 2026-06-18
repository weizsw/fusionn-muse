package processor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/pkg/logger"
)

func init() {
	logger.Init(true)
}

func TestMoveToProcessingPreservesPreparedStagingSource(t *testing.T) {
	root := t.TempDir()
	stagingPath := filepath.Join(root, "staging", "SSNI-083.mkv")
	processingPath := filepath.Join(root, "processing", "SSNI-083.mkv")
	if err := os.MkdirAll(filepath.Dir(stagingPath), 0755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.WriteFile(stagingPath, []byte("prepared"), 0644); err != nil {
		t.Fatalf("write staging: %v", err)
	}
	job := queue.NewJob("job1", stagingPath, "SSNI-083.mkv", "SSNI-083", "")
	job.StagingPath = stagingPath

	preserved, err := moveToProcessing(job, stagingPath, processingPath)
	if err != nil {
		t.Fatalf("moveToProcessing returned error: %v", err)
	}
	if !preserved {
		t.Fatal("preserved = false, want true for prepared staging source")
	}
	if !fileExists(stagingPath) {
		t.Fatal("prepared staging source was removed")
	}
	if !fileExists(processingPath) {
		t.Fatal("processing copy was not created")
	}
}

func TestMoveToProcessingMovesNormalStagingFile(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source", "SSNI-083.mp4")
	stagingPath := filepath.Join(root, "staging", "SSNI-083.mp4")
	processingPath := filepath.Join(root, "processing", "SSNI-083.mp4")
	if err := os.MkdirAll(filepath.Dir(stagingPath), 0755); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.WriteFile(stagingPath, []byte("staged"), 0644); err != nil {
		t.Fatalf("write staging: %v", err)
	}
	job := queue.NewJob("job1", sourcePath, "SSNI-083.mp4", "SSNI-083", "")
	job.StagingPath = stagingPath

	preserved, err := moveToProcessing(job, stagingPath, processingPath)
	if err != nil {
		t.Fatalf("moveToProcessing returned error: %v", err)
	}
	if preserved {
		t.Fatal("preserved = true, want false for normal staged source")
	}
	if fileExists(stagingPath) {
		t.Fatal("normal staging file still exists")
	}
	if !fileExists(processingPath) {
		t.Fatal("processing file was not created")
	}
}

func TestProcessCopiesSidecarSubtitleForLightJob(t *testing.T) {
	root := t.TempDir()
	cfgMgr := newTestConfigManager(t, root, "zh-CN")
	defer cfgMgr.Stop()

	folders := config.FoldersConfig{
		Staging:   filepath.Join(root, "staging"),
		Process:   filepath.Join(root, "processing"),
		Scraping:  filepath.Join(root, "scraping"),
		Subtitles: filepath.Join(root, "subtitles"),
		Failed:    filepath.Join(root, "failed"),
	}
	source := filepath.Join(root, "input", "SSNI-083.mp4")
	sidecar := filepath.Join(root, "input", "SSNI-083.ass")
	sidecarContent := "Subtitle: 0,0:00:00.00,0:00:01.00,Default,,0,0,0,,中文字幕"
	mustWriteTestFile(t, source, "video")
	mustWriteTestFile(t, sidecar, sidecarContent)

	svc := New(cfgMgr, nil, folders)
	job := queue.NewJob("job1", source, "SSNI-083.mp4", "SSNI-083", "")
	job.IsLight = true
	job.SubtitleDetectionReason = mediaintake.SubtitleDetectionSidecar
	job.SidecarSubtitlePath = sidecar

	if err := svc.Process(context.Background(), job); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	wantSubtitle := filepath.Join(folders.Subtitles, "SSNI-083.zh-CN.ass")
	got, err := os.ReadFile(wantSubtitle)
	if err != nil {
		t.Fatalf("read copied sidecar: %v", err)
	}
	if string(got) != sidecarContent {
		t.Fatalf("copied sidecar content = %q", got)
	}
	if fileExists(filepath.Join(folders.Subtitles, "SSNI-083.zh-CN.srt")) {
		t.Fatal("dummy subtitle was copied to subtitles folder")
	}
}

func TestProcessUsesOCRToSkipHardSubbedVideo(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	mustMkdir(t, bin)
	mustWriteExecutable(t, filepath.Join(bin, "ffprobe"), "#!/bin/sh\nprintf '100\\n'\n")
	mustWriteExecutable(t, filepath.Join(bin, "ffmpeg"), "#!/bin/sh\nfor last do :; done\ntouch \"$last\"\n")
	mustWriteExecutable(t, filepath.Join(bin, "tesseract"), "#!/bin/sh\nprintf 'visible subtitle text\\n'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfgMgr := newTestConfigManager(t, root, "")
	defer cfgMgr.Stop()
	folders := config.FoldersConfig{
		Staging:   filepath.Join(root, "staging"),
		Process:   filepath.Join(root, "processing"),
		Scraping:  filepath.Join(root, "scraping"),
		Subtitles: filepath.Join(root, "subtitles"),
		Failed:    filepath.Join(root, "failed"),
	}
	source := filepath.Join(root, "input", "SSNI-083.mp4")
	mustWriteTestFile(t, source, "video")

	svc := New(cfgMgr, nil, folders)
	job := queue.NewJob("job1", source, "SSNI-083.mp4", "SSNI-083", "")

	if err := svc.Process(context.Background(), job); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if job.SubtitleDetectionReason != mediaintake.SubtitleDetectionHardSubOCR {
		t.Fatalf("SubtitleDetectionReason = %q, want %q", job.SubtitleDetectionReason, mediaintake.SubtitleDetectionHardSubOCR)
	}
	if !fileExists(filepath.Join(folders.Scraping, "SSNI-083.mp4")) {
		t.Fatal("video was not moved to scraping")
	}
	if fileExists(filepath.Join(folders.Subtitles, "SSNI-083.srt")) {
		t.Fatal("dummy subtitle was copied to subtitles folder")
	}
}

func TestProcessDoesNotCreateDummySubtitleForProductionLightJob(t *testing.T) {
	root := t.TempDir()
	cfgMgr := newTestConfigManager(t, root, "")
	defer cfgMgr.Stop()
	folders := config.FoldersConfig{
		Staging:   filepath.Join(root, "staging"),
		Process:   filepath.Join(root, "processing"),
		Scraping:  filepath.Join(root, "scraping"),
		Subtitles: filepath.Join(root, "subtitles"),
		Failed:    filepath.Join(root, "failed"),
	}
	source := filepath.Join(root, "input", "SSNI-083-C.mp4")
	mustWriteTestFile(t, source, "video")

	svc := New(cfgMgr, nil, folders)
	job := queue.NewJob("job1", source, "SSNI-083-C.mp4", "SSNI-083", "")

	if err := svc.Process(context.Background(), job); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if job.SubtitlePath != "" || job.TranslatedPath != "" {
		t.Fatalf("subtitle paths = %q/%q, want empty for production light job", job.SubtitlePath, job.TranslatedPath)
	}
	scrapingPath := filepath.Join(folders.Scraping, "SSNI-083.mp4")
	if !sameFile(t, source, scrapingPath) {
		t.Fatal("scraping file is not hard-linked to source")
	}
	if fileExists(filepath.Join(folders.Process, "SSNI-083.srt")) {
		t.Fatal("dummy subtitle exists in processing folder")
	}
	if fileExists(filepath.Join(folders.Subtitles, "SSNI-083.srt")) {
		t.Fatal("dummy subtitle exists in subtitles folder")
	}
}

func TestNewUsesProvidedFolders(t *testing.T) {
	root := t.TempDir()
	cfgMgr := newTestConfigManager(t, root, "")
	defer cfgMgr.Stop()
	folders := config.FoldersConfig{Staging: filepath.Join(root, "custom-staging")}

	svc := New(cfgMgr, nil, folders)

	if svc.folders.Staging != folders.Staging {
		t.Fatalf("Staging folder = %q, want %q", svc.folders.Staging, folders.Staging)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sameFile(t *testing.T, a, b string) bool {
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

func newTestConfigManager(t *testing.T, root string, suffix string) *config.Manager {
	t.Helper()
	cfgPath := filepath.Join(root, "config.yaml")
	mustWriteTestFile(t, cfgPath, "dry_run: false\nsubtitle:\n  language_suffix: "+suffix+"\n")
	cfgMgr, err := config.NewManager(cfgPath)
	if err != nil {
		t.Fatalf("new config manager: %v", err)
	}
	return cfgMgr
}

func mustWriteTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteExecutable(t *testing.T, path, content string) {
	t.Helper()
	mustWriteTestFile(t, path, content)
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}
