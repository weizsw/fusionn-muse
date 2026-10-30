package processor

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap/zapcore"

	"github.com/fusionn-muse/internal/queue"
	"github.com/fusionn-muse/pkg/logger"
)

type stageLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *stageLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *stageLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A stage ends with stage_completed and the same stage value as its
// stage_started, so the two lines pair up.
func TestStageTimerLogsStageCompleted(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	buf := &stageLogBuffer{}
	logger.Configure(logger.Config{Output: zapcore.AddSync(buf)})
	t.Cleanup(func() { logger.Configure(logger.Config{Output: zapcore.AddSync(&stageLogBuffer{})}) })

	ctx := logger.WithJobID(context.Background(), "j1")
	startStage(ctx, queue.StageTranscribing).done()

	out := buf.String()
	if !strings.Contains(out, "INFO component=pipeline event=stage_completed job_id=j1 stage=transcribing duration_ms=") {
		t.Errorf("no stage_completed line for the stage:\n%s", out)
	}
	if strings.Contains(out, "step_completed") || strings.Contains(out, "step=") {
		t.Errorf("the old step event is still logged:\n%s", out)
	}
}
