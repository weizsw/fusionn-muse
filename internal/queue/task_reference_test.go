package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fusionn-muse/internal/mediaintake"
)

func TestTaskUnresolvedSubmissionStillUsesReceiptID(t *testing.T) {
	p := &submissionTestProcessor{processorFunc: func(context.Context, *Job) error { return nil }}
	p.profile.Store("original")
	p.prepare = func(context.Context, *Submission) (*mediaintake.ResolvedMedia, func() error, error) {
		return nil, nil, errors.New("input unavailable")
	}
	q := New(p, 1, 0)
	q.Start()
	defer q.Close()
	input, err := q.Submit(context.Background(), "/missing/SNOS-411.mp4", "SNOS-411", "video")
	if err != nil {
		t.Fatal(err)
	}
	detail := waitTaskStatus(t, q, "submission", input.ID, "failed")
	if detail.ID != input.ID || detail.Submission.JobID != "" {
		t.Fatalf("unresolved receipt=%+v", detail)
	}
	if _, err := q.GetTaskDetail(context.Background(), "job", "SNOS-411", "", 50); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unresolved input became a Job: %v", err)
	}
}

func TestTaskCodeHistoryCursorWorksAcrossCodeAndID(t *testing.T) {
	q := New(processorFunc(func(context.Context, *Job) error { return errors.New("upstream unavailable") }), 1, 0)
	q.Start()
	defer q.Close()
	job := NewJob("canonical-job-id", "/missing/SNOS-411.mp4", "SNOS-411.mp4", "", "")
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitTaskStatus(t, q, "job", job.ID, "failed")
	if _, err := q.ExecuteTaskAction(context.Background(), "job", "SNOS-411", TaskAction{Action: "retry"}); err != nil {
		t.Fatal(err)
	}
	waitTaskStatus(t, q, "job", job.ID, "failed")
	first, err := q.GetTaskDetail(context.Background(), "job", "SNOS-411", "", 1)
	if err != nil || first.NextHistoryCursor == "" || len(first.History) != 1 {
		t.Fatalf("first=%+v error=%v", first, err)
	}
	for _, reference := range []string{job.ID, "snos-411"} {
		second, err := q.GetTaskDetail(context.Background(), "job", reference, first.NextHistoryCursor, 1)
		if err != nil || second.ID != job.ID || len(second.History) != 1 || second.History[0].ID == first.History[0].ID || second.NextHistoryCursor != "" {
			t.Fatalf("second reference=%q detail=%+v error=%v", reference, second, err)
		}
	}
}

func TestTaskCodeDoesNotMatchNumericPrefixOrSubmission(t *testing.T) {
	q := New(processorFunc(func(context.Context, *Job) error { return nil }), 1, 0)
	q.Start()
	defer q.Close()
	if err := q.Accept(NewJob("different-job", "/missing/SNOS-4110.mp4", "SNOS-4110.mp4", "", "")); err != nil {
		t.Fatal(err)
	}
	for _, reference := range []string{"SNOS-411", "SNOS-412", "movieSNOS-4110", "SNOS-4110.mp4"} {
		if _, err := q.GetTaskDetail(context.Background(), "job", reference, "", 50); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown reference=%q error=%v", reference, err)
		}
		if _, err := q.ExecuteTaskAction(context.Background(), "job", reference, TaskAction{Action: "retry"}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unknown action reference=%q error=%v", reference, err)
		}
	}
	if _, err := q.GetTaskDetail(context.Background(), "submission", "SNOS-4110", "", 50); !errors.Is(err, ErrNotFound) {
		t.Fatalf("code resolved to submission: %v", err)
	}
}

func TestTaskActionByCodeAndIDReusesOneExecution(t *testing.T) {
	var calls atomic.Int32
	q := New(processorFunc(func(ctx context.Context, job *Job) error {
		if calls.Add(1) == 1 {
			return errors.New("upstream unavailable")
		}
		<-ctx.Done()
		return ctx.Err()
	}), 1, 0)
	q.Start()
	defer q.Close()
	job := NewJob("canonical-job-id", "/missing/SNOS-411.mp4", "SNOS-411.mp4", "", "")
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitTaskStatus(t, q, "job", job.ID, "failed")
	first, err := q.ExecuteTaskAction(context.Background(), "job", "snos-411", TaskAction{Action: "retry"})
	if err != nil || first.ID != job.ID || first.Attempt == nil || first.Attempt.JobID != job.ID {
		t.Fatalf("code action=%+v error=%v", first, err)
	}
	var group sync.WaitGroup
	for i := range 16 {
		reference := job.ID
		if i%2 == 0 {
			reference = "SNOS-411"
		}
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := q.ExecuteTaskAction(context.Background(), "job", reference, TaskAction{Action: "retry"})
			if err != nil || result.ID != job.ID || result.Attempt.ID != first.Attempt.ID {
				t.Errorf("repeated action=%+v error=%v", result, err)
			}
		}()
	}
	group.Wait()
	detail, err := q.GetTaskDetail(context.Background(), "job", "SNOS-411", "", 50)
	if err != nil || len(detail.History) != 2 {
		t.Fatalf("concurrent actions changed history=%+v error=%v", detail, err)
	}
}

func TestTaskDetailAcceptsCodeAndKeepsCanonicalJobIdentity(t *testing.T) {
	q := New(processorFunc(func(context.Context, *Job) error { return nil }), 1, 0)
	q.Start()
	defer q.Close()
	job := NewJob("canonical-job-id", "/missing/SNOS-411.mp4", "SNOS-411.mp4", "", "")
	if err := q.Accept(job); err != nil {
		t.Fatal(err)
	}
	waitTaskStatus(t, q, "job", job.ID, "completed")
	for _, reference := range []string{"SNOS-411", "snos-411", job.ID} {
		detail, err := q.GetTaskDetail(context.Background(), "job", reference, "", 50)
		if err != nil || detail.ID != job.ID || detail.Job.ID != job.ID || len(detail.History) != 1 {
			t.Fatalf("reference=%q detail=%+v error=%v", reference, detail, err)
		}
	}
}
