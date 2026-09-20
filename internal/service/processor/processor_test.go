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

type transcriberFunc func(context.Context, string) (string, error)

func (f transcriberFunc) Transcribe(ctx context.Context, path string) (string, error) {
	return f(ctx, path)
}

type subtitleTranslatorFunc func(context.Context, string) (string, error)

func (f subtitleTranslatorFunc) Translate(ctx context.Context, path string) (string, error) {
	return f(ctx, path)
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
		Staging:        filepath.Join(root, "staging"),
		Process:        filepath.Join(root, "processing"),
		Scraping:       filepath.Join(root, "scraping"),
		Subtitles:      filepath.Join(root, "subtitles"),
		Transcriptions: filepath.Join(root, "transcriptions"),
		Failed:         filepath.Join(root, "failed"),
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
	runner := &processorCommandRunner{onOutput: func(name string, args ...string) ([]byte, error) {
		if name != "python3" || len(args) != 2 || args[0] != "/app/scripts/detect_hard_sub.py" {
			t.Fatalf("unexpected command: %s %#v", name, args)
		}
		return []byte("true\n"), nil
	}}

	cfgMgr := newTestConfigManager(t, root, "")
	defer cfgMgr.Stop()
	folders := config.FoldersConfig{
		Staging:        filepath.Join(root, "staging"),
		Process:        filepath.Join(root, "processing"),
		Scraping:       filepath.Join(root, "scraping"),
		Subtitles:      filepath.Join(root, "subtitles"),
		Transcriptions: filepath.Join(root, "transcriptions"),
		Failed:         filepath.Join(root, "failed"),
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
	if len(runner.outputCalls) != 1 || runner.outputCalls[0].name != "python3" {
		t.Fatalf("output calls = %#v, want one Python OCR call", runner.outputCalls)
	}
}

func TestProcessContinuesHeavyProcessingWhenHardSubProbeFails(t *testing.T) {
	root := t.TempDir()
	runner := &processorCommandRunner{
		onOutput: func(name string, _ ...string) ([]byte, error) {
			if name != "python3" {
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
		Staging:        filepath.Join(root, "staging"),
		Process:        filepath.Join(root, "processing"),
		Scraping:       filepath.Join(root, "scraping"),
		Subtitles:      filepath.Join(root, "subtitles"),
		Transcriptions: filepath.Join(root, "transcriptions"),
		Failed:         filepath.Join(root, "failed"),
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

func TestDetectHardSubOCRRejectsInvalidResult(t *testing.T) {
	runner := &processorCommandRunner{onOutput: func(string, ...string) ([]byte, error) {
		return []byte("unknown\n"), nil
	}}

	_, err := detectHardSubOCR(context.Background(), runner, "movie.mp4")
	if err == nil || !strings.Contains(err.Error(), "invalid hard-sub OCR result") {
		t.Fatalf("detectHardSubOCR error = %v, want invalid result", err)
	}
}

func TestProcessHeavyUsesExecutorPairInOrder(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "movie.mp4")
	var calls []string
	svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
		calls = append(calls, "resolve")
		return transcriberFunc(func(_ context.Context, videoPath string) (string, error) {
				calls = append(calls, "transcribe")
				path := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".srt"
				return path, os.WriteFile(path, []byte("source subtitle"), 0644)
			}), subtitleTranslatorFunc(func(_ context.Context, subtitlePath string) (string, error) {
				calls = append(calls, "translate")
				path := strings.TrimSuffix(subtitlePath, filepath.Ext(subtitlePath)) + ".zh.srt"
				return path, os.WriteFile(path, []byte("translated subtitle"), 0644)
			}), nil
	}

	if err := svc.Process(context.Background(), job); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if got := strings.Join(calls, ","); got != "resolve,transcribe,translate" {
		t.Fatalf("calls = %q, want resolve,transcribe,translate", got)
	}
	if !fileExists(filepath.Join(folders.Subtitles, "movie.srt")) {
		t.Fatal("translated subtitle was not delivered")
	}
	if !fileExists(filepath.Join(folders.Transcriptions, "movie.srt")) {
		t.Fatal("source transcription was not persisted")
	}
	if !fileExists(filepath.Join(folders.Scraping, "movie.mp4")) {
		t.Fatal("video was not delivered")
	}
}

func TestProcessRetranslatesPersistedSourceWithoutTranscribing(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "movie.mp4")
	job.StartStage = queue.StageTranslating
	job.AttemptKind = queue.AttemptRetranslate
	job.AttemptID = 7
	job.SubtitlePath = filepath.Join(folders.Transcriptions, "movie.srt")
	mustWriteTestFile(t, job.SubtitlePath, "source subtitle")
	final := filepath.Join(folders.Subtitles, "movie.srt")
	mustWriteTestFile(t, final, "old translation")
	failedMedia := filepath.Join(folders.Failed, "movie.mp4")
	if err := os.MkdirAll(folders.Failed, 0755); err != nil {
		t.Fatalf("create failed folder: %v", err)
	}
	if err := os.Rename(job.SourcePath, failedMedia); err != nil {
		t.Fatalf("move media to failed: %v", err)
	}
	job.ProcessingPath = failedMedia

	svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
		return transcriberFunc(func(context.Context, string) (string, error) {
				t.Fatal("retranslation invoked transcriber")
				return "", nil
			}), subtitleTranslatorFunc(func(_ context.Context, source string) (string, error) {
				if source != job.SubtitlePath {
					t.Fatalf("translation source = %q, want %q", source, job.SubtitlePath)
				}
				generated := filepath.Join(folders.Transcriptions, "movie.new.srt")
				return generated, os.WriteFile(generated, []byte("new translation"), 0644)
			}), nil
	}

	if err := svc.Process(context.Background(), job); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	got, err := os.ReadFile(final)
	if err != nil || string(got) != "new translation" {
		t.Fatalf("published translation = %q, %v", got, err)
	}
	if !fileExists(job.SubtitlePath) {
		t.Fatal("persisted transcription was removed")
	}
	if !fileExists(filepath.Join(folders.Scraping, "movie.mp4")) {
		t.Fatal("failed media was not delivered after successful retranslation")
	}
}

func TestFileOperationsResumeAfterDestinationCommit(t *testing.T) {
	root := t.TempDir()
	committedMove := filepath.Join(root, "scraping.mp4")
	mustWriteTestFile(t, committedMove, "media")
	if err := moveOnce(context.Background(), filepath.Join(root, "missing-processing.mp4"), committedMove); err != nil {
		t.Fatalf("resume committed move: %v", err)
	}

	source := filepath.Join(root, "new.srt")
	destination := filepath.Join(root, "published.srt")
	mustWriteTestFile(t, source, "new translation")
	mustWriteTestFile(t, destination, "old translation")
	if err := publishAtomically(context.Background(), source, destination, 9); err != nil {
		t.Fatalf("replace committed publication: %v", err)
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "new translation" {
		t.Fatalf("published translation = %q, %v", got, err)
	}
}

func TestProcessTranslationStartRequiresPersistedTranscription(t *testing.T) {
	for _, tc := range []struct {
		name         string
		subtitlePath func(config.FoldersConfig) string
	}{
		{name: "empty", subtitlePath: func(config.FoldersConfig) string { return "" }},
		{name: "missing", subtitlePath: func(folders config.FoldersConfig) string {
			return filepath.Join(folders.Transcriptions, "missing.srt")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, folders, job := newProcessFixture(t, "movie.mp4")
			job.StartStage = queue.StageTranslating
			job.ProcessingPath = job.SourcePath
			job.SubtitlePath = tc.subtitlePath(folders)
			svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
				return transcriberFunc(func(context.Context, string) (string, error) {
						t.Fatal("transcription ran for a translation-start attempt")
						return "", nil
					}), subtitleTranslatorFunc(func(context.Context, string) (string, error) {
						t.Fatal("translation ran without persisted transcription")
						return "", nil
					}), nil
			}

			err := svc.Process(context.Background(), job)
			if err == nil || !strings.Contains(err.Error(), "read persisted transcription failed") {
				t.Fatalf("Process error = %v, want persisted transcription failure", err)
			}
		})
	}
}

func TestMoveToFailedRefusesUnrelatedDestination(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "movie.mp4")
	currentPath := job.SourcePath
	job.ProcessingPath = currentPath
	failedPath := filepath.Join(folders.Failed, job.FileName)
	mustWriteTestFile(t, failedPath, "unrelated")

	err := svc.moveToFailed(context.Background(), job, currentPath)
	if err == nil || !strings.Contains(err.Error(), "unrelated file") {
		t.Fatalf("moveToFailed error = %v, want unrelated destination failure", err)
	}
	if got, readErr := os.ReadFile(currentPath); readErr != nil || string(got) != "video" {
		t.Fatalf("source changed: %q, %v", got, readErr)
	}
	if got, readErr := os.ReadFile(failedPath); readErr != nil || string(got) != "unrelated" {
		t.Fatalf("destination changed: %q, %v", got, readErr)
	}
	if job.ProcessingPath != currentPath {
		t.Fatalf("processing path = %q, want %q", job.ProcessingPath, currentPath)
	}
}

func TestMoveToFailedAcceptsIdenticalDestination(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "movie.mp4")
	failedPath := filepath.Join(folders.Failed, job.FileName)
	mustWriteTestFile(t, failedPath, "video")

	if err := svc.moveToFailed(context.Background(), job, job.SourcePath); err != nil {
		t.Fatalf("moveToFailed identical destination: %v", err)
	}
	if job.ProcessingPath != failedPath {
		t.Fatalf("processing path = %q, want %q", job.ProcessingPath, failedPath)
	}
	if got, err := os.ReadFile(failedPath); err != nil || string(got) != "video" {
		t.Fatalf("failed file = %q, %v", got, err)
	}
}

func TestHandleTerminalFailureMovesUnfinishedHeavyMedia(t *testing.T) {
	for _, stage := range []queue.Stage{queue.StageTranscribing, queue.StageTranslating} {
		t.Run(string(stage), func(t *testing.T) {
			svc, _, folders, job := newProcessFixture(t, "movie.mp4")
			job.ProcessingPath = job.SourcePath

			if err := svc.HandleTerminalFailure(context.Background(), job, stage, errors.New("pipeline failed")); err != nil {
				t.Fatalf("HandleTerminalFailure: %v", err)
			}
			failedPath := filepath.Join(folders.Failed, job.FileName)
			if job.ProcessingPath != failedPath || !fileExists(failedPath) || fileExists(job.SourcePath) {
				t.Fatalf("terminal move = {processing:%q failed:%t source:%t}", job.ProcessingPath, fileExists(failedPath), fileExists(job.SourcePath))
			}
		})
	}
}

func TestHandleTerminalFailurePreservesCompletedRetranslationMedia(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "movie.mp4")
	scrapingPath := filepath.Join(folders.Scraping, job.FileName)
	mustWriteTestFile(t, scrapingPath, "delivered video")
	job.ProcessingPath = scrapingPath
	job.Status = queue.StatusCompleted
	job.AttemptKind = queue.AttemptRetranslate

	if err := svc.HandleTerminalFailure(context.Background(), job, queue.StageTranslating, errors.New("translation failed")); err != nil {
		t.Fatalf("HandleTerminalFailure: %v", err)
	}
	if job.ProcessingPath != scrapingPath || !fileExists(scrapingPath) {
		t.Fatalf("completed retranslation media changed: path=%q exists=%t", job.ProcessingPath, fileExists(scrapingPath))
	}
	if fileExists(filepath.Join(folders.Failed, job.FileName)) {
		t.Fatal("completed retranslation media was moved to failed")
	}
}

func TestProcessSkipsExecutorResolutionForLightAndDryRunJobs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		light  bool
		dryRun bool
	}{
		{name: "light", light: true},
		{name: "dry run", dryRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, cfgMgr, _, job := newProcessFixture(t, "movie.mp4")
			job.IsLight = tc.light
			cfgMgr.Get().DryRun = tc.dryRun
			svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
				t.Fatal("executor pair resolved for skipped heavy work")
				return nil, nil, errors.New("unexpected executor resolution")
			}

			if err := svc.Process(context.Background(), job); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}
		})
	}
}

func TestProcessUnsupportedProviderFailsOnlyForHeavyWork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		light  bool
		dryRun bool
		wantOK bool
	}{
		{name: "light", light: true, wantOK: true},
		{name: "dry run", dryRun: true, wantOK: true},
		{name: "heavy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, cfgMgr, _, job := newProcessFixture(t, "movie.mp4")
			cfgMgr.Get().Pipeline.Provider = "unsupported"
			cfgMgr.Get().DryRun = tc.dryRun
			job.IsLight = tc.light

			err := svc.Process(context.Background(), job)
			if tc.wantOK && err != nil {
				t.Fatalf("Process returned error: %v", err)
			}
			if !tc.wantOK && (err == nil || !strings.Contains(err.Error(), "unsupported pipeline provider")) {
				t.Fatalf("Process error = %v, want unsupported provider error", err)
			}
		})
	}
}

func TestProcessExecutorFailuresPreserveTranslationInputs(t *testing.T) {
	for _, tc := range []struct {
		name          string
		transcribeErr error
		translateErr  error
		wantCalls     string
		wantStep      string
		wantMoved     bool
	}{
		{name: "transcription", transcribeErr: errors.New("transcribe boom"), wantCalls: "transcribe", wantStep: "transcription failed", wantMoved: false},
		{name: "translation", translateErr: errors.New("translate boom"), wantCalls: "transcribe,translate", wantStep: "translation failed", wantMoved: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, folders, job := newProcessFixture(t, "movie.mp4")
			var calls []string
			svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
				return transcriberFunc(func(_ context.Context, videoPath string) (string, error) {
						calls = append(calls, "transcribe")
						if tc.transcribeErr != nil {
							return "", tc.transcribeErr
						}
						path := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".srt"
						return path, os.WriteFile(path, []byte("subtitle"), 0644)
					}), subtitleTranslatorFunc(func(_ context.Context, subtitlePath string) (string, error) {
						calls = append(calls, "translate")
						if tc.translateErr != nil {
							return "", tc.translateErr
						}
						return subtitlePath, nil
					}), nil
			}
			finalPath := filepath.Join(folders.Subtitles, "movie.srt")
			if tc.translateErr != nil {
				mustWriteTestFile(t, finalPath, "old translation")
			}

			err := svc.Process(context.Background(), job)
			if err == nil || !strings.Contains(err.Error(), tc.wantStep) {
				t.Fatalf("Process error = %v, want %q", err, tc.wantStep)
			}
			if got := strings.Join(calls, ","); got != tc.wantCalls {
				t.Fatalf("calls = %q, want %q", got, tc.wantCalls)
			}
			if moved := fileExists(filepath.Join(folders.Failed, "movie.mp4")); moved != tc.wantMoved {
				t.Fatalf("video moved to failed = %t, want %t", moved, tc.wantMoved)
			}
			if !fileExists(job.ProcessingPath) {
				t.Fatalf("processing media %q was not retained for terminal queue handling", job.ProcessingPath)
			}
			if tc.translateErr != nil {
				got, readErr := os.ReadFile(finalPath)
				if readErr != nil || string(got) != "old translation" {
					t.Fatalf("existing translation changed after failure: %q, %v", got, readErr)
				}
				if !fileExists(job.ProcessingPath) {
					t.Fatalf("processing media %q was not retained for translation retry", job.ProcessingPath)
				}
			}
		})
	}
}

func TestProcessHardSubtitleSeamPromotesJobWithoutExecutors(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "movie.mp4")
	svc.detectHardSubtitle = func(context.Context, string) (bool, error) { return true, nil }
	svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
		t.Fatal("executor pair resolved after hard-subtitle detection")
		return nil, nil, errors.New("unexpected executor resolution")
	}

	if err := svc.Process(context.Background(), job); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if !job.IsLight || job.SubtitleDetectionReason != mediaintake.SubtitleDetectionHardSubOCR {
		t.Fatalf("job light/reason = %v/%q", job.IsLight, job.SubtitleDetectionReason)
	}
	if !fileExists(filepath.Join(folders.Scraping, "movie.mp4")) {
		t.Fatal("video was not delivered")
	}
}

func TestProcessReadsFreshConfigForEachAttempt(t *testing.T) {
	svc, cfgMgr, folders, firstJob := newProcessFixture(t, "first.mp4")
	var providers []string
	svc.resolveExecutors = func(cfg config.Config) (transcriber, subtitleTranslator, error) {
		providers = append(providers, cfg.Pipeline.Provider)
		return transcriberFunc(func(_ context.Context, videoPath string) (string, error) {
				path := strings.TrimSuffix(videoPath, filepath.Ext(videoPath)) + ".srt"
				return path, os.WriteFile(path, []byte("subtitle"), 0644)
			}), subtitleTranslatorFunc(func(_ context.Context, subtitlePath string) (string, error) {
				return subtitlePath, nil
			}), nil
	}

	cfgMgr.Get().Pipeline.Provider = "first-provider"
	if err := svc.Process(context.Background(), firstJob); err != nil {
		t.Fatalf("first Process returned error: %v", err)
	}

	root := filepath.Dir(folders.Staging)
	secondSource := filepath.Join(root, "input", "second.mp4")
	mustWriteTestFile(t, secondSource, "video")
	cfgMgr.Get().Pipeline.Provider = "second-provider"
	secondJob := queue.NewJob("job2", secondSource, "second.mp4", "second", "")
	if err := svc.Process(context.Background(), secondJob); err != nil {
		t.Fatalf("second Process returned error: %v", err)
	}

	if got := strings.Join(providers, ","); got != "first-provider,second-provider" {
		t.Fatalf("providers = %q, want fresh provider for each Attempt", got)
	}
}

func TestConfigForAttemptFreezesSettingsWithoutPersistingSecrets(t *testing.T) {
	frozen := config.Config{}
	frozen.Pipeline.Provider = "frozen-provider"
	frozen.Translate.Model = "frozen-model"
	frozen.Translate.APIKey = "old-secret"
	frozen.Apprise.Key = "old-apprise-secret"
	snapshot, err := snapshotSettings(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snapshot, "secret") {
		t.Fatalf("settings snapshot contains a secret: %s", snapshot)
	}

	current := config.Config{}
	current.Pipeline.Provider = "current-provider"
	current.Translate.Model = "current-model"
	current.Translate.APIKey = "current-secret"
	current.Apprise.Key = "current-apprise-secret"
	got, err := configForAttempt(current, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pipeline.Provider != "frozen-provider" || got.Translate.Model != "frozen-model" {
		t.Fatalf("config = %#v, want frozen non-secret settings", got)
	}
	if got.Translate.APIKey != "current-secret" || got.Apprise.Key != "current-apprise-secret" {
		t.Fatalf("config secrets = %q/%q, want current secrets", got.Translate.APIKey, got.Apprise.Key)
	}
}

func TestProcessDoesNotCreateDummySubtitleForProductionLightJob(t *testing.T) {
	root := t.TempDir()
	cfgMgr := newTestConfigManager(t, root, "")
	defer cfgMgr.Stop()
	folders := config.FoldersConfig{
		Staging:        filepath.Join(root, "staging"),
		Process:        filepath.Join(root, "processing"),
		Scraping:       filepath.Join(root, "scraping"),
		Subtitles:      filepath.Join(root, "subtitles"),
		Transcriptions: filepath.Join(root, "transcriptions"),
		Failed:         filepath.Join(root, "failed"),
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
	svc.NotifyFailure(ctx, job, queue.StageTranscribing, errors.New("boom"))

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

func newProcessFixture(t *testing.T, fileName string) (*Service, *config.Manager, config.FoldersConfig, *queue.Job) {
	t.Helper()
	root := t.TempDir()
	cfgMgr := newTestConfigManager(t, root, "")
	t.Cleanup(cfgMgr.Stop)
	folders := config.FoldersConfig{
		Staging:        filepath.Join(root, "staging"),
		Process:        filepath.Join(root, "processing"),
		Scraping:       filepath.Join(root, "scraping"),
		Subtitles:      filepath.Join(root, "subtitles"),
		Transcriptions: filepath.Join(root, "transcriptions"),
		Failed:         filepath.Join(root, "failed"),
	}
	source := filepath.Join(root, "input", fileName)
	mustWriteTestFile(t, source, "video")
	svc := New(cfgMgr, nil, folders, nil)
	svc.detectHardSubtitle = func(context.Context, string) (bool, error) { return false, nil }
	return svc, cfgMgr, folders, queue.NewJob("job1", source, fileName, strings.TrimSuffix(fileName, filepath.Ext(fileName)), "")
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
