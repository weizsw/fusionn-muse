package handler

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/fusionn-muse/pkg/logger"
)

// RequestIDHeader carries the request ID in and out of the service. A legal
// caller value is reused; anything else is replaced, and the value the service
// used is always returned.
const RequestIDHeader = "X-Request-ID"

// healthRoute is the one route whose successful summary stays at DEBUG.
const healthRoute = "/api/v1/health"

// httpComponent is the stable module name for HTTP framework events.
var httpComponent = logger.Component("http")

// reportedErrorKey marks a request whose failure ERROR a handler or Recovery
// already recorded, so the summary adds only status and duration for it.
const reportedErrorKey = "muse_error_reported"

// markErrorReported records that this request's failure was already reported.
func markErrorReported(c *gin.Context) { c.Set(reportedErrorKey, true) }

// RequestContext gives every request one ID and carries it in the request
// context, so a handler and its summary agree, and concurrent requests cannot
// mix their correlation.
func RequestContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if !logger.ValidRequestID(id) {
			id = logger.NewRequestID()
		}
		c.Header(RequestIDHeader, id)
		c.Request = c.Request.WithContext(logger.WithRequestID(c.Request.Context(), id))
		c.Next()
	}
}

// RequestSummary writes the single line every request leaves behind: method,
// normalized route, status and duration. Register it outside Recovery, so the
// line is written even when a handler panics. The detail of a failure belongs
// to the handler that owns it, so the summary never repeats an error body.
func RequestSummary() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		route := c.FullPath()
		if route == "" {
			// An unmatched request has no route pattern; its raw path is not
			// logged, because it can carry user input.
			route = "unmatched"
		}
		status := c.Writer.Status()
		fields := []zap.Field{
			zap.String("method", c.Request.Method),
			zap.String("route", route),
			zap.Int("status", status),
			logger.DurMs(time.Since(start)),
		}
		ctx := c.Request.Context()
		switch {
		case status >= http.StatusInternalServerError && c.GetBool(reportedErrorKey):
			// The failure is already recorded once; this line carries the status
			// and duration only.
			httpComponent.Warn(ctx, "request_completed", "Request failed", fields...)
		case status >= http.StatusInternalServerError:
			httpComponent.Error(ctx, "request_completed", "Request failed", fields...)
		case status >= http.StatusBadRequest:
			httpComponent.Warn(ctx, "request_completed", "Request rejected", fields...)
		case route == healthRoute:
			httpComponent.Debug(ctx, "request_completed", "Health check completed", fields...)
		default:
			httpComponent.Info(ctx, "request_completed", "Request completed", fields...)
		}
	}
}

// Recovery reports a panic as one unified ERROR event and answers 500. It
// replaces gin.Recovery, which writes an unformatted line on its own writer and
// would bypass the shared format.
func Recovery() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		httpComponent.Error(c.Request.Context(), "request_panicked", "Request panicked",
			zap.String("panic_type", fmt.Sprintf("%T", recovered)),
			// The panic text is content from an unexpected place, so it is only
			// ever a policy-capped DEBUG preview.
			logger.Preview(fmt.Sprint(recovered)),
			zap.Stack("stack"))
		c.AbortWithStatus(http.StatusInternalServerError)
		markErrorReported(c)
	})
}
