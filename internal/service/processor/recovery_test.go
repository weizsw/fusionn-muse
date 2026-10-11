package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/queue"
)

func TestStageMovingRefusesUnrelatedVideo(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "movie.mp4")
	job.StartStage = queue.StageMoving
	job.StagingPath = filepath.Join(folders.Staging, job.FileName)
	mustWriteTestFile(t, job.StagingPath, "video")
	destination := filepath.Join(folders.Process, job.FileName)
	mustWriteTestFile(t, destination, "another video")
	_, err := svc.ProcessStage(context.Background(), job)
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("moving error = %v, want conflict", err)
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "another video" {
		t.Fatalf("existing media changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(job.StagingPath); err != nil || string(got) != "video" {
		t.Fatalf("staged media changed: %q, %v", got, err)
	}
}

func TestQueueRefusesUnownedTranscription(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "movie.mp4")
	source := filepath.Join(folders.Transcriptions, "movie.srt")
	mustWriteTestFile(t, source, "subtitle belonging to somebody else")
	var calls atomic.Int32
	svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
		return transcriberFunc(func(context.Context, string) (string, error) {
				calls.Add(1)
				return "", errors.New("unexpected transcription")
			}), subtitleTranslatorFunc(func(context.Context, string) (string, error) {
				calls.Add(1)
				return "", errors.New("unexpected translation")
			}), nil
	}
	q := queue.New(svc, 1, 0)
	q.Start()
	defer q.Close()
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := q.GetJob(job.ID)
		if got.Status == queue.StatusFailed {
			if !strings.Contains(got.Error, "artifact conflict") || calls.Load() != 0 {
				t.Fatalf("failure = %q, external calls=%d; want conflict before external work", got.Error, calls.Load())
			}
			if data, err := os.ReadFile(source); err != nil || string(data) != "subtitle belonging to somebody else" {
				t.Fatalf("unowned transcription changed: %q, %v", data, err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job did not reject unowned transcription: %+v", q.GetJob(job.ID))
}

func TestTranslationRejectsInvalidSourceBeforeRequest(t *testing.T) {
	for _, kind := range []string{"empty", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			svc, _, folders, job := newProcessFixture(t, "movie.mp4")
			job.StartStage = queue.StageTranslating
			job.SubtitlePath = filepath.Join(folders.Transcriptions, "movie.srt")
			switch kind {
			case "empty":
				mustWriteTestFile(t, job.SubtitlePath, "")
			case "directory":
				if err := os.MkdirAll(job.SubtitlePath, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				mustWriteTestFile(t, filepath.Join(folders.Transcriptions, "elsewhere.srt"), "unrelated source")
				if err := os.Symlink(filepath.Join(folders.Transcriptions, "elsewhere.srt"), job.SubtitlePath); err != nil {
					t.Fatal(err)
				}
			}
			var requested bool
			svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
				return nil, subtitleTranslatorFunc(func(context.Context, string) (string, error) {
					requested = true
					return "", errors.New("unexpected request")
				}), nil
			}
			_, err := svc.ProcessStage(context.Background(), job)
			if err == nil || requested || !strings.Contains(err.Error(), "read persisted transcription failed") {
				t.Fatalf("invalid source: error=%v, requested=%v", err, requested)
			}
		})
	}
}

func TestResumeReusesTranslationAfterLostCheckpoint(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(fmt.Sprintf("source_edited_%v", edited), func(t *testing.T) {
			svc, _, folders, job := newProcessFixture(t, "movie.mp4")
			var transcriptions, translations atomic.Int32
			translated := make(chan struct{}, 2)
			svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
				return transcriberFunc(func(context.Context, string) (string, error) {
						transcriptions.Add(1)
						path := filepath.Join(folders.Process, "generated.srt")
						mustWriteTestFile(t, path, "editable source")
						return path, nil
					}), subtitleTranslatorFunc(func(_ context.Context, source string) (string, error) {
						translations.Add(1)
						path := filepath.Join(filepath.Dir(source), "generated.translation.srt")
						mustWriteTestFile(t, path, "translated subtitle")
						translated <- struct{}{}
						return path, nil
					}), nil
			}
			dbPath := filepath.Join(filepath.Dir(folders.Process), "state.db")
			q, err := queue.Open(dbPath, svc, 1, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer q.Close()
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TRIGGER reject_after_translation BEFORE UPDATE ON attempts
		WHEN NEW.current_stage='translated' OR NEW.status IN ('completed','failed','interrupted')
		BEGIN SELECT RAISE(ABORT,'storage unavailable after translation'); END`); err != nil {
				t.Fatal(err)
			}
			q.Start()
			if err := q.Accept(job); err != nil {
				t.Fatal(err)
			}
			select {
			case <-translated:
			case <-time.After(3 * time.Second):
				t.Fatal("translation did not finish")
			}
			time.Sleep(100 * time.Millisecond)
			q.Close()
			if _, err := db.Exec(`DROP TRIGGER reject_after_translation`); err != nil {
				t.Fatal(err)
			}
			q, err = queue.Open(dbPath, svc, 1, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer q.Close()
			if got := q.GetJob(job.ID); got.Status != queue.StatusInterrupted {
				t.Fatalf("job after reopen: %+v", got)
			}
			q.Start()
			if edited {
				mustWriteTestFile(t, filepath.Join(folders.Transcriptions, "movie.srt"), "corrected source")
			}
			if err := q.Resume(job.ID); err != nil {
				t.Fatal(err)
			}
			waitRecoveryStatus(t, q, job.ID, queue.StatusCompleted)
			wantTranslations := int32(1)
			if edited {
				wantTranslations = 2
			}
			if transcriptions.Load() != 1 || translations.Load() != wantTranslations {
				t.Fatalf("recovery repeated external work: ASR=%d, LLM=%d", transcriptions.Load(), translations.Load())
			}
			if data, err := os.ReadFile(filepath.Join(folders.Subtitles, "movie.srt")); err != nil || string(data) != "translated subtitle" {
				t.Fatalf("delivered subtitle=%q, %v", data, err)
			}
		})
	}
}
func waitRecoveryStatus(t *testing.T, q *queue.Queue, id string, status queue.JobStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if job := q.GetJob(id); job != nil && job.Status == status {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("job=%+v, want %s", q.GetJob(id), status)
}

func TestResumeMovingRejectsChangedDestination(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "movie.mp4")
	job.StartStage = queue.StageMoving
	job.StagingPath = filepath.Join(folders.Staging, job.FileName)
	job.ProcessingPath = filepath.Join(folders.Process, job.FileName)
	mustWriteTestFile(t, job.ProcessingPath, "changed video")
	_, err := svc.ProcessStage(context.Background(), job)
	if err == nil || !strings.Contains(err.Error(), "artifact conflict") {
		t.Fatalf("resumed move=%v, want identity conflict", err)
	}
	if data, err := os.ReadFile(job.ProcessingPath); err != nil || string(data) != "changed video" {
		t.Fatalf("destination changed: %q, %v", data, err)
	}
}

func TestDifferentMediaCannotReuseOwnedOutputs(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "movie.mp4")
	var calls atomic.Int32
	svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
		return transcriberFunc(func(context.Context, string) (string, error) {
				calls.Add(1)
				path := filepath.Join(folders.Process, "asr.srt")
				mustWriteTestFile(t, path, "first source")
				return path, nil
			}), subtitleTranslatorFunc(func(_ context.Context, input string) (string, error) {
				path := filepath.Join(filepath.Dir(input), "new.srt")
				mustWriteTestFile(t, path, "first translation")
				return path, nil
			}), nil
	}
	q := queue.New(svc, 3, 0)
	q.Start()
	defer q.Close()
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitRecoveryStatus(t, q, job.ID, queue.StatusCompleted)
	otherSource := filepath.Join(filepath.Dir(folders.Process), "other-input", "movie.mp4")
	mustWriteTestFile(t, otherSource, "different media")
	other := queue.NewJob("other", otherSource, "movie.mp4", "", "")
	if err := q.Accept(other); err != nil {
		t.Fatal(err)
	}
	waitRecoveryStatus(t, q, other.ID, queue.StatusFailed)
	if got := q.GetJob(other.ID); !strings.Contains(got.Error, "artifact conflict") {
		t.Fatalf("missing ownership conflict: %+v", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("different media reused source: ASR calls=%d", calls.Load())
	}
	for path, want := range map[string]string{filepath.Join(folders.Transcriptions, "movie.srt"): "first source", filepath.Join(folders.Subtitles, "movie.srt"): "first translation"} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("original result changed: %q, %v", got, err)
		}
	}
}

func TestResumeFindsMediaMovedBeforeFailureCommit(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "movie.mp4")
	job.StartStage = queue.StageTranslating
	job.ProcessingPath = filepath.Join(folders.Process, job.FileName)
	job.SubtitlePath = filepath.Join(folders.Transcriptions, "movie.srt")
	mustWriteTestFile(t, job.SubtitlePath, "persisted source")
	failed := filepath.Join(folders.Failed, job.FileName)
	if err := os.MkdirAll(folders.Failed, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(job.SourcePath, failed); err != nil {
		t.Fatal(err)
	}
	svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
		return transcriberFunc(func(context.Context, string) (string, error) {
				t.Error("recovery repeated ASR")
				return "", errors.New("unexpected ASR")
			}), subtitleTranslatorFunc(func(_ context.Context, input string) (string, error) {
				path := filepath.Join(filepath.Dir(input), "result.srt")
				mustWriteTestFile(t, path, "translation")
				return path, nil
			}), nil
	}
	if err := svc.Process(context.Background(), job); err != nil {
		t.Fatalf("recover moved failed media: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folders.Scraping, job.FileName)); err != nil {
		t.Fatalf("recovered media not delivered: %v", err)
	}
}

func TestFailedRetranslationKeepsCanonicalResultPath(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "movie.mp4")
	var fail atomic.Bool
	svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
		return transcriberFunc(func(context.Context, string) (string, error) {
				path := filepath.Join(folders.Process, "asr.srt")
				mustWriteTestFile(t, path, "source")
				return path, nil
			}), subtitleTranslatorFunc(func(_ context.Context, input string) (string, error) {
				if fail.Load() {
					return "", errors.New("LLM unavailable")
				}
				path := filepath.Join(filepath.Dir(input), "result.srt")
				body := "original translation"
				if source, _ := os.ReadFile(input); string(source) == "edited source" {
					body = "updated translation"
				}
				mustWriteTestFile(t, path, body)
				return path, nil
			}), nil
	}
	q := queue.New(svc, 1, 0)
	q.Start()
	defer q.Close()
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitRecoveryStatus(t, q, job.ID, queue.StatusCompleted)
	original := q.GetJob(job.ID).TranslatedPath
	fail.Store(true)
	if err := q.Retranslate(job.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		detail := q.GetJobDetail(job.ID)
		if len(detail.Attempts) == 2 && detail.Attempts[1].Status == queue.AttemptFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed retranslation not recorded: %+v", detail)
		}
		time.Sleep(time.Millisecond)
	}
	got := q.GetJob(job.ID)
	if got.Status != queue.StatusCompleted || got.TranslatedPath != original {
		t.Fatalf("canonical result changed on failure: %+v", got)
	}
	if data, err := os.ReadFile(original); err != nil || string(data) != "original translation" {
		t.Fatalf("original translation lost: %q %v", data, err)
	}
	mustWriteTestFile(t, got.SubtitlePath, "edited source")
	if err := os.Remove(got.ProcessingPath); err != nil {
		t.Fatal(err)
	} // Already consumed by downstream.
	fail.Store(false)
	if err := q.Retranslate(job.ID); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(4 * time.Second)
	for {
		detail := q.GetJobDetail(job.ID)
		if len(detail.Attempts) == 3 && detail.Attempts[2].Status == queue.AttemptCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retranslation after downstream consumption: %+v", detail)
		}
		time.Sleep(time.Millisecond)
	}
	if data, err := os.ReadFile(original); err != nil || string(data) != "updated translation" {
		t.Fatalf("successful retranslation was not published: %q %v", data, err)
	}
}

func TestTaskResumeCompletedJobsInterruptedRetranslation(t *testing.T) {
	for _, checkpoint := range []string{"", "translated", "delivered"} {
		for _, edited := range []bool{false, true} {
			t.Run(fmt.Sprintf("checkpoint_%s_edited_%v", checkpoint, edited), func(t *testing.T) {
				svc, _, folders, job := newProcessFixture(t, "movie.mp4")
				var transcriptions, translations atomic.Int32
				translated := make(chan struct{}, 4)
				svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
					return transcriberFunc(func(context.Context, string) (string, error) {
							transcriptions.Add(1)
							path := filepath.Join(folders.Process, "generated.srt")
							mustWriteTestFile(t, path, "editable source")
							return path, nil
						}), subtitleTranslatorFunc(func(_ context.Context, input string) (string, error) {
							n := translations.Add(1)
							path := filepath.Join(filepath.Dir(input), "generated.translation.srt")
							mustWriteTestFile(t, path, fmt.Sprintf("translation %d", n))
							translated <- struct{}{}
							return path, nil
						}), nil
				}
				dbPath := filepath.Join(filepath.Dir(folders.Process), "state.db")
				q, err := queue.Open(dbPath, svc, 1, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer q.Close()
				q.Start()
				if err := q.Accept(job); err != nil {
					t.Fatal(err)
				}
				waitRecoveryStatus(t, q, job.ID, queue.StatusCompleted)
				<-translated
				original := q.GetJob(job.ID).TranslatedPath
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				condition := "NEW.current_stage='translated' OR "
				if checkpoint == "translated" {
					condition = "NEW.current_stage='delivered' OR "
				}
				if checkpoint == "delivered" {
					condition = ""
				}
				if _, err := db.Exec(`CREATE TRIGGER reject_retranslation_checkpoint BEFORE UPDATE ON attempts
			WHEN NEW.kind='retranslation' AND (` + condition + `NEW.status IN ('completed','failed','interrupted'))
			BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`); err != nil {
					t.Fatal(err)
				}
				started, err := q.ExecuteTaskAction(context.Background(), "job", job.ID, queue.TaskAction{Action: "retranslate"})
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-translated:
				case <-time.After(3 * time.Second):
					t.Fatal("retranslation not requested")
				}
				blocked := false
				waitUntil := time.Now().Add(3 * time.Second)
				for time.Now().Before(waitUntil) {
					detail, err := q.GetTaskDetail(context.Background(), "job", job.ID, "", 1)
					if err != nil {
						t.Fatal(err)
					}
					if detail.ExecutionID == started.Attempt.ID && detail.PersistenceError != "" {
						blocked = true
						break
					}
					time.Sleep(time.Millisecond)
				}
				if !blocked {
					t.Fatal("injected checkpoint/outcome failure not reached")
				}
				q.Close()
				if _, err := db.Exec(`DROP TRIGGER reject_retranslation_checkpoint`); err != nil {
					t.Fatal(err)
				}
				q, err = queue.Open(dbPath, svc, 1, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer q.Close()
				q.Start()
				if got := q.GetJob(job.ID); got.Status != queue.StatusCompleted || got.TranslatedPath != original {
					t.Fatalf("original result lost: %+v", got)
				}
				if edited {
					mustWriteTestFile(t, filepath.Join(folders.Transcriptions, "movie.srt"), "corrected source")
				}
				result, err := q.ExecuteTaskAction(context.Background(), "job", job.ID, queue.TaskAction{Action: "resume"})
				if err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					detail, err := q.GetTaskDetail(context.Background(), "job", job.ID, "", 50)
					if err != nil {
						t.Fatal(err)
					}
					if detail.ExecutionID == result.Attempt.ID && detail.Status == "completed" {
						break
					}
					time.Sleep(time.Millisecond)
				}
				detail, err := q.GetTaskDetail(context.Background(), "job", job.ID, "", 50)
				want := int32(2)
				if edited {
					want = 3
				}
				if err != nil || detail.Status != "completed" || detail.ExecutionID != result.Attempt.ID || transcriptions.Load() != 1 || translations.Load() != want {
					t.Fatalf("resume=%+v %v; ASR=%d LLM=%d, want LLM=%d", detail, err, transcriptions.Load(), translations.Load(), want)
				}
			})
		}
	}
}

func TestTaskRenameRepairsCollisionWithoutReleasingOtherJobsClaims(t *testing.T) {
	svc, _, folders, original := newProcessFixture(t, "movie.mp4")
	svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
		return transcriberFunc(func(context.Context, string) (string, error) {
				path := filepath.Join(folders.Process, "generated.srt")
				mustWriteTestFile(t, path, "source")
				return path, nil
			}), subtitleTranslatorFunc(func(_ context.Context, source string) (string, error) {
				path := filepath.Join(filepath.Dir(source), "generated.translation.srt")
				mustWriteTestFile(t, path, "translation")
				return path, nil
			}), nil
	}
	q, err := queue.Open(filepath.Join(filepath.Dir(folders.Process), "state.db"), svc, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	defer q.Close()
	if err := q.Accept(original); err != nil {
		t.Fatal(err)
	}
	waitRecoveryStatus(t, q, original.ID, queue.StatusCompleted)
	conflictingSource := filepath.Join(filepath.Dir(folders.Process), "other", "movie.mp4")
	mustWriteTestFile(t, conflictingSource, "different media")
	other := queue.NewJob("other", conflictingSource, "movie.mp4", "", "")
	other.StagingPath = filepath.Join(folders.Staging, "movie.mp4")
	if err := q.Accept(other); !errors.Is(err, queue.ErrArtifactConflict) {
		t.Fatalf("conflicting admission=%v", err)
	}
	waitRecoveryStatus(t, q, other.ID, queue.StatusFailed)
	identity := q.GetJob(other.ID)
	before := q.GetJob(original.ID)
	for _, name := range []string{"../escape.mp4", "wrong.mkv", "movie\x00.mp4", "movie\\escape.mp4"} {
		if _, err := q.ExecuteTaskAction(context.Background(), "job", other.ID, queue.TaskAction{Action: "retry", OutputName: name}); !errors.Is(err, queue.ErrInvalidTaskAction) {
			t.Fatalf("unsafe name %q: %v", name, err)
		}
	}
	if _, err := q.ExecuteTaskAction(context.Background(), "job", other.ID, queue.TaskAction{Action: "retry", OutputName: "movie.mp4"}); !errors.Is(err, queue.ErrArtifactConflict) {
		t.Fatalf("unchanged occupied name=%v", err)
	}
	if _, err := q.ExecuteTaskAction(context.Background(), "job", other.ID, queue.TaskAction{Action: "retry", OutputName: "movie-2.mp4"}); err != nil {
		t.Fatal(err)
	}
	waitRecoveryStatus(t, q, other.ID, queue.StatusCompleted)
	after := q.GetJob(other.ID)
	if after.ID != other.ID || after.ContentSignature != identity.ContentSignature || after.MediaID != identity.MediaID || after.SourceKey != identity.SourceKey || after.FileName != "movie-2.mp4" {
		t.Fatalf("renamed=%+v", after)
	}
	if got := q.GetJob(original.ID); got.TranslatedPath != before.TranslatedPath || got.MediaID != before.MediaID || got.FileName != before.FileName {
		t.Fatalf("original was changed: %+v", got)
	}
	if err := after.ValidateMedia(filepath.Join(folders.Scraping, "movie-2.mp4")); err != nil {
		t.Fatal(err)
	}
	if err := before.ValidateMedia(filepath.Join(folders.Scraping, "movie.mp4")); err != nil {
		t.Fatal(err)
	}
	if _, err := q.ExecuteTaskAction(context.Background(), "job", after.ID, queue.TaskAction{Action: "retranslate", OutputName: "movie-3.mp4"}); !errors.Is(err, queue.ErrInvalidTaskAction) {
		t.Fatalf("renamed completed transcription: %v", err)
	}
	detail, err := q.GetTaskDetail(context.Background(), "job", other.ID, "", 50)
	if err != nil || len(detail.History) < 2 || detail.History[0].Request == nil || detail.History[0].Request.PreviousOutputName != "movie.mp4" || detail.History[0].Request.OutputName != "movie-2.mp4" {
		t.Fatalf("rename audit=%+v %v", detail, err)
	}
}
