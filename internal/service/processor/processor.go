package processor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/fusionn-muse/internal/client/apprise"
	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/executor"
	"github.com/fusionn-muse/internal/fileops"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/pkg/logger"
)

// Service handles the subtitle processing pipeline.
type Service struct {
	cfgMgr  *config.Manager
	folders config.FoldersConfig
	apprise *apprise.Client
}

// New creates a new processor service.
func New(cfgMgr *config.Manager, appriseClient *apprise.Client) *Service {
	return &Service{
		cfgMgr:  cfgMgr,
		folders: config.Folders(),
		apprise: appriseClient,
	}
}

// stepTimer tracks timing for a processing step.
type stepTimer struct {
	name  string
	start time.Time
}

func startStep(name string) *stepTimer {
	return &stepTimer{name: name, start: time.Now()}
}

func (s *stepTimer) done() time.Duration {
	elapsed := time.Since(s.start)
	logger.Infof("   ⏱️  %s: %v", s.name, formatDuration(elapsed))
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

	// Get fresh config for this job (enables hot-reload)
	cfg := s.cfgMgr.Get()
	whisper := executor.NewWhisper(cfg.Whisper, cfg.Translate)
	translator := executor.NewTranslator(cfg.Translate)

	logger.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	logger.Infof("🎬 Starting job: %s", job.FileName)
	logger.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	var durations = make(map[string]time.Duration)

	// Step 1: Hardlink/copy to staging (if not already there)
	stagingPath := filepath.Join(s.folders.Staging, job.FileName)
	if job.StagingPath == "" {
		logger.Infof("📥 Step 1: Staging file...")
		t := startStep("Staging")

		if err := fileops.HardlinkOrCopy(job.SourcePath, stagingPath); err != nil {
			return s.handleError(job, "staging", err)
		}
		job.StagingPath = stagingPath
		durations["staging"] = t.done()
	} else {
		stagingPath = job.StagingPath
		logger.Infof("📥 Step 1: Using existing staging file (skipped)")
	}

	// Step 2: Clean filename and move to processing
	// Check if filename has Chinese subtitle indicators (skip transcription/translation)
	originalName := job.FileName
	hasChineseSub := job.IsLight
	if !hasChineseSub && fileops.HasChineseSubtitle(originalName) {
		hasChineseSub = true
		job.SubtitleDetectionReason = fileops.SubtitleDetectionFilename
	}

	cleanedName := fileops.CleanVideoFilename(job.FileName)
	if cleanedName != job.FileName {
		logger.Infof("📝 Cleaned filename: %s → %s", job.FileName, cleanedName)
		job.FileName = cleanedName
	}

	processingPath := filepath.Join(s.folders.Process, job.FileName)
	logger.Infof("📦 Step 2: Moving to processing...")
	t := startStep("Move to processing")

	preserveStaging, err := moveToProcessing(job, stagingPath, processingPath)
	if err != nil {
		return s.handleError(job, "move to processing", err)
	}
	job.ProcessingPath = processingPath
	if !preserveStaging {
		job.StagingPath = ""
	}
	durations["move_to_processing"] = t.done()

	if !cfg.DryRun && !hasChineseSub && hardSubOCREnabled(cfg) {
		detected, err := detectHardSubOCR(ctx, processingPath)
		if err != nil {
			logger.Warnf("⚠️ Hard-sub OCR detection failed for %s: %v", job.FileName, err)
		} else if detected {
			hasChineseSub = true
			job.IsLight = true
			job.SubtitleDetectionReason = fileops.SubtitleDetectionHardSubOCR
		}
	}

	var subtitlePath, translatedPath string
	skipSubtitle := cfg.DryRun || hasChineseSub

	if skipSubtitle {
		// Skip transcription and translation
		if cfg.DryRun {
			logger.Infof("⏭️  Step 3-4: Skipping transcription & translation (dry run)")
			baseName := strings.TrimSuffix(job.FileName, filepath.Ext(job.FileName))
			subtitlePath = filepath.Join(filepath.Dir(processingPath), baseName+".srt")
			if err := fileops.WriteDummySubtitle(subtitlePath); err != nil {
				s.moveToFailed(job, processingPath)
				return s.handleError(job, "create dummy subtitle", err)
			}
			translatedPath = subtitlePath
		} else {
			logger.Infof("⏭️  Step 3-4: Skipping transcription & translation (Chinese subtitle detected: %s)", job.SubtitleDetectionReason)
		}
	} else {
		// Step 3: Transcribe with whisper
		logger.Infof("🎤 Step 3: Transcribing with Whisper (%s)...", cfg.Whisper.Model)
		t = startStep("Transcription")

		var err error
		subtitlePath, err = whisper.Transcribe(ctx, processingPath)
		if err != nil {
			s.moveToFailed(job, processingPath)
			return s.handleError(job, "transcription", err)
		}
		durations["transcription"] = t.done()

		// Step 4: Translate with llm-subtrans
		logger.Infof("🌐 Step 4: Translating subtitle → %s...", cfg.Translate.TargetLang)
		t = startStep("Translation")

		translatedPath, err = translator.Translate(ctx, subtitlePath)
		if err != nil {
			s.moveToFailed(job, processingPath)
			return s.handleError(job, "translation", err)
		}
		durations["translation"] = t.done()
	}
	job.SubtitlePath = subtitlePath
	job.TranslatedPath = translatedPath

	// Step 5: Move translated subtitle to subtitles folder (skip if no real subtitle)
	if skipSubtitle {
		if cfg.DryRun {
			logger.Infof("⏭️  Step 5: Skipping subtitle move")
			// Clean up dummy subtitle
			_ = fileops.Remove(subtitlePath) //nolint:errcheck // Best-effort cleanup
		} else if job.SubtitleDetectionReason == fileops.SubtitleDetectionSidecar && job.SidecarSubtitlePath != "" {
			finalSubPath := filepath.Join(s.folders.Subtitles, subtitleOutputName(job.FileName, filepath.Ext(job.SidecarSubtitlePath), cfg.Subtitle.LanguageSuffix))
			logger.Infof("📦 Step 5: Copying sidecar subtitle to subtitles folder...")
			t = startStep("Copy sidecar subtitle")
			if err := fileops.Copy(job.SidecarSubtitlePath, finalSubPath); err != nil {
				return s.handleError(job, "copy sidecar subtitle", err)
			}
			durations["copy_sidecar_subtitle"] = t.done()
		} else {
			logger.Infof("⏭️  Step 5: Skipping subtitle move")
		}
	} else {
		// Use cleaned video name as subtitle name with optional language suffix
		finalSubPath := filepath.Join(s.folders.Subtitles, subtitleOutputName(job.FileName, ".srt", cfg.Subtitle.LanguageSuffix))
		logger.Infof("📦 Step 5: Moving translated subtitle to subtitles folder...")
		t = startStep("Move subtitle")

		if err := fileops.Move(translatedPath, finalSubPath); err != nil {
			return s.handleError(job, "move subtitle", err)
		}
		durations["move_subtitle"] = t.done()

		// Clean up original (untranslated) subtitle - don't move, just delete
		if subtitlePath != translatedPath && fileops.Exists(subtitlePath) {
			_ = fileops.Remove(subtitlePath) //nolint:errcheck // Best-effort cleanup
		}
	}

	// Step 6: Move video to scraping folder (another program handles from here)
	scrapingPath := filepath.Join(s.folders.Scraping, job.FileName)
	logger.Infof("📦 Step 6: Moving video to scraping...")
	t = startStep("Move to scraping")

	if err := fileops.Move(processingPath, scrapingPath); err != nil {
		return s.handleError(job, "move video to scraping", err)
	}
	if preserveStaging {
		if err := fileops.Remove(stagingPath); err != nil {
			logger.Warnf("⚠️ Failed to remove preserved staging file: %v", err)
		}
		job.StagingPath = ""
	}
	durations["move_to_scraping"] = t.done()

	// Step 7: Send success notification
	logger.Infof("🔔 Step 7: Sending notification...")
	t = startStep("Notification")
	s.notifySuccess(job, durations)
	durations["notification"] = t.done()

	// Total time
	totalDuration := time.Since(totalStart)

	logger.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	logger.Infof("✅ Job completed: %s", job.FileName)
	logger.Infof("⏱️  Total time: %s", formatDuration(totalDuration))
	if !skipSubtitle {
		logger.Infof("   Transcription: %s | Translation: %s",
			formatDuration(durations["transcription"]),
			formatDuration(durations["translation"]))
	}
	logger.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}

func moveToProcessing(job *queue.Job, stagingPath, processingPath string) (bool, error) {
	preserveStaging := samePath(job.SourcePath, stagingPath) && samePath(job.StagingPath, stagingPath)
	if preserveStaging {
		return true, fileops.HardlinkOrCopy(stagingPath, processingPath)
	}
	return false, fileops.Move(stagingPath, processingPath)
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
		text, err := exec.CommandContext(ctx, "tesseract", frame, "stdout").Output()
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
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", videoPath).Output()
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
	cmd := exec.CommandContext(
		ctx,
		"ffmpeg",
		"-y",
		"-ss", fmt.Sprintf("%.3f", seconds),
		"-i", videoPath,
		"-frames:v", "1",
		"-vf", "crop=iw:ih*0.4:0:ih*0.6",
		outPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
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
func (s *Service) moveToFailed(job *queue.Job, currentPath string) {
	if currentPath == "" || !fileops.Exists(currentPath) {
		return
	}

	failedPath := filepath.Join(s.folders.Failed, job.FileName)
	if err := fileops.Move(currentPath, failedPath); err != nil {
		logger.Warnf("⚠️ Failed to move to failed folder: %v", err)
	} else {
		logger.Infof("📁 Moved to failed folder: %s", failedPath)
	}
}

// MoveToStagingForRetry moves a failed file back to staging for manual retry.
func (s *Service) MoveToStagingForRetry(fileName string) error {
	failedPath := filepath.Join(s.folders.Failed, fileName)
	stagingPath := filepath.Join(s.folders.Staging, fileName)

	if !fileops.Exists(failedPath) {
		return fmt.Errorf("file not found in failed folder: %s", fileName)
	}

	return fileops.Move(failedPath, stagingPath)
}

// GetStagingFiles returns all video files in staging folder.
func (s *Service) GetStagingFiles() ([]string, error) {
	return fileops.FindVideoFiles(s.folders.Staging)
}

// GetFailedFiles returns all video files in failed folder.
func (s *Service) GetFailedFiles() ([]string, error) {
	return fileops.FindVideoFiles(s.folders.Failed)
}

func (s *Service) handleError(job *queue.Job, step string, err error) error {
	fullErr := fmt.Errorf("%s failed: %w", step, err)
	logger.Errorf("❌ %v", fullErr)
	s.notifyError(job, step, err)
	return fullErr
}

func (s *Service) notifySuccess(job *queue.Job, durations map[string]time.Duration) {
	if s.apprise == nil {
		return
	}

	title := "🎬 Subtitle Ready"
	body := fmt.Sprintf("**%s**\n\nTranscription: %s\nTranslation: %s",
		job.FileName,
		formatDuration(durations["transcription"]),
		formatDuration(durations["translation"]),
	)

	if err := s.apprise.NotifySuccess(title, body); err != nil {
		logger.Warnf("⚠️ Failed to send notification: %v", err)
	}
}

func (s *Service) notifyError(job *queue.Job, step string, err error) {
	if s.apprise == nil {
		return
	}

	title := "❌ Subtitle Processing Failed"
	body := fmt.Sprintf("**%s**\nFailed at: %s\nError: %v", job.FileName, step, err)

	if notifyErr := s.apprise.NotifyError(title, body); notifyErr != nil {
		logger.Warnf("⚠️ Failed to send error notification: %v", notifyErr)
	}
}
