package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fusionn-muse/internal/mediaintake"
	"github.com/fusionn-muse/internal/queue"
	"github.com/gin-gonic/gin"
)

// Only media preparation and processing are replaced. The CLI, callback,
// HTTP handlers, SQLite, duplicate resolution and TaskDetail serialization are real.
// This fixture deliberately proves no model or media-decoder execution.
type deploymentProcessor struct {
	root  string
	light bool
}

func (p deploymentProcessor) SnapshotSettings() (string, error) {
	return `{"DryRun":false}`, nil
}

func (p deploymentProcessor) PrepareSubmission(_ context.Context, submission *queue.Submission) (*mediaintake.ResolvedMedia, func() error, error) {
	if submission.TorrentName == "PREP-FAIL" {
		return nil, nil, errors.New("fixture preparation failure")
	}
	return &mediaintake.ResolvedMedia{
		SourcePath: submission.SourcePath, StagingPath: submission.SourcePath,
		FileName: filepath.Base(submission.SourcePath), HasChineseSubtitle: p.light,
	}, nil, nil
}

func (p deploymentProcessor) Process(ctx context.Context, job *queue.Job) error {
	if job.AttemptKind == queue.AttemptRetranslate {
		return errors.New("fixture retranslation failure")
	}
	job.ProcessingPath = filepath.Join(p.root, "automation", "scraping", "renamed.mkv")
	job.SubtitlePath = filepath.Join(p.root, "automation", "transcriptions", "renamed.srt")
	job.TranslatedPath = filepath.Join(p.root, "automation", "subtitles", "renamed.zh.srt")
	job.TranscriptionSource = "fixture-not-a-model"
	for _, path := range []string{job.ProcessingPath, job.SubtitlePath, job.TranslatedPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("1\n00:00:00,000 --> 00:00:01,000\nfixture\n"), 0644); err != nil {
			return err
		}
	}
	return job.SaveCheckpoint(ctx, queue.StageDelivered)
}

type deploymentResult struct {
	Reason       string            `json:"reason"`
	SubmissionID string            `json:"submission_id"`
	JobID        string            `json:"job_id"`
	Claims       map[string]string `json:"claims"`
}

func TestDeploymentCLIRealAPIContract(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for the shared deployment CLI contract test")
	}
	script, err := filepath.Abs("../../scripts/verify_deployment.py")
	if err != nil {
		t.Fatal(err)
	}
	for _, light := range []bool{false, true} {
		name := "subtitle-artifacts"
		if light {
			name = "sidecar-bypass"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			q, err := queue.Open(filepath.Join(root, "queue.db"), deploymentProcessor{root: root, light: light}, 1, 0)
			if err != nil {
				t.Fatal(err)
			}
			q.Start()
			t.Cleanup(func() { _ = q.Close() })
			router := gin.New()
			New(q, nil).RegisterRoutes(router)
			server := httptest.NewServer(router)
			t.Cleanup(server.Close)
			sample := filepath.Join(root, "VERIFY-123.mp4")
			if err := os.WriteFile(sample, []byte("fixture media for contract testing only"), 0644); err != nil {
				t.Fatal(err)
			}
			invoke := func(command, target string, wantCode int, extra ...string) deploymentResult {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				args := []string{script, command, "--url", server.URL, "--host-root", root,
					"--container-root", root, "--expect", target, "--timeout", "10", "--poll-interval", "0.01"}
				args = append(args, extra...)
				cmd := exec.CommandContext(ctx, python, args...)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				code := 0
				if err != nil {
					var exit *exec.ExitError
					if !errors.As(err, &exit) {
						t.Fatal(err)
					}
					code = exit.ExitCode()
				}
				var result deploymentResult
				if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
					t.Fatalf("invalid CLI JSON: %s; stderr=%s; error=%v", &stdout, &stderr, err)
				}
				if code != wantCode {
					t.Fatalf("exit=%d want=%d: %s; stderr=%s", code, wantCode, &stdout, &stderr)
				}
				if result.Claims["asr_invoked_this_run"] != "not_proven" || result.Claims["llm_invoked_this_run"] != "not_proven" {
					t.Fatalf("fixture was claimed as model evidence: %s", &stdout)
				}
				return result
			}
			target := "subtitles"
			if light {
				target = "delivered"
			}
			first := invoke("run", target, 0, "--sample", sample, "--name", "VERIFY-123")
			if first.Claims["delivery_observed"] != "proven" || first.JobID == "" {
				t.Fatalf("missing provider evidence: %+v", first)
			}
			if light {
				result := invoke("observe", "subtitles", 2, "--submission-id", first.SubmissionID)
				if result.Reason != "sample_bypassed" {
					t.Fatalf("unexpected bypass result: %+v", result)
				}
			}
			duplicate := invoke("run", target, 2, "--sample", sample, "--name", "VERIFY-123")
			if duplicate.JobID != first.JobID || duplicate.Reason != "existing_job" {
				t.Fatalf("duplicate not recognized: first=%+v duplicate=%+v", first, duplicate)
			}
			failed := invoke("run", target, 1, "--sample", sample, "--name", "PREP-FAIL")
			if failed.Reason != "preparation_failed" || failed.JobID != "" {
				t.Fatalf("preparation failure read old completed Job: %+v", failed)
			}
			if light {
				return
			}
			if err := q.Retranslate(first.JobID); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				detail, err := q.GetTaskDetail(context.Background(), "job", first.JobID, "", 50)
				if err != nil {
					t.Fatal(err)
				}
				if detail.Status == "failed" {
					if detail.Job.Status != queue.StatusCompleted {
						t.Fatal("fixture lost the retained completed outcome")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("retranslation did not fail")
				}
				time.Sleep(time.Millisecond)
			}
			latest := invoke("observe", target, 1, "--submission-id", first.SubmissionID)
			if !strings.HasPrefix(latest.Reason, "execution_failed") {
				t.Fatalf("retained result hid latest failure: %+v", latest)
			}
		})
	}
}
