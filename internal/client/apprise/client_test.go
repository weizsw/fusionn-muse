package apprise

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"

	"github.com/fusionn-muse/internal/config"
	"github.com/fusionn-muse/pkg/logger"
)

const testKey = "apprise-key-that-must-not-leak"

// droppedConnectionServer hijacks every request and closes the connection, so
// the client fails at the transport level with a URL-bearing error.
func droppedConnectionServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(server.Close)
	return server
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	original := os.Stderr
	os.Stderr = writer
	defer func() { os.Stderr = original }()

	fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close reader: %v", err)
	}
	return string(output)
}

func TestNotifyKeepsTheKeyOutOfErrorsAndStderr(t *testing.T) {
	server := droppedConnectionServer(t)
	client := NewClient(config.AppriseConfig{Enabled: true, BaseURL: server.URL, Key: testKey})

	var notifyErr error
	stderr := captureStderr(t, func() {
		notifyErr = client.Notify(context.Background(), "Subtitle ready", "STREAM_SENTINEL_BODY", "info")
	})

	if notifyErr == nil {
		t.Fatal("Notify returned nil error for a dropped connection")
	}
	if strings.Contains(notifyErr.Error(), testKey) {
		t.Fatalf("error = %q, want no configured key", notifyErr)
	}
	if strings.Contains(notifyErr.Error(), "STREAM_SENTINEL_BODY") {
		t.Fatalf("error = %q, want no request body", notifyErr)
	}
	// The transport cause survives unwrapping, so a caller can still classify it.
	var opErr *net.OpError
	if !errors.Is(notifyErr, io.EOF) && !errors.As(notifyErr, &opErr) {
		t.Fatalf("error = %v, want the transport cause preserved", notifyErr)
	}
	if strings.Contains(stderr, "RESTY") || strings.Contains(stderr, testKey) {
		t.Fatalf("Resty wrote to stderr: %q", stderr)
	}
}

func TestNotifyDebugDetailRedactsTheKey(t *testing.T) {
	server := droppedConnectionServer(t)
	client := NewClient(config.AppriseConfig{Enabled: true, BaseURL: server.URL, Key: testKey})

	var buf bytes.Buffer
	t.Setenv("LOG_LEVEL", "DEBUG")
	logger.Configure(logger.Config{Output: zapcore.AddSync(&buf)})
	if err := client.Notify(context.Background(), "Subtitle ready", "body", "info"); err == nil {
		t.Fatal("Notify returned nil error for a dropped connection")
	}

	rendered := buf.String()
	if strings.Contains(rendered, testKey) {
		t.Fatalf("log contains the configured key: %q", rendered)
	}
	if !strings.Contains(rendered, "event=notification_transport_detail") {
		t.Fatalf("log = %q, want the transport detail event", rendered)
	}
	if !strings.Contains(rendered, "***") {
		t.Fatalf("log = %q, want the key replaced in the excerpt", rendered)
	}
	if lines := strings.Split(strings.TrimSpace(rendered), "\n"); len(lines) != 1 {
		t.Fatalf("rendered lines = %d, want one physical line: %q", len(lines), rendered)
	}
}

func TestNotifyReportsTheStatusWithoutTheRemoteBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("STREAM_SENTINEL_UPSTREAM_BODY"))
	}))
	t.Cleanup(server.Close)
	client := NewClient(config.AppriseConfig{Enabled: true, BaseURL: server.URL, Key: testKey})

	err := client.Notify(context.Background(), "Subtitle ready", "body", "info")
	if err == nil {
		t.Fatal("Notify returned nil error for an error status")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("error = %q, want the status", err)
	}
	for _, hidden := range []string{"STREAM_SENTINEL_UPSTREAM_BODY", testKey} {
		if strings.Contains(err.Error(), hidden) {
			t.Fatalf("error = %q, want no %q", err, hidden)
		}
	}
}
