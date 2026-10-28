package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap/zapcore"

	"github.com/fusionn-muse/pkg/logger"
)

// newMiddlewareRouter builds a router with the middleware order of the
// production entry point.
func newMiddlewareRouter(register func(*gin.Engine)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestContext())
	router.Use(RequestSummary())
	router.Use(Recovery())
	register(router)
	return router
}

// serveWithLog runs one request and returns the response and the rendered log
// lines.
func serveWithLog(t *testing.T, router *gin.Engine, request *http.Request) (*httptest.ResponseRecorder, []string) {
	t.Helper()
	var buf bytes.Buffer
	t.Setenv("LOG_LEVEL", "DEBUG")
	logger.Configure(logger.Config{Output: zapcore.AddSync(&buf)})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	text := strings.TrimSuffix(buf.String(), "\n")
	if text == "" {
		return response, nil
	}
	return response, strings.Split(text, "\n")
}

func linesContaining(lines []string, parts ...string) []string {
	var matched []string
	for _, line := range lines {
		all := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				all = false
				break
			}
		}
		if all {
			matched = append(matched, line)
		}
	}
	return matched
}

func TestPanicLeavesOneSummaryAndOneError(t *testing.T) {
	router := newMiddlewareRouter(func(r *gin.Engine) {
		r.GET("/api/v1/panic", func(*gin.Context) { panic("boom") })
	})
	response, lines := serveWithLog(t, router, httptest.NewRequest(http.MethodGet, "/api/v1/panic", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	requestID := response.Header().Get(RequestIDHeader)
	if requestID == "" {
		t.Fatal("response has no request id")
	}
	if errors := linesContaining(lines, " ERROR "); len(errors) != 1 {
		t.Fatalf("ERROR lines = %d, want exactly one: %q", len(errors), lines)
	}
	panics := linesContaining(lines, "event=request_panicked", "request_id="+requestID)
	if len(panics) != 1 {
		t.Fatalf("request_panicked lines = %d, want one with the request id: %q", len(panics), lines)
	}
	summaries := linesContaining(lines, "event=request_completed")
	if len(summaries) != 1 {
		t.Fatalf("request_completed lines = %d, want exactly one: %q", len(summaries), lines)
	}
	for _, want := range []string{" WARN ", "status=500", `route="/api/v1/panic"`, "request_id=" + requestID, "duration_ms="} {
		if !strings.Contains(summaries[0], want) {
			t.Fatalf("summary line = %q, want %q", summaries[0], want)
		}
	}
}

func TestHandlerFailureIsReportedOnce(t *testing.T) {
	router := newMiddlewareRouter(func(r *gin.Engine) {
		r.GET("/api/v1/broken", func(c *gin.Context) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "boom"})
		})
	})
	response, lines := serveWithLog(t, router, httptest.NewRequest(http.MethodGet, "/api/v1/broken", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	errors := linesContaining(lines, " ERROR ")
	if len(errors) != 1 {
		t.Fatalf("ERROR lines = %d, want exactly one: %q", len(errors), lines)
	}
	if !strings.Contains(errors[0], "event=request_completed") || !strings.Contains(errors[0], "status=500") {
		t.Fatalf("ERROR line = %q, want the request summary", errors[0])
	}
	if summaries := linesContaining(lines, "event=request_completed"); len(summaries) != 1 {
		t.Fatalf("request_completed lines = %d, want one: %q", len(summaries), lines)
	}
}

func TestRejectedRequestIsAWarning(t *testing.T) {
	router := newMiddlewareRouter(func(r *gin.Engine) {
		r.GET("/api/v1/rejected", func(c *gin.Context) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "boom"})
		})
	})
	_, lines := serveWithLog(t, router, httptest.NewRequest(http.MethodGet, "/api/v1/rejected", nil))

	if errors := linesContaining(lines, " ERROR "); len(errors) != 0 {
		t.Fatalf("ERROR lines = %q, want none for a rejected request", errors)
	}
	warnings := linesContaining(lines, " WARN ", "event=request_completed", "status=400")
	if len(warnings) != 1 {
		t.Fatalf("WARN summary lines = %d, want one: %q", len(warnings), lines)
	}
}

func TestHealthCheckSummaryStaysAtDebug(t *testing.T) {
	router := newMiddlewareRouter(func(r *gin.Engine) {
		r.GET("/api/v1/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	})
	response, lines := serveWithLog(t, router, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if noisy := linesContaining(lines, " ERROR "); len(noisy) != 0 {
		t.Fatalf("ERROR lines = %q, want none", noisy)
	}
	if noisy := linesContaining(lines, " WARN "); len(noisy) != 0 {
		t.Fatalf("WARN lines = %q, want none", noisy)
	}
	debug := linesContaining(lines, " DEBUG ", "event=request_completed", `route="/api/v1/health"`)
	if len(debug) != 1 {
		t.Fatalf("DEBUG summary lines = %d, want one: %q", len(debug), lines)
	}
}

func TestSummaryLogsTheNormalizedRoute(t *testing.T) {
	router := newMiddlewareRouter(func(r *gin.Engine) {
		r.GET("/api/v1/tasks/:kind/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
	})
	_, lines := serveWithLog(t, router, httptest.NewRequest(http.MethodGet, "/api/v1/tasks/job/abc-123", nil))

	summaries := linesContaining(lines, "event=request_completed", `route="/api/v1/tasks/:kind/:id"`)
	if len(summaries) != 1 {
		t.Fatalf("summary lines = %d, want the route pattern: %q", len(summaries), lines)
	}
	for _, line := range lines {
		if strings.Contains(line, "abc-123") {
			t.Fatalf("line %q logs the raw path parameter", line)
		}
	}
}

func TestRequestIDIsReusedWhenLegalAndReplacedWhenNot(t *testing.T) {
	router := newMiddlewareRouter(func(r *gin.Engine) {
		r.GET("/api/v1/health", func(c *gin.Context) { c.Status(http.StatusOK) })
	})

	t.Run("legal incoming id is reused", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		request.Header.Set(RequestIDHeader, "abc-123.def")
		response, lines := serveWithLog(t, router, request)
		if got := response.Header().Get(RequestIDHeader); got != "abc-123.def" {
			t.Fatalf("response %s = %q, want the caller value", RequestIDHeader, got)
		}
		if got := linesContaining(lines, "request_id=abc-123.def"); len(got) != 1 {
			t.Fatalf("summary lines with the caller id = %d, want one: %q", len(got), lines)
		}
	})

	t.Run("illegal incoming id is replaced", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		request.Header.Set(RequestIDHeader, "bad id\nERROR forged")
		response, lines := serveWithLog(t, router, request)
		replaced := response.Header().Get(RequestIDHeader)
		if replaced == "" || replaced == "bad id\nERROR forged" {
			t.Fatalf("response %s = %q, want a generated id", RequestIDHeader, replaced)
		}
		for _, line := range lines {
			if strings.Contains(line, "forged") {
				t.Fatalf("line %q carries the caller's invalid value", line)
			}
		}
		if got := linesContaining(lines, "request_id="+replaced); len(got) != 1 {
			t.Fatalf("summary lines with the generated id = %d, want one: %q", len(got), lines)
		}
	})
}
