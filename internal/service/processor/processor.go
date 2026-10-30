package processor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/fusionn-muse/internal/client/apprise"
	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/executor"
	"github.com/fusionn-muse/internal/fileops"
	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/internal/toolrun"
	"github.com/fusionn-muse/pkg/logger"
)

type transcriber interface {
	Transcribe(context.Context, string) (string, error)
}

type subtitleTranslator interface {
	Translate(context.Context, string) (string, error)
}

// Service handles the subtitle processing pipeline.
type Service struct {
	cfgMgr             *config.Manager
	folders            config.FoldersConfig
	runner             toolrun.Runner
	apprise            *apprise.Client
	resolveExecutors   func(config.Config) (transcriber, subtitleTranslator, error)
	detectHardSubtitle func(context.Context, string) (bool, error)
}

// New creates a new processor service.
func New(cfgMgr *config.Manager, appriseClient *apprise.Client, folders config.FoldersConfig, runner toolrun.Runner) *Service {
	return &Service{
		cfgMgr:  cfgMgr,
		folders: folders,
		runner:  runner,
		apprise: appriseClient,
		resolveExecutors: func(cfg config.Config) (transcriber, subtitleTranslator, error) {
			return resolveExecutorPair(cfg, runner)
		},
		detectHardSubtitle: func(ctx context.Context, videoPath string) (bool, error) {
			return detectHardSubOCR(ctx, runner, videoPath)
		},
	}
}

func resolveExecutorPair(cfg config.Config, runner toolrun.Runner) (transcriber, subtitleTranslator, error) {
	switch pipelineProvider(cfg) {
	case "videocaptioner":
		return nil, nil, fmt.Errorf("VideoCaptioner/Whisper pipeline is no longer supported; configure mlx_qwen3_asr and explicitly select current settings to recover legacy attempts")
	case "mlx_qwen3_asr":
		return executor.NewHostASR(cfg.MLXQwen3ASR), executor.NewLLMSubtrans(cfg.LLMSubtrans, cfg.Translate, runner), nil
	default:
		return nil, nil, fmt.Errorf("unsupported pipeline provider: %s", cfg.Pipeline.Provider)
	}
}

func pipelineProvider(cfg config.Config) string {
	provider := strings.ToLower(cfg.Pipeline.Provider)
	if provider == "" {
		// Preserve old snapshot meaning for rejection; never silently select MLX.
		return "videocaptioner"
	}
	return provider
}

// stageTimer times one processing stage.
type stageTimer struct {
	ctx   context.Context
	stage queue.Stage
	start time.Time
}

func startStage(ctx context.Context, stage queue.Stage) *stageTimer {
	return &stageTimer{ctx: ctx, stage: stage, start: time.Now()}
}

// done logs stage_completed with the same stage value as the stage_started
// line, so the two pair up, and returns the stage's duration.
func (s *stageTimer) done() time.Duration {
	elapsed := time.Since(s.start)
	logger.Component("pipeline").Info(s.ctx, "stage_completed", "Stage completed",
		zap.String("stage", string(s.stage)), logger.DurMs(elapsed))
	return elapsed
}

// formatDuration formats duration in human-readable form.
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", h, m)
}

func configForAttempt(current config.Config, snapshot string) (config.Config, error) {
	if snapshot == "" {
		return current, nil
	}
	translateKey, appriseKey := current.Translate.APIKey, current.Apprise.Key
	// Snapshots made before rate_limit existed used no pacing. Only explicit
	// current-settings actions should adopt a newly configured limit.
	current.LLMSubtrans.RateLimit = 0
	// Snapshots made before max_retries existed always retried once.
	current.LLMSubtrans.MaxRetries = 1
	// Snapshots made before request_timeout_seconds existed used 180 seconds.
	current.LLMSubtrans.RequestTimeoutSeconds = 180
	// Snapshots made before retry_backoff_seconds existed used 3.0 seconds.
	defaultBackoff := 3.0
	current.LLMSubtrans.RetryBackoffSeconds = &defaultBackoff
	// Snapshots made before batch sizes existed used PySubtrans defaults (10, 100).
	current.LLMSubtrans.MinBatchSize = 10
	current.LLMSubtrans.MaxBatchSize = 100
	if err := json.Unmarshal([]byte(snapshot), &current); err != nil {
		return config.Config{}, fmt.Errorf("load settings snapshot: %w", err)
	}
	current.Translate.APIKey = translateKey
	current.Apprise.Key = appriseKey
	return current, nil
}

func snapshotSettings(cfg config.Config) (string, error) {
	cfg.Translate.APIKey = ""
	cfg.Apprise.Key = ""
	settings, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("snapshot settings: %w", err)
	}
	return string(settings), nil
}

// SnapshotSettings captures non-secret settings for a future Attempt.
func (s *Service) SnapshotSettings() (string, error) {
	return snapshotSettings(*s.cfgMgr.Get())
}

// Process implements queue.Processor interface.
func (s *Service) Process(ctx context.Context, job *queue.Job) error {
	_, err := s.process(ctx, job, false)
	return err
}

// ProcessStage runs one durable pipeline stage and returns the next runnable stage.
func (s *Service) ProcessStage(ctx context.Context, job *queue.Job) (queue.Stage, error) {
	return s.process(ctx, job, true)
}

// HandleTerminalFailure moves unfinished heavy media out of processing after retries are exhausted.
func (s *Service) HandleTerminalFailure(ctx context.Context, job *queue.Job, stage queue.Stage, _ error) error {
	if job.AttemptKind == queue.AttemptRetranslate && job.Status == queue.StatusCompleted {
		return nil
	}
	if stage != queue.StageTranscribing && stage != queue.StageTranslating {
		return nil
	}
	return s.moveToFailed(ctx, job, job.ProcessingPath)
}

func (s *Service) process(ctx context.Context, job *queue.Job, singleStage bool) (queue.Stage, error) {
	totalStart := time.Now()
	cfg, err := configForAttempt(*s.cfgMgr.Get(), job.SettingsSnapshot)
	if err != nil {
		return "", err
	}
	provider := pipelineProvider(cfg)
	settings, err := snapshotSettings(cfg)
	if err != nil {
		return "", err
	}
	if err := job.SaveSettings(ctx, settings); err != nil {
		return "", err
	}

	pipelineLog := logger.Component("pipeline")
	pipelineLog.Info(ctx, "job_started", "Job processing started", zap.String("file", job.FileName))

	durations := make(map[string]time.Duration)
	start := job.StartStage
	if singleStage && job.AttemptStage != "" {
		start = job.AttemptStage
	}
	if start == "" {
		start = queue.StagePreparing
	}
	if job.RecoveryTranslatedPath != "" {
		job.TranslatedPath = job.RecoveryTranslatedPath
	}
	if start == queue.StageDelivered {
		return queue.StageDelivered, nil
	}
	retranslation := job.AttemptKind == queue.AttemptRetranslate
	paths := s.outputPaths(job, cfg)
	if err := job.ClaimArtifacts(ctx, paths...); err != nil {
		return "", err
	}
	if singleStage && start == queue.StageDelivering && !cfg.DryRun && !job.IsLight && s.isTranslationReceipt(job, job.TranslatedPath) {
		input := filepath.Join(filepath.Dir(job.TranslatedPath), "source", filepath.Base(job.SubtitlePath))
		same, err := fileOperationDone(job.SubtitlePath, input)
		if err != nil {
			return "", fmt.Errorf("verify translation source snapshot: %w", err)
		}
		if !same {
			return queue.StageTranslating, nil
		}
	}
	stagingPath := job.StagingPath
	processingPath := job.ProcessingPath
	preserveStaging := stagingPath != "" && samePath(job.SourcePath, stagingPath)

	if stageDue(start, queue.StagePreparing) {
		if err := job.BeginStage(ctx, queue.StagePreparing); err != nil {
			return "", err
		}
		stagingPath = filepath.Join(s.folders.Staging, job.FileName)
		pipelineLog.Info(ctx, "stage_started", "Staging file", zap.String("stage", string(queue.StagePreparing)))
		t := startStage(ctx, queue.StagePreparing)
		if err := hardlinkOrCopyOnce(ctx, job.SourcePath, stagingPath); err != nil {
			return "", s.handleError(ctx, "staging", err)
		}
		job.StagingPath = stagingPath
		durations["staging"] = t.done()
		if err := job.SaveCheckpoint(ctx, queue.StagePrepared); err != nil {
			return "", err
		}
		if singleStage {
			return queue.StageMoving, nil
		}
	}

	hasChineseSub := job.IsLight
	if stageDue(start, queue.StageMoving) {
		if err := job.BeginStage(ctx, queue.StageMoving); err != nil {
			return "", err
		}
		originalName := job.FileName
		if !hasChineseSub && (mediaintake.HasChineseSubtitle(originalName) || mediaintake.HasChineseSubtitleInHierarchy(job.SourcePath, filepath.Dir(job.SourcePath), job.TorrentName)) {
			hasChineseSub = true
			job.IsLight = true
			job.SubtitleDetectionReason = mediaintake.SubtitleDetectionFilename
		}
		if cleaned := mediaintake.CleanVideoFilename(job.FileName); cleaned != job.FileName {
			pipelineLog.Info(ctx, "filename_cleaned", "Filename cleaned",
				zap.String("from", job.FileName), zap.String("to", cleaned))
			job.FileName = cleaned
		}
		processingPath = filepath.Join(s.folders.Process, job.FileName)
		pipelineLog.Info(ctx, "stage_started", "Moving to processing", zap.String("stage", string(queue.StageMoving)))
		t := startStage(ctx, queue.StageMoving)
		preserveStaging, err = moveToProcessing(ctx, job, stagingPath, processingPath)
		if err != nil {
			return "", s.handleError(ctx, "move to processing", err)
		}
		if err := job.ValidateMedia(processingPath); err != nil {
			return "", s.handleError(ctx, "move to processing", err)
		}
		job.ProcessingPath = processingPath
		if !preserveStaging {
			job.StagingPath = ""
		}
		durations["move_to_processing"] = t.done()

		if !cfg.DryRun && !hasChineseSub && hardSubOCREnabled(&cfg) {
			detected, detectErr := s.detectHardSubtitle(ctx, processingPath)
			if detectErr != nil {
				pipelineLog.Warn(ctx, "hard_sub_detection_failed", "Hard-sub detection failed; continuing without it",
					zap.String("file", job.FileName), zap.Error(detectErr),
					logger.FailureDetail(toolrun.FailureOutput(detectErr)))
			} else if detected {
				hasChineseSub = true
				job.IsLight = true
				job.SubtitleDetectionReason = mediaintake.SubtitleDetectionHardSubOCR
			}
		}
		if err := job.SaveCheckpoint(ctx, queue.StageMoved); err != nil {
			return "", err
		}
		if singleStage {
			if cfg.DryRun || hasChineseSub {
				return queue.StageDelivering, nil
			}
			return queue.StageTranscribing, nil
		}
	}

	skipSubtitle := cfg.DryRun || hasChineseSub
	if !skipSubtitle && (stageDue(start, queue.StageTranscribing) || stageDue(start, queue.StageTranslating)) {
		transcriber, translator, resolveErr := s.resolveExecutors(cfg)
		if resolveErr != nil {
			return "", s.handleError(ctx, string(start), resolveErr)
		}

		if stageDue(start, queue.StageTranscribing) {
			job.SubtitlePath = filepath.Join(s.folders.Transcriptions, subtitleOutputName(job.FileName, ".srt", ""))
			if err := job.BeginStage(ctx, queue.StageTranscribing); err != nil {
				return "", err
			}
			pipelineLog.Info(ctx, "stage_started", "Transcribing",
				zap.String("stage", string(queue.StageTranscribing)), zap.String("provider", provider))
			t := startStage(ctx, queue.StageTranscribing)
			if _, statErr := os.Stat(job.SubtitlePath); errors.Is(statErr, os.ErrNotExist) {
				if err := job.ValidateMedia(processingPath); err != nil {
					return "", s.handleError(ctx, "read processing media", err)
				}
				generated, transcribeErr := transcriber.Transcribe(ctx, processingPath)
				if transcribeErr != nil {
					return "", s.handleError(ctx, "transcription", transcribeErr)
				}
				if err := validateArtifact(generated); err != nil {
					return "", s.handleError(ctx, "persist transcription", err)
				}
				if err := atomicCopy(ctx, generated, job.SubtitlePath); err != nil {
					return "", s.handleError(ctx, "persist transcription", err)
				}
				if !samePath(generated, job.SubtitlePath) {
					_ = fileops.Remove(generated) //nolint:errcheck // Best-effort cleanup after durable copy.
				}
			} else if statErr != nil {
				return "", s.handleError(ctx, "read persisted transcription", statErr)
			}
			if err := validateArtifact(job.SubtitlePath); err != nil {
				return "", s.handleError(ctx, "read persisted transcription", err)
			}
			job.TranscriptionSource = provider
			durations["transcription"] = t.done()
			if err := job.SaveCheckpoint(ctx, queue.StageTranscribed); err != nil {
				return "", err
			}
			if singleStage {
				return queue.StageTranslating, nil
			}
		}

		if stageDue(start, queue.StageTranslating) {
			if job.SubtitlePath == "" {
				return "", s.handleError(ctx, "read persisted transcription", fmt.Errorf("%w: persisted transcription is missing", queue.ErrArtifactUnavailable))
			}
			if err := validateArtifact(job.SubtitlePath); err != nil {
				return "", s.handleError(ctx, "read persisted transcription", err)
			}
			pipelineLog.Info(ctx, "stage_started", "Translating subtitle",
				zap.String("stage", string(queue.StageTranslating)), zap.String("target_language", cfg.Translate.TargetLang))
			t := startStage(ctx, queue.StageTranslating)
			job.TranslatedPath, err = s.translateWithReceipt(ctx, job, translator)
			if err != nil {
				return "", s.handleError(ctx, "translation", err)
			}
			if err := validateArtifact(job.TranslatedPath); err != nil {
				return "", s.handleError(ctx, "persist translation", err)
			}
			durations["translation"] = t.done()
			if err := job.SaveCheckpoint(ctx, queue.StageTranslated); err != nil {
				return "", err
			}
			if singleStage {
				return queue.StageDelivering, nil
			}
		}
	}

	if stageDue(start, queue.StageDelivering) {
		if singleStage && retranslation && job.Status == queue.StatusCompleted && job.RecoveryTranslatedPath == "" {
			job.TranslatedPath = s.translationReceiptPath(job)
		}
		if err := job.BeginStage(ctx, queue.StageDelivering); err != nil {
			return "", err
		}
		if !retranslation || job.Status != queue.StatusCompleted {
			processingPath, err = s.reconcileMedia(job)
			if err != nil {
				return "", s.handleError(ctx, "read delivery media", err)
			}
			job.ProcessingPath = processingPath
		}
		if skipSubtitle {
			if cfg.DryRun {
				baseName := strings.TrimSuffix(job.FileName, filepath.Ext(job.FileName))
				dummy := filepath.Join(filepath.Dir(processingPath), baseName+".srt")
				if err := mediaintake.WriteDummySubtitle(dummy); err != nil {
					return "", s.handleError(ctx, "create dummy subtitle", err)
				}
				_ = fileops.Remove(dummy) //nolint:errcheck // Best-effort dry-run cleanup.
			} else if job.SubtitleDetectionReason == mediaintake.SubtitleDetectionSidecar && job.SidecarSubtitlePath != "" {
				final := filepath.Join(s.folders.Subtitles, subtitleOutputName(job.FileName, filepath.Ext(job.SidecarSubtitlePath), cfg.Subtitle.LanguageSuffix))
				if err := atomicCopy(ctx, job.SidecarSubtitlePath, final); err != nil {
					return "", s.handleError(ctx, "copy sidecar subtitle", err)
				}
				job.TranslatedPath = final
			}
		} else {
			if err := validateArtifact(job.TranslatedPath); err != nil {
				return "", s.handleError(ctx, "read translated subtitle", err)
			}
			final := filepath.Join(s.folders.Subtitles, subtitleOutputName(job.FileName, ".srt", cfg.Subtitle.LanguageSuffix))
			if !samePath(job.TranslatedPath, final) {
				if err := publishAtomically(ctx, job.TranslatedPath, final, job.AttemptID); err != nil {
					return "", s.handleError(ctx, "publish translated subtitle", err)
				}
			}
			job.TranslatedPath = final
		}

		scrapingPath := filepath.Join(s.folders.Scraping, job.FileName)
		if !retranslation || job.Status != queue.StatusCompleted {
			if err := os.MkdirAll(s.folders.Scraping, 0755); err != nil {
				return "", s.handleError(ctx, "prepare scraping folder", err)
			}
			pipelineLog.Info(ctx, "stage_started", "Moving video to scraping", zap.String("stage", string(queue.StageDelivering)))
			t := startStage(ctx, queue.StageDelivering)
			if err := moveOnce(ctx, processingPath, scrapingPath); err != nil {
				return "", s.handleError(ctx, "move video to scraping", err)
			}
			job.ProcessingPath = scrapingPath
			if preserveStaging {
				if err := fileops.Remove(stagingPath); err != nil {
					pipelineLog.Warn(ctx, "staging_remove_failed", "Preserved staging file could not be removed", zap.Error(err))
				}
				job.StagingPath = ""
			}
			durations["move_to_scraping"] = t.done()
		}
		if err := job.SaveCheckpoint(ctx, queue.StageDelivered); err != nil {
			return "", err
		}
	}

	s.notifySuccess(ctx, job, durations)
	pipelineLog.Info(ctx, "job_completed", "Job completed",
		zap.String("file", job.FileName), logger.DurMs(time.Since(totalStart)))
	return queue.StageDelivered, nil
}

func stageDue(start, stage queue.Stage) bool {
	order := map[queue.Stage]int{
		queue.StagePreparing: 1, queue.StageMoving: 2, queue.StageTranscribing: 3,
		queue.StageTranslating: 4, queue.StageDelivering: 5,
	}
	return order[stage] >= order[start]
}

func publishAtomically(ctx context.Context, src, dst string, attemptID int64) error {
	done, err := fileOperationDone(src, dst)
	if err != nil || done {
		return err
	}
	tmp := fmt.Sprintf("%s.attempt-%d.tmp", dst, attemptID)
	if err := fileops.Copy(ctx, src, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace destination: %w", err)
	}
	return fileops.SyncDir(filepath.Dir(dst))
}

func moveToProcessing(ctx context.Context, job *queue.Job, stagingPath, processingPath string) (bool, error) {
	preserveStaging := samePath(job.SourcePath, stagingPath) && samePath(job.StagingPath, stagingPath)
	done, err := fileOperationDone(stagingPath, processingPath)
	if err != nil || done {
		return preserveStaging, err
	}
	if preserveStaging {
		return true, fileops.HardlinkOrCopyNoReplace(ctx, stagingPath, processingPath)
	}
	if err := rejectExistingDestination(processingPath); err != nil {
		return false, err
	}
	return false, fileops.MoveNoReplace(ctx, stagingPath, processingPath)
}

func hardlinkOrCopyOnce(ctx context.Context, source, destination string) error {
	done, err := fileOperationDone(source, destination)
	if err != nil || done {
		return err
	}
	return fileops.HardlinkOrCopyNoReplace(ctx, source, destination)
}

func moveOnce(ctx context.Context, source, destination string) error {
	done, err := fileOperationDone(source, destination)
	if err != nil || done {
		return err
	}
	if err := rejectExistingDestination(destination); err != nil {
		return err
	}
	return fileops.MoveNoReplace(ctx, source, destination)
}

func rejectExistingDestination(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("artifact conflict: destination already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func fileOperationDone(source, destination string) (bool, error) {
	if samePath(source, destination) {
		return true, nil
	}
	destinationInfo, err := os.Stat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sourceInfo, err := os.Stat(source)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if os.SameFile(sourceInfo, destinationInfo) {
		return true, nil
	}
	if sourceInfo.Size() != destinationInfo.Size() {
		return false, nil
	}
	sourceDigest, err := fileDigest(source)
	if err != nil {
		return false, err
	}
	destinationDigest, err := fileDigest(destination)
	if err != nil {
		return false, err
	}
	if sourceDigest == destinationDigest {
		return true, nil
	}
	return false, nil
}

func fileDigest(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}
func atomicCopy(ctx context.Context, source, destination string) error {
	done, err := fileOperationDone(source, destination)
	if err != nil || done {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".transcription-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	defer os.Remove(tempPath)
	if err := fileops.Copy(ctx, source, tempPath); err != nil {
		return err
	}
	return os.Rename(tempPath, destination)
}

// A crash may leave a completed move ahead of its database update. Check all
// known locations, rejecting changed or conflicting files instead of guessing.
func (s *Service) reconcileMedia(job *queue.Job) (string, error) {
	var selected string
	seen := make(map[string]bool)
	for _, path := range []string{job.ProcessingPath, filepath.Join(s.folders.Process, job.FileName), filepath.Join(s.folders.Failed, job.FileName), filepath.Join(s.folders.Scraping, job.FileName)} {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := job.ValidateMedia(path); err != nil {
			return "", err
		}
		if selected == "" {
			selected = path
			continue
		}
		same, err := fileOperationDone(selected, path)
		if err != nil {
			return "", err
		}
		if !same {
			return "", fmt.Errorf("%w: inconsistent media locations", queue.ErrArtifactConflict)
		}
	}
	if selected == "" {
		return "", fmt.Errorf("%w: media missing from known locations", queue.ErrArtifactUnavailable)
	}
	return selected, nil
}

func validateArtifact(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", queue.ErrArtifactUnavailable, path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("%w: artifact must be a nonempty regular file: %s", queue.ErrArtifactUnavailable, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func subtitleOutputName(videoName, subtitleExt, languageSuffix string) string {
	baseName := strings.TrimSuffix(videoName, filepath.Ext(videoName))
	if languageSuffix == "" {
		return baseName + subtitleExt
	}
	return baseName + "." + languageSuffix + subtitleExt
}

func hardSubOCREnabled(cfg *config.Config) bool {
	return cfg.HardSubOCR.Enabled == nil || *cfg.HardSubOCR.Enabled
}

func detectHardSubOCR(parent context.Context, runner toolrun.Runner, videoPath string) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()

	out, err := runner.Output(ctx, "python3", "/app/scripts/detect_hard_sub.py", videoPath)
	if err != nil {
		return false, fmt.Errorf("hard-sub OCR: %w", err)
	}
	switch result := strings.TrimSpace(string(out)); result {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("invalid hard-sub OCR result: %q", result)
	}
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// moveToFailed moves the file to failed folder for manual inspection.
func (s *Service) moveToFailed(ctx context.Context, job *queue.Job, currentPath string) error {
	if currentPath == "" || !fileops.Exists(currentPath) {
		return nil
	}

	failedPath := filepath.Join(s.folders.Failed, job.FileName)
	if err := os.MkdirAll(s.folders.Failed, 0755); err != nil {
		return fmt.Errorf("prepare failed folder: %w", err)
	}
	done, err := fileOperationDone(currentPath, failedPath)
	if err != nil {
		return fmt.Errorf("check failed folder destination: %w", err)
	}
	if done {
		job.ProcessingPath = failedPath
		return nil
	}
	if fileops.Exists(failedPath) {
		return fmt.Errorf("failed folder already has an unrelated file: %s", failedPath)
	}
	if err := job.ValidateMedia(currentPath); err != nil {
		return err
	}
	if err := fileops.MoveNoReplace(ctx, currentPath, failedPath); err != nil {
		return fmt.Errorf("move to failed folder: %w", err)
	}
	job.ProcessingPath = failedPath
	logger.Component("pipeline").Info(ctx, "job_moved_to_failed", "Job media moved to the failed folder",
		zap.String("path", failedPath))
	return nil
}

// handleError wraps a step failure with the step that failed. It does not log:
// the queue owns the failure boundary, where a failure that will be retried is
// a WARN, and only an exhausted attempt is an ERROR.
func (s *Service) handleError(_ context.Context, step string, err error) error {
	return fmt.Errorf("%s failed: %w", step, err)
}

func (s *Service) notifySuccess(ctx context.Context, job *queue.Job, durations map[string]time.Duration) {
	if s.apprise == nil {
		return
	}

	title := "🎬 Subtitle Ready"
	body := fmt.Sprintf("**%s**\n\nJob ID: %s\nTranscription: %s\nTranslation: %s",
		job.FileName,
		job.ID,
		formatDuration(durations["transcription"]),
		formatDuration(durations["translation"]),
	)

	if err := s.apprise.NotifySuccess(ctx, title, body); err != nil {
		logger.Component("pipeline").Warn(ctx, "notification_failed", "Success notification could not be sent", zap.Error(err))
	}
}

// NotifyFailure sends the terminal Job failure notification.
func (s *Service) NotifyFailure(ctx context.Context, job *queue.Job, stage queue.Stage, err error) {
	if s.apprise == nil {
		return
	}

	title := "❌ Subtitle Processing Failed"
	body := fmt.Sprintf("**%s**\nJob ID: %s\nFailed at: %s\nError: %v", job.FileName, job.ID, stage, err)

	if notifyErr := s.apprise.NotifyError(ctx, title, body); notifyErr != nil {
		logger.Component("pipeline").Warn(ctx, "notification_failed", "Failure notification could not be sent", zap.Error(notifyErr))
	}
}

// PlanOutputPaths lets a naming repair reserve exactly the paths used by processing.
func (s *Service) PlanOutputPaths(job *queue.Job, settings, name string) ([]string, error) {
	cfg, err := configForAttempt(*s.cfgMgr.Get(), settings)
	if err != nil {
		return nil, err
	}
	planned := *job
	planned.FileName = name
	paths := s.outputPaths(&planned, cfg)
	for _, path := range paths {
		if len(filepath.Base(path)) > 255 {
			return nil, fmt.Errorf("%w: output file name is too long", queue.ErrInvalidTaskAction)
		}
	}
	return paths, nil
}

func (s *Service) outputPaths(job *queue.Job, cfg config.Config) []string {
	outputName := mediaintake.CleanVideoFilename(job.FileName)
	paths := []string{
		filepath.Join(s.folders.Staging, job.FileName),
		filepath.Join(s.folders.Process, outputName),
		filepath.Join(s.folders.Scraping, outputName),
		filepath.Join(s.folders.Failed, outputName),
	}
	if !cfg.DryRun && !job.IsLight {
		paths = append(paths,
			filepath.Join(s.folders.Transcriptions, subtitleOutputName(outputName, ".srt", "")),
			filepath.Join(s.folders.Subtitles, subtitleOutputName(outputName, ".srt", cfg.Subtitle.LanguageSuffix)))
	} else if job.SidecarSubtitlePath != "" {
		paths = append(paths, filepath.Join(s.folders.Subtitles, subtitleOutputName(outputName, filepath.Ext(job.SidecarSubtitlePath), cfg.Subtitle.LanguageSuffix)))
	}
	return paths
}
