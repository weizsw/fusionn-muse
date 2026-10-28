package queue

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/internal/executor"
	"github.com/fusionn-muse/internal/toolrun"
	"github.com/fusionn-muse/pkg/logger"
)

// stubFailureScript writes the translation script's real failure stream from a
// real child process, followed by raw lines a reader must never see. The
// sentinel text stands in for a credential and for subtitle content.
const stubFailureScript = `printf '%s\n' '2026-07-01T14:20:09.456+08:00 ERROR service=fusionn-muse component=llm-subtrans event=translation_failed error="ModuleNotFoundError" errno=2 module=PySubtrans stack="  File \"a b.py\", line 7, in f\n  File \"c\\d.py\", line 8, in g\n" msg="Subtitle translation failed"' >&2
printf '%s\n' '2026-07-01T14:20:09.456+08:00 ERROR service=fusionn-muse component=llm-subtrans event=script_failure_detected msg="matched"' >&2
printf '%s\n' 'Authorization: Bearer STREAM_SENTINEL_TOKEN' >&2
printf '%s\n' 'STREAM_SENTINEL_SUBTITLE_BODY' >&2
exit 1
`

const stubFrames = "  File \"a b.py\", line 7, in f\n  File \"c\\d.py\", line 8, in g\n"

// scriptFailureJobID is the job whose translation fails.
const scriptFailureJobID = "job-script-failure"

// scriptRunner runs the real ExecRunner with a stub script in place of the
// container path, so a real child process feeds the real capture.
type scriptRunner struct{ script string }

func (scriptRunner) Run(context.Context, string, ...string) error { return nil }

func (scriptRunner) Output(context.Context, string, ...string) ([]byte, error) { return nil, nil }

func (r scriptRunner) Stream(ctx context.Context, _ string, stdout, stderr io.Writer, _ ...string) error {
	return toolrun.ExecRunner{}.Stream(ctx, "sh", stdout, stderr, r.script)
}

// scriptFailureProcessor runs the real translation executor for one job, so
// the script's failure travels the real path to the one report at the worker
// boundary. Another job fails plainly, which consumes an earlier durable
// attempt ID.
type scriptFailureProcessor struct {
	translator *executor.LLMSubtrans
	input      string
}

func (p scriptFailureProcessor) Process(ctx context.Context, job *Job) error {
	if err := job.BeginStage(ctx, StageTranslating); err != nil {
		return err
	}
	if job.ID != scriptFailureJobID {
		return errors.New("unrelated failure")
	}
	_, err := p.translator.Translate(ctx, p.input)
	return err
}

func TestScriptFailureIsReportedOnceAtTheQueueBoundary(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "stub.sh")
	if err := os.WriteFile(script, []byte(stubFailureScript), 0o755); err != nil {
		t.Fatalf("write stub script: %v", err)
	}

	var buf bytes.Buffer
	t.Setenv("LOG_LEVEL", "DEBUG")
	logger.Configure(logger.Config{Output: zapcore.AddSync(&buf)})

	translator := executor.NewLLMSubtrans(config.LLMSubtransConfig{},
		config.TranslateConfig{TargetLang: "Simplified Chinese"}, scriptRunner{script: script})
	q := New(scriptFailureProcessor{translator: translator, input: filepath.Join(root, "movie.srt")}, 0, 0)
	q.Start()
	defer q.Stop()

	// A first job consumes durable attempt 1, so the script job's own attempt ID
	// cannot be confused with its retry ordinal.
	first := NewJob("job-first", "/tmp/other.mp4", "other.mp4", "", "")
	first.IsLight = true
	if err := q.Accept(first); err != nil {
		t.Fatalf("accept first job: %v", err)
	}
	waitForStatus(t, q, first.ID, StatusFailed)

	job := NewJob(scriptFailureJobID, "/tmp/source.mp4", "source.mp4", "", "")
	job.IsLight = true
	if err := q.Accept(job); err != nil {
		t.Fatalf("accept job: %v", err)
	}
	waitForStatus(t, q, job.ID, StatusFailed)
	// The status is persisted before the boundary writes its report, so the
	// worker must finish before the captured output is read.
	q.Stop()

	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	var failures []string
	for _, line := range lines {
		if strings.Contains(line, "event=job_failed") && strings.Contains(line, "job_id="+scriptFailureJobID) {
			failures = append(failures, line)
		}
	}
	if len(failures) != 1 {
		t.Fatalf("job_failed lines = %d, want exactly one for the script job: %q", len(failures), failures)
	}
	line := failures[0]
	t.Logf("boundary report: %s", line)
	for field, want := range map[string]string{
		"job_id":     scriptFailureJobID,
		"stage":      string(StageTranslating),
		"error_type": "ModuleNotFoundError",
		"module":     "PySubtrans",
		"errno":      "2",
		"stack":      stubFrames,
		"retry":      "1",
	} {
		if got := renderedField(t, line, field); got != want {
			t.Fatalf("job_failed %s = %q, want %q (line: %s)", field, got, want, line)
		}
	}

	detail := q.GetJobDetail(scriptFailureJobID)
	if detail == nil || len(detail.Attempts) == 0 {
		t.Fatalf("script job attempt detail missing: %#v", detail)
	}
	attemptID := strconv.FormatInt(detail.Attempts[0].ID, 10)
	if got := renderedField(t, line, "attempt_id"); got != attemptID {
		t.Fatalf("attempt_id = %q, want the persisted attempt %s", got, attemptID)
	}
	if attemptID == renderedField(t, line, "retry") {
		t.Fatalf("attempt_id %s is the retry ordinal, not the persisted attempt", attemptID)
	}
	for _, want := range []string{"llm-subtrans failed", "exit status 1"} {
		if !strings.Contains(renderedField(t, line, "error"), want) {
			t.Fatalf("job_failed error = %q, want %q", renderedField(t, line, "error"), want)
		}
	}
	for _, line := range lines {
		for _, hidden := range []string{
			"event=translation_failed", "event=script_failure_detected",
			"STREAM_SENTINEL_TOKEN", "STREAM_SENTINEL_SUBTITLE_BODY",
		} {
			if strings.Contains(line, hidden) {
				t.Fatalf("line %q shows %q", line, hidden)
			}
		}
	}
}

// renderedField decodes one field of a rendered line the way any reader of the
// unified format does.
func renderedField(t *testing.T, line, key string) string {
	t.Helper()
	marker := " " + key + "="
	at := strings.Index(line, marker)
	if at < 0 {
		t.Fatalf("line %q has no %s field", line, key)
	}
	rest := line[at+len(marker):]
	if strings.HasPrefix(rest, `"`) {
		quoted, err := strconv.QuotedPrefix(rest)
		if err != nil {
			t.Fatalf("line %q has an undecodable %s field: %v", line, key, err)
		}
		value, err := strconv.Unquote(quoted)
		if err != nil {
			t.Fatalf("line %q has an undecodable %s field: %v", line, key, err)
		}
		return value
	}
	if end := strings.IndexByte(rest, ' '); end >= 0 {
		return rest[:end]
	}
	return rest
}
