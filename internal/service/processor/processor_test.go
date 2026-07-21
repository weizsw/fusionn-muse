package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fusionn-muse/internal/client/apprise"
	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/pkg/logger"
)

func init() {
	logger.Init(true)
}

type processorCommandCall struct {
	name string
	args []string
}

type processorCommandRunner struct {
	runCalls    []processorCommandCall
	outputCalls []processorCommandCall
	streamCalls []processorCommandCall
	onRun       func(name string, args ...string) error
	onOutput    func(name string, args ...string) ([]byte, error)
	onStream    func(name string, args ...string) (string, string, error)
}

func (r *processorCommandRunner) Run(_ context.Context, name string, args ...string) error {
	r.runCalls = append(r.runCalls, processorCommandCall{name: name, args: append([]string(nil), args...)})
	if r.onRun != nil {
		return r.onRun(name, args...)
	}
	return nil
}

func (r *processorCommandRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	r.outputCalls = append(r.outputCalls, processorCommandCall{name: name, args: append([]string(nil), args...)})
	if r.onOutput != nil {
		return r.onOutput(name, args...)
	}
	return nil, nil
}

func (r *processorCommandRunner) Stream(_ context.Context, name string, args ...string) (string, string, error) {
	r.streamCalls = append(r.streamCalls, processorCommandCall{name: name, args: append([]string(nil), args...)})
	if r.onStream != nil {
		return r.onStream(name, args...)
	}
	return "", "", nil
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

	preserved, err := moveToProcessing(context.Background(), job, stagingPath, processingPath)
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

	preserved, err := moveToProcessing(context.Background(), job, stagingPath, processingPath)
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

	svc := New(cfgMgr, nil, folders, nil)
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
	runner := &processorCommandRunner{onOutput: func(name string, _ ...string) ([]byte, error) {
		switch name {
		case "ffprobe":
			return []byte("100\n"), nil
		case "tesseract":
			return []byte("visible subtitle text\n"), nil
		default:
			return nil, errors.New("unexpected command: " + name)
		}
	}}

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

	svc := New(cfgMgr, nil, folders, runner)
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
	if len(runner.runCalls) != 2 || runner.runCalls[0].name != "ffmpeg" || runner.runCalls[1].name != "ffmpeg" {
		t.Fatalf("run calls = %#v, want two ffmpeg frame extractions", runner.runCalls)
	}
	if len(runner.outputCalls) != 3 || runner.outputCalls[0].name != "ffprobe" || runner.outputCalls[1].name != "tesseract" || runner.outputCalls[2].name != "tesseract" {
		t.Fatalf("output calls = %#v, want ffprobe then two tesseract calls", runner.outputCalls)
	}
}

func TestProcessContinuesHeavyProcessingWhenHardSubProbeFails(t *testing.T) {
	root := t.TempDir()
	runner := &processorCommandRunner{
		onOutput: func(name string, _ ...string) ([]byte, error) {
			if name != "ffprobe" {
				t.Fatalf("unexpected output command: %s", name)
			}
			return nil, errors.New("probe failed")
		},
		onStream: func(name string, args ...string) (string, string, error) {
			if name != "python3" || len(args) < 3 {
				t.Fatalf("unexpected stream command: %s %#v", name, args)
			}
			if err := os.WriteFile(args[2], []byte("subtitle\n"), 0644); err != nil {
				t.Fatalf("write command output: %v", err)
			}
			return "", "", nil
		},
	}
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

	svc := New(cfgMgr, nil, folders, runner)
	job := queue.NewJob("job1", source, "SSNI-083.mp4", "SSNI-083", "")
	if err := svc.Process(context.Background(), job); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if len(runner.streamCalls) != 2 {
		t.Fatalf("stream calls = %#v, want transcription and translation", runner.streamCalls)
	}
	if !fileExists(filepath.Join(folders.Subtitles, "SSNI-083.srt")) {
		t.Fatal("translated subtitle was not delivered")
	}
	if !fileExists(filepath.Join(folders.Scraping, "SSNI-083.mp4")) {
		t.Fatal("video was not moved to scraping")
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

	svc := New(cfgMgr, nil, folders, nil)
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

func TestNotificationsIncludeJobID(t *testing.T) {
	bodies := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request apprise.NotifyRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode notification: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		bodies <- request.Body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := apprise.NewClient(config.AppriseConfig{Enabled: true, BaseURL: server.URL, Key: "test"})
	svc := Service{apprise: client}
	job := queue.NewJob("d4e79fec-d17d-48bd-82e0-c064c6bc80e1", "/tmp/movie.mp4", "movie.mp4", "", "")
	ctx := logger.WithAttempt(logger.WithJob(context.Background(), job.ID), 1)

	svc.notifySuccess(ctx, job, map[string]time.Duration{})
	svc.notifyError(ctx, job, "transcription", errors.New("boom"))

	for range 2 {
		body := <-bodies
		if !strings.Contains(body, "Job ID: "+job.ID) {
			t.Fatalf("notification body = %q, want Job ID", body)
		}
		if strings.Contains(body, "Attempt") {
			t.Fatalf("notification body = %q, want no Attempt", body)
		}
	}
}

func TestNewUsesProvidedFolders(t *testing.T) {
	root := t.TempDir()
	cfgMgr := newTestConfigManager(t, root, "")
	defer cfgMgr.Stop()
	folders := config.FoldersConfig{Staging: filepath.Join(root, "custom-staging")}

	svc := New(cfgMgr, nil, folders, nil)

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
