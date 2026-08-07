package processor

import (
	"context"
	"fmt"
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

// Process implements queue.Processor interface.
func (s *Service) Process(ctx context.Context, job *queue.Job) error {
	totalStart := time.Now()
	log := logger.FromContext(ctx)

	// Take one fresh snapshot for this Attempt (enables hot-reload between Attempts).
	cfg := *s.cfgMgr.Get()
	provider := pipelineProvider(cfg)

	log.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Infof("🎬 Starting job: %s", job.FileName)
	log.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	var durations = make(map[string]time.Duration)

	// Step 1: Hardlink/copy to staging (if not already there)
	stagingPath := filepath.Join(s.folders.Staging, job.FileName)
	if job.StagingPath == "" {
		log.Infof("📥 Step 1: Staging file...")
		t := startStep(ctx, "Staging")

		if err := fileops.HardlinkOrCopy(ctx, job.SourcePath, stagingPath); err != nil {
			return s.handleError(ctx, job, "staging", err)
		}
		job.StagingPath = stagingPath
		durations["staging"] = t.done()
	} else {
		stagingPath = job.StagingPath
		log.Infof("📥 Step 1: Using existing staging file (skipped)")
	}

	// Step 2: Clean filename and move to processing
	// Check if filename has Chinese subtitle indicators (skip transcription/translation)
	originalName := job.FileName
	hasChineseSub := job.IsLight
	if !hasChineseSub && mediaintake.HasChineseSubtitle(originalName) {
		hasChineseSub = true
		job.SubtitleDetectionReason = mediaintake.SubtitleDetectionFilename
	}

	cleanedName := mediaintake.CleanVideoFilename(job.FileName)
	if cleanedName != job.FileName {
		log.Infof("📝 Cleaned filename: %s → %s", job.FileName, cleanedName)
		job.FileName = cleanedName
	}

	processingPath := filepath.Join(s.folders.Process, job.FileName)
	log.Infof("📦 Step 2: Moving to processing...")
	t := startStep(ctx, "Move to processing")

	preserveStaging, err := moveToProcessing(ctx, job, stagingPath, processingPath)
	if err != nil {
		return s.handleError(ctx, job, "move to processing", err)
	}
	job.ProcessingPath = processingPath
	if !preserveStaging {
		job.StagingPath = ""
	}
	durations["move_to_processing"] = t.done()

	if !cfg.DryRun && !hasChineseSub && hardSubOCREnabled(&cfg) {
		detected, err := s.detectHardSubtitle(ctx, processingPath)
		if err != nil {
			log.Warnf("⚠️ Hard-sub OCR detection failed for %s: %v", job.FileName, err)
		} else if detected {
			hasChineseSub = true
			job.IsLight = true
			job.SubtitleDetectionReason = mediaintake.SubtitleDetectionHardSubOCR
		}
	}

	var subtitlePath, translatedPath string
	skipSubtitle := cfg.DryRun || hasChineseSub

	if skipSubtitle {
		// Skip transcription and translation
		if cfg.DryRun {
			log.Infof("⏭️  Step 3-4: Skipping transcription & translation (dry run)")
			baseName := strings.TrimSuffix(job.FileName, filepath.Ext(job.FileName))
			subtitlePath = filepath.Join(filepath.Dir(processingPath), baseName+".srt")
			if err := mediaintake.WriteDummySubtitle(subtitlePath); err != nil {
				s.moveToFailed(ctx, job, processingPath)
				return s.handleError(ctx, job, "create dummy subtitle", err)
			}
			translatedPath = subtitlePath
		} else {
			log.Infof("⏭️  Step 3-4: Skipping transcription & translation (Chinese subtitle detected: %s)", job.SubtitleDetectionReason)
		}
	} else {
		transcriber, translator, err := s.resolveExecutors(cfg)
		if err != nil {
			s.moveToFailed(ctx, job, processingPath)
			return s.handleError(ctx, job, "transcription", err)
		}

		// Step 3: Transcribe
		log.Infof("🎤 Step 3: Transcribing with %s...", provider)
		t = startStep(ctx, "Transcription")

		subtitlePath, err = transcriber.Transcribe(ctx, processingPath)
		if err != nil {
			s.moveToFailed(ctx, job, processingPath)
			return s.handleError(ctx, job, "transcription", err)
		}
		durations["transcription"] = t.done()

		// Step 4: Translate
		log.Infof("🌐 Step 4: Translating subtitle → %s...", cfg.Translate.TargetLang)
		t = startStep(ctx, "Translation")

		translatedPath, err = translator.Translate(ctx, subtitlePath)
		if err != nil {
			s.moveToFailed(ctx, job, processingPath)
			return s.handleError(ctx, job, "translation", err)
		}
		durations["translation"] = t.done()
	}
	job.SubtitlePath = subtitlePath
	job.TranslatedPath = translatedPath

	// Step 5: Move translated subtitle to subtitles folder (skip if no real subtitle)
	if skipSubtitle {
		if cfg.DryRun {
			log.Infof("⏭️  Step 5: Skipping subtitle move")
			// Clean up dummy subtitle
			_ = fileops.Remove(subtitlePath) //nolint:errcheck // Best-effort cleanup
		} else if job.SubtitleDetectionReason == mediaintake.SubtitleDetectionSidecar && job.SidecarSubtitlePath != "" {
			finalSubPath := filepath.Join(s.folders.Subtitles, subtitleOutputName(job.FileName, filepath.Ext(job.SidecarSubtitlePath), cfg.Subtitle.LanguageSuffix))
			log.Infof("📦 Step 5: Copying sidecar subtitle to subtitles folder...")
			t = startStep(ctx, "Copy sidecar subtitle")
			if err := fileops.Copy(ctx, job.SidecarSubtitlePath, finalSubPath); err != nil {
				return s.handleError(ctx, job, "copy sidecar subtitle", err)
			}
			durations["copy_sidecar_subtitle"] = t.done()
		} else {
			log.Infof("⏭️  Step 5: Skipping subtitle move")
		}
	} else {
		// Use cleaned video name as subtitle name with optional language suffix
		finalSubPath := filepath.Join(s.folders.Subtitles, subtitleOutputName(job.FileName, ".srt", cfg.Subtitle.LanguageSuffix))
		log.Infof("📦 Step 5: Moving translated subtitle to subtitles folder...")
		t = startStep(ctx, "Move subtitle")

		if err := fileops.Move(ctx, translatedPath, finalSubPath); err != nil {
			return s.handleError(ctx, job, "move subtitle", err)
		}
		durations["move_subtitle"] = t.done()

		// Clean up original (untranslated) subtitle - don't move, just delete
		if subtitlePath != translatedPath && fileops.Exists(subtitlePath) {
			_ = fileops.Remove(subtitlePath) //nolint:errcheck // Best-effort cleanup
		}
	}

	// Step 6: Move video to scraping folder (another program handles from here)
	scrapingPath := filepath.Join(s.folders.Scraping, job.FileName)
	log.Infof("📦 Step 6: Moving video to scraping...")
	t = startStep(ctx, "Move to scraping")

	if err := fileops.Move(ctx, processingPath, scrapingPath); err != nil {
		return s.handleError(ctx, job, "move video to scraping", err)
	}
	if preserveStaging {
		if err := fileops.Remove(stagingPath); err != nil {
			log.Warnf("⚠️ Failed to remove preserved staging file: %v", err)
		}
		job.StagingPath = ""
	}
	durations["move_to_scraping"] = t.done()

	// Step 7: Send success notification
	log.Infof("🔔 Step 7: Sending notification...")
	t = startStep(ctx, "Notification")
	s.notifySuccess(ctx, job, durations)
	durations["notification"] = t.done()

	// Total time
	totalDuration := time.Since(totalStart)

	log.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	log.Infof("✅ Job completed: %s", job.FileName)
	log.Infof("⏱️  Total time: %s", formatDuration(totalDuration))
	if !skipSubtitle {
		log.Infof("   Transcription: %s | Translation: %s",
			formatDuration(durations["transcription"]),
			formatDuration(durations["translation"]))
	}
	log.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}

func moveToProcessing(ctx context.Context, job *queue.Job, stagingPath, processingPath string) (bool, error) {
	preserveStaging := samePath(job.SourcePath, stagingPath) && samePath(job.StagingPath, stagingPath)
	if preserveStaging {
		return true, fileops.HardlinkOrCopy(ctx, stagingPath, processingPath)
	}
	return false, fileops.Move(ctx, stagingPath, processingPath)
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
func (s *Service) moveToFailed(ctx context.Context, job *queue.Job, currentPath string) {
	if currentPath == "" || !fileops.Exists(currentPath) {
		return
	}

	log := logger.FromContext(ctx)
	failedPath := filepath.Join(s.folders.Failed, job.FileName)
	if err := fileops.Move(ctx, currentPath, failedPath); err != nil {
		log.Warnf("⚠️ Failed to move to failed folder: %v", err)
	} else {
		log.Infof("📁 Moved to failed folder: %s", failedPath)
	}
}

func (s *Service) handleError(ctx context.Context, job *queue.Job, step string, err error) error {
	fullErr := fmt.Errorf("%s failed: %w", step, err)
	logger.FromContext(ctx).Errorf("❌ %v", fullErr)
	s.notifyError(ctx, job, step, err)
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

func (s *Service) notifyError(ctx context.Context, job *queue.Job, step string, err error) {
	if s.apprise == nil {
		return
	}

	title := "❌ Subtitle Processing Failed"
	body := fmt.Sprintf("**%s**\nJob ID: %s\nFailed at: %s\nError: %v", job.FileName, job.ID, step, err)

	if notifyErr := s.apprise.NotifyError(ctx, title, body); notifyErr != nil {
		logger.FromContext(ctx).Warnf("⚠️ Failed to send error notification: %v", notifyErr)
	}
}
