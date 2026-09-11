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
	apprise            *apprise.Client
	resolveExecutors   func(config.Config) (transcriber, subtitleTranslator, error)
	detectHardSubtitle func(context.Context, string) (bool, error)
}

// New creates a new processor service.
func New(cfgMgr *config.Manager, appriseClient *apprise.Client, folders config.FoldersConfig, runner toolrun.Runner) *Service {
	return &Service{
		cfgMgr:  cfgMgr,
		folders: folders,
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
		return executor.NewWhisper(cfg.Whisper, cfg.Translate, runner), executor.NewTranslator(cfg.Translate, runner), nil
	case "mlx_qwen3_asr":
		return executor.NewHostASR(cfg.MLXQwen3ASR), executor.NewLLMSubtrans(cfg.LLMSubtrans, cfg.Translate, runner), nil
	default:
		return nil, nil, fmt.Errorf("unsupported pipeline provider: %s", cfg.Pipeline.Provider)
	}
}

func pipelineProvider(cfg config.Config) string {
	provider := strings.ToLower(cfg.Pipeline.Provider)
	if provider == "" {
		return "videocaptioner"
	}
	return provider
}

// stepTimer tracks timing for a processing step.
type stepTimer struct {
	ctx   context.Context
	name  string
	start time.Time
}

func startStep(ctx context.Context, name string) *stepTimer {
	return &stepTimer{ctx: ctx, name: name, start: time.Now()}
}

func (s *stepTimer) done() time.Duration {
	elapsed := time.Since(s.start)
	logger.FromContext(s.ctx).Infof("   ⏱️  %s: %v", s.name, formatDuration(elapsed))
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
	totalStart := time.Now()
	log := logger.FromContext(ctx)
	cfg, err := configForAttempt(*s.cfgMgr.Get(), job.SettingsSnapshot)
	if err != nil {
		return err
	}
	provider := pipelineProvider(cfg)
	settings, err := snapshotSettings(cfg)
	if err != nil {
		return err
	}
	if err := job.SaveSettings(ctx, settings); err != nil {
		return err
	}

	log.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Infof("🎬 Starting job: %s", job.FileName)
	log.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	durations := make(map[string]time.Duration)
	start := job.StartStage
	if start == "" {
		start = queue.StagePreparing
	}
	if start == queue.StageDelivered {
		return nil
	}
	retranslation := job.AttemptKind == queue.AttemptRetranslate
	stagingPath := job.StagingPath
	processingPath := job.ProcessingPath
	preserveStaging := stagingPath != "" && samePath(job.SourcePath, stagingPath)

	if stageDue(start, queue.StagePreparing) {
		if err := job.BeginStage(ctx, queue.StagePreparing); err != nil {
			return err
		}
		stagingPath = filepath.Join(s.folders.Staging, job.FileName)
		log.Infof("📥 Step 1: Staging file...")
		t := startStep(ctx, "Staging")
		if err := hardlinkOrCopyOnce(ctx, job.SourcePath, stagingPath); err != nil {
			return s.handleError(ctx, "staging", err)
		}
		job.StagingPath = stagingPath
		durations["staging"] = t.done()
		if err := job.SaveCheckpoint(ctx, queue.StagePrepared); err != nil {
			return err
		}
	}

	hasChineseSub := job.IsLight
	if stageDue(start, queue.StageMoving) {
		if err := job.BeginStage(ctx, queue.StageMoving); err != nil {
			return err
		}
		originalName := job.FileName
		if !hasChineseSub && mediaintake.HasChineseSubtitle(originalName) {
			hasChineseSub = true
			job.IsLight = true
			job.SubtitleDetectionReason = mediaintake.SubtitleDetectionFilename
		}
		if cleaned := mediaintake.CleanVideoFilename(job.FileName); cleaned != job.FileName {
			log.Infof("📝 Cleaned filename: %s → %s", job.FileName, cleaned)
			job.FileName = cleaned
		}
		processingPath = filepath.Join(s.folders.Process, job.FileName)
		log.Infof("📦 Step 2: Moving to processing...")
		t := startStep(ctx, "Move to processing")
		preserveStaging, err = moveToProcessing(ctx, job, stagingPath, processingPath)
		if err != nil {
			return s.handleError(ctx, "move to processing", err)
		}
		job.ProcessingPath = processingPath
		if !preserveStaging {
			job.StagingPath = ""
		}
		durations["move_to_processing"] = t.done()

		if !cfg.DryRun && !hasChineseSub && hardSubOCREnabled(&cfg) {
			detected, detectErr := s.detectHardSubtitle(ctx, processingPath)
			if detectErr != nil {
				log.Warnf("⚠️ Hard-sub OCR detection failed for %s: %v", job.FileName, detectErr)
			} else if detected {
				hasChineseSub = true
				job.IsLight = true
				job.SubtitleDetectionReason = mediaintake.SubtitleDetectionHardSubOCR
			}
		}
		if err := job.SaveCheckpoint(ctx, queue.StageMoved); err != nil {
			return err
		}
	}

	skipSubtitle := cfg.DryRun || hasChineseSub
	if !skipSubtitle && (stageDue(start, queue.StageTranscribing) || stageDue(start, queue.StageTranslating)) {
		transcriber, translator, resolveErr := s.resolveExecutors(cfg)
		if resolveErr != nil {
			if stageDue(start, queue.StageTranscribing) {
				if moveErr := s.moveToFailed(ctx, job, processingPath); moveErr != nil {
					resolveErr = errors.Join(resolveErr, moveErr)
				}
			}
			return s.handleError(ctx, string(start), resolveErr)
		}

		if stageDue(start, queue.StageTranscribing) {
			if err := job.BeginStage(ctx, queue.StageTranscribing); err != nil {
				return err
			}
			log.Infof("🎤 Step 3: Transcribing with %s...", provider)
			t := startStep(ctx, "Transcription")
			job.SubtitlePath = filepath.Join(s.folders.Transcriptions, subtitleOutputName(job.FileName, ".srt", ""))
			if _, statErr := os.Stat(job.SubtitlePath); errors.Is(statErr, os.ErrNotExist) {
				generated, transcribeErr := transcriber.Transcribe(ctx, processingPath)
				if transcribeErr != nil {
					if moveErr := s.moveToFailed(ctx, job, processingPath); moveErr != nil {
						transcribeErr = errors.Join(transcribeErr, moveErr)
					}
					return s.handleError(ctx, "transcription", transcribeErr)
				}
				if err := atomicCopy(ctx, generated, job.SubtitlePath); err != nil {
					return s.handleError(ctx, "persist transcription", err)
				}
				if !samePath(generated, job.SubtitlePath) {
					_ = fileops.Remove(generated) //nolint:errcheck // Best-effort cleanup after durable copy.
				}
			} else if statErr != nil {
				return s.handleError(ctx, "read persisted transcription", statErr)
			}
			job.TranscriptionSource = provider
			durations["transcription"] = t.done()
			if err := job.SaveCheckpoint(ctx, queue.StageTranscribed); err != nil {
				return err
			}
		}

		if stageDue(start, queue.StageTranslating) {
			if err := job.BeginStage(ctx, queue.StageTranslating); err != nil {
				return err
			}
			if job.SubtitlePath == "" {
				return s.handleError(ctx, "read persisted transcription", fmt.Errorf("persisted transcription is missing"))
			}
			file, statErr := os.Open(job.SubtitlePath)
			if statErr != nil {
				return s.handleError(ctx, "read persisted transcription", statErr)
			}
			file.Close()
			log.Infof("🌐 Step 4: Translating subtitle → %s...", cfg.Translate.TargetLang)
			t := startStep(ctx, "Translation")
			job.TranslatedPath, err = translator.Translate(ctx, job.SubtitlePath)
			if err != nil {
				return s.handleError(ctx, "translation", err)
			}
			durations["translation"] = t.done()
			if err := job.SaveCheckpoint(ctx, queue.StageTranslated); err != nil {
				return err
			}
		}
	}

	if stageDue(start, queue.StageDelivering) {
		if err := job.BeginStage(ctx, queue.StageDelivering); err != nil {
			return err
		}
		if skipSubtitle {
			if cfg.DryRun {
				baseName := strings.TrimSuffix(job.FileName, filepath.Ext(job.FileName))
				dummy := filepath.Join(filepath.Dir(processingPath), baseName+".srt")
				if err := mediaintake.WriteDummySubtitle(dummy); err != nil {
					return s.handleError(ctx, "create dummy subtitle", err)
				}
				_ = fileops.Remove(dummy) //nolint:errcheck // Best-effort dry-run cleanup.
			} else if job.SubtitleDetectionReason == mediaintake.SubtitleDetectionSidecar && job.SidecarSubtitlePath != "" {
				final := filepath.Join(s.folders.Subtitles, subtitleOutputName(job.FileName, filepath.Ext(job.SidecarSubtitlePath), cfg.Subtitle.LanguageSuffix))
				if err := atomicCopy(ctx, job.SidecarSubtitlePath, final); err != nil {
					return s.handleError(ctx, "copy sidecar subtitle", err)
				}
				job.TranslatedPath = final
			}
		} else {
			final := filepath.Join(s.folders.Subtitles, subtitleOutputName(job.FileName, ".srt", cfg.Subtitle.LanguageSuffix))
			if !samePath(job.TranslatedPath, final) {
				if err := publishAtomically(ctx, job.TranslatedPath, final, job.AttemptID); err != nil {
					return s.handleError(ctx, "publish translated subtitle", err)
				}
				if !samePath(job.TranslatedPath, job.SubtitlePath) {
					_ = fileops.Remove(job.TranslatedPath) //nolint:errcheck // Best-effort cleanup after publish.
				}
			}
			job.TranslatedPath = final
		}

		scrapingPath := filepath.Join(s.folders.Scraping, job.FileName)
		if !retranslation || !samePath(processingPath, scrapingPath) {
			if err := os.MkdirAll(s.folders.Scraping, 0755); err != nil {
				return s.handleError(ctx, "prepare scraping folder", err)
			}
			log.Infof("📦 Step 6: Moving video to scraping...")
			t := startStep(ctx, "Move to scraping")
			if err := moveOnce(ctx, processingPath, scrapingPath); err != nil {
				return s.handleError(ctx, "move video to scraping", err)
			}
			job.ProcessingPath = scrapingPath
			if preserveStaging {
				if err := fileops.Remove(stagingPath); err != nil {
					log.Warnf("⚠️ Failed to remove preserved staging file: %v", err)
				}
				job.StagingPath = ""
			}
			durations["move_to_scraping"] = t.done()
		}
		if err := job.SaveCheckpoint(ctx, queue.StageDelivered); err != nil {
			return err
		}
	}

	s.notifySuccess(ctx, job, durations)
	log.Infof("✅ Job completed: %s (%s)", job.FileName, formatDuration(time.Since(totalStart)))
	return nil
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
	return nil
}

func moveToProcessing(ctx context.Context, job *queue.Job, stagingPath, processingPath string) (bool, error) {
	preserveStaging := samePath(job.SourcePath, stagingPath) && samePath(job.StagingPath, stagingPath)
	done, err := fileOperationDone(stagingPath, processingPath)
	if err != nil || done {
		return preserveStaging, err
	}
	if preserveStaging {
		return true, fileops.HardlinkOrCopy(ctx, stagingPath, processingPath)
	}
	return false, fileops.Move(ctx, stagingPath, processingPath)
}

func hardlinkOrCopyOnce(ctx context.Context, source, destination string) error {
	done, err := fileOperationDone(source, destination)
	if err != nil || done {
		return err
	}
	return fileops.HardlinkOrCopy(ctx, source, destination)
}

func moveOnce(ctx context.Context, source, destination string) error {
	done, err := fileOperationDone(source, destination)
	if err != nil || done {
		return err
	}
	return fileops.Move(ctx, source, destination)
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

	log := logger.FromContext(ctx)
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
	if err := fileops.Move(ctx, currentPath, failedPath); err != nil {
		return fmt.Errorf("move to failed folder: %w", err)
	}
	job.ProcessingPath = failedPath
	log.Infof("📁 Moved to failed folder: %s", failedPath)
	return nil
}

func (s *Service) handleError(ctx context.Context, step string, err error) error {
	fullErr := fmt.Errorf("%s failed: %w", step, err)
	logger.FromContext(ctx).Errorf("❌ %v", fullErr)
	return fullErr
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
		logger.FromContext(ctx).Warnf("⚠️ Failed to send notification: %v", err)
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
		logger.FromContext(ctx).Warnf("⚠️ Failed to send error notification: %v", notifyErr)
	}
}
