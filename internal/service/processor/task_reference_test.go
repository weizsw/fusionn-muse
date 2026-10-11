package processor

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/queue"
)

func TestTaskOriginalCodeStillFindsJobAfterOutputNameRepair(t *testing.T) {
	svc, _, folders, job := newProcessFixture(t, "SNOS-411.mp4")
	var calls atomic.Int32
	svc.resolveExecutors = func(config.Config) (transcriber, subtitleTranslator, error) {
		return transcriberFunc(func(context.Context, string) (string, error) {
				if calls.Add(1) == 1 {
					return "", errors.New("ASR unavailable")
				}
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
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitRecoveryStatus(t, q, job.ID, queue.StatusFailed)
	result, err := q.ExecuteTaskAction(context.Background(), "job", "SNOS-411", queue.TaskAction{Action: "retry", OutputName: "operator-name.mp4"})
	if err != nil || result.ID != job.ID {
		t.Fatalf("naming repair=%+v error=%v", result, err)
	}
	waitRecoveryStatus(t, q, job.ID, queue.StatusCompleted)
	detail, err := q.GetTaskDetail(context.Background(), "job", "snos-411", "", 50)
	if err != nil || detail.ID != job.ID || detail.Name != "operator-name.mp4" || len(detail.History) != 2 || detail.History[0].Request.PreviousOutputName != "SNOS-411.mp4" {
		t.Fatalf("renamed code detail=%+v error=%v", detail, err)
	}
	if _, err := q.GetTaskDetail(context.Background(), "job", "SNOS-412", "", 50); !errors.Is(err, queue.ErrNotFound) {
		t.Fatalf("another code found renamed Job: %v", err)
	}
}
