package processor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/fusionn-muse/internal/client/apprise"
	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/executor"
	"github.com/fusionn-muse/internal/fileops"
	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/internal/toolrun"
	"github.com/fusionn-muse/pkg/logger"
)

// Service handles the subtitle processing pipeline.
type Service struct {
	cfgMgr  *config.Manager
	folders config.FoldersConfig
	apprise *apprise.Client
}

// New creates a new processor service.
func New(cfgMgr *config.Manager, appriseClient *apprise.Client, folders config.FoldersConfig) *Service {
	return &Service{
		cfgMgr:  cfgMgr,
		folders: folders,
		apprise: appriseClient,
	}
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

	// Get fresh config for this job (enables hot-reload)
	cfg := s.cfgMgr.Get()
	pipelineProvider := strings.ToLower(cfg.Pipeline.Provider)
	if pipelineProvider == "" {
		pipelineProvider = "videocaptioner"
	}

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

	if !cfg.DryRun && !hasChineseSub && hardSubOCREnabled(cfg) {
		detected, err := detectHardSubOCR(ctx, processingPath)
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
		// Step 3: Transcribe
		log.Infof("🎤 Step 3: Transcribing with %s...", pipelineProvider)
		t = startStep(ctx, "Transcription")

		var err error
		switch pipelineProvider {
		case "videocaptioner":
			subtitlePath, err = executor.NewWhisper(cfg.Whisper, cfg.Translate).Transcribe(ctx, processingPath)
		case "mlx_qwen3_asr":
			subtitlePath, err = executor.NewHostASR(cfg.MLXQwen3ASR).Transcribe(ctx, processingPath)
		default:
			err = fmt.Errorf("unsupported pipeline provider: %s", cfg.Pipeline.Provider)
		}
		if err != nil {
			s.moveToFailed(ctx, job, processingPath)
			return s.handleError(ctx, job, "transcription", err)
		}
		durations["transcription"] = t.done()

		// Step 4: Translate
		log.Infof("🌐 Step 4: Translating subtitle → %s...", cfg.Translate.TargetLang)
		t = startStep(ctx, "Translation")

		if pipelineProvider == "mlx_qwen3_asr" {
			translatedPath, err = executor.NewLLMSubtrans(cfg.LLMSubtrans, cfg.Translate).Translate(ctx, subtitlePath)
		} else {
			translatedPath, err = executor.NewTranslator(cfg.Translate).Translate(ctx, subtitlePath)
		}
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

func detectHardSubOCR(parent context.Context, videoPath string) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()

	duration, err := probeDuration(ctx, videoPath)
	if err != nil {
		return false, err
	}
	tmpDir, err := os.MkdirTemp("", "fusionn-muse-ocr-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	hits := 0
	for i, pct := range []float64{0.15, 0.30, 0.45, 0.60, 0.75} {
		frame := filepath.Join(tmpDir, fmt.Sprintf("frame-%d.png", i))
		if err := extractSubtitleBand(ctx, videoPath, duration*pct, frame); err != nil {
			return false, err
		}
		text, err := toolrun.ExecRunner{}.Output(ctx, "tesseract", frame, "stdout")
		if err != nil {
			return false, err
		}
		if ocrTextLooksReadable(string(text)) {
			hits++
			if hits >= 2 {
				return true, nil
			}
		}
	}
	return false, nil
}

func probeDuration(ctx context.Context, videoPath string) (float64, error) {
	out, err := toolrun.ExecRunner{}.Output(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", videoPath)
	if err != nil {
		return 0, fmt.Errorf("ffprobe duration: %w", err)
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("invalid ffprobe duration: %q", strings.TrimSpace(string(out)))
	}
	return duration, nil
}

func extractSubtitleBand(ctx context.Context, videoPath string, seconds float64, outPath string) error {
	out, err := toolrun.ExecRunner{}.CombinedOutput(
		ctx,
		"ffmpeg",
		"-y",
		"-ss", fmt.Sprintf("%.3f", seconds),
		"-i", videoPath,
		"-frames:v", "1",
		"-vf", "crop=iw:ih*0.4:0:ih*0.6",
		outPath,
	)
	if err != nil {
		return fmt.Errorf("ffmpeg frame extract: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ocrTextLooksReadable(text string) bool {
	nonSpace := 0
	cjk := 0
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		nonSpace++
		if (r >= '\u3400' && r <= '\u9fff') || (r >= '\uf900' && r <= '\ufaff') {
			cjk++
		}
	}
	return cjk >= 4 || nonSpace >= 8
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

// MoveToStagingForRetry moves a failed file back to staging for manual retry.
func (s *Service) MoveToStagingForRetry(fileName string) error {
	failedPath := filepath.Join(s.folders.Failed, fileName)
	stagingPath := filepath.Join(s.folders.Staging, fileName)

	if !fileops.Exists(failedPath) {
		return fmt.Errorf("file not found in failed folder: %s", fileName)
	}

	return fileops.Move(context.Background(), failedPath, stagingPath)
}

// GetStagingFiles returns all video files in staging folder.
func (s *Service) GetStagingFiles() ([]string, error) {
	return mediaintake.FindVideoFiles(s.folders.Staging)
}

// GetFailedFiles returns all video files in failed folder.
func (s *Service) GetFailedFiles() ([]string, error) {
	return mediaintake.FindVideoFiles(s.folders.Failed)
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
