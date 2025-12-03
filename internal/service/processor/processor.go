package processor

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fusionn-muse/internal/client/apprise"
	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/executor"
	"github.com/fusionn-muse/internal/fileops"
	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/pkg/logger"
)

// Service handles the subtitle processing pipeline.
type Service struct {
	cfg        *config.Config
	folders    config.FoldersConfig
	whisper    *executor.Whisper
	translator *executor.Translator
	apprise    *apprise.Client
}

// New creates a new processor service.
func New(cfg *config.Config, appriseClient *apprise.Client) *Service {
	return &Service{
		cfg:        cfg,
		folders:    config.Folders(),
		whisper:    executor.NewWhisper(cfg.Whisper),
		translator: executor.NewTranslator(cfg.Translate),
		apprise:    appriseClient,
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

	// Step 2: Move to processing
	processingPath := filepath.Join(s.folders.Process, job.FileName)
	logger.Infof("📦 Step 2: Moving to processing...")
	t := startStep("Move to processing")

	if err := fileops.Move(stagingPath, processingPath); err != nil {
		return s.handleError(job, "move to processing", err)
	}
	job.ProcessingPath = processingPath
	job.StagingPath = ""
	durations["move_to_processing"] = t.done()

	// Step 3: Transcribe with whisper
	logger.Infof("🎤 Step 3: Transcribing with Whisper (%s)...", s.cfg.Whisper.Provider)
	t = startStep("Transcription")

	subtitlePath, err := s.whisper.Transcribe(ctx, processingPath)
	if err != nil {
		s.moveToFailed(job, processingPath)
		return s.handleError(job, "transcription", err)
	}
	job.SubtitlePath = subtitlePath
	durations["transcription"] = t.done()

	// Step 4: Translate with llm-subtrans
	logger.Infof("🌐 Step 4: Translating subtitle → %s...", s.cfg.Translate.TargetLang)
	t = startStep("Translation")

	translatedPath, err := s.translator.Translate(ctx, subtitlePath)
	if err != nil {
		s.moveToFailed(job, processingPath)
		return s.handleError(job, "translation", err)
	}
	job.TranslatedPath = translatedPath
	durations["translation"] = t.done()

	// Step 5: Move video to finished folder
	finishedVideoPath := filepath.Join(s.folders.Finished, job.FileName)
	logger.Infof("📦 Step 5: Moving video to finished...")
	t = startStep("Move to finished")

	if err := fileops.Move(processingPath, finishedVideoPath); err != nil {
		return s.handleError(job, "move video to finished", err)
	}
	durations["move_to_finished"] = t.done()

	// Step 6: Move translated subtitle to subtitles folder
	translatedSubName := filepath.Base(translatedPath)
	finalSubPath := filepath.Join(s.folders.Subtitles, translatedSubName)
	logger.Infof("📦 Step 6: Moving subtitle to subtitles folder...")
	t = startStep("Move subtitle")

	if err := fileops.Move(translatedPath, finalSubPath); err != nil {
		return s.handleError(job, "move subtitle", err)
	}
	durations["move_subtitle"] = t.done()

	// Also move the original (untranslated) subtitle if it exists
	if subtitlePath != translatedPath && fileops.Exists(subtitlePath) {
		origSubName := filepath.Base(subtitlePath)
		origFinalPath := filepath.Join(s.folders.Subtitles, origSubName)
		_ = fileops.Move(subtitlePath, origFinalPath)
	}

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
	logger.Infof("   Transcription: %s | Translation: %s",
		formatDuration(durations["transcription"]),
		formatDuration(durations["translation"]))
	logger.Infof("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
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
