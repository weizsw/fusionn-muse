package version_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"go.uber.org/zap/zapcore"

	"github.com/fusionn-muse/internal/version"
	"github.com/fusionn-muse/pkg/logger"
)

// TestPrintBannerShowsProjectVersionAndLevel pins the facts the banner carries:
// the project name, the build version and the effective log level.
func TestPrintBannerShowsProjectVersionAndLevel(t *testing.T) {
	var buf bytes.Buffer
	version.PrintBanner(&buf, "INFO")

	got := buf.String()
	for _, want := range []string{"fusionn-muse", version.Version, "Log level: INFO", version.Banner()} {
		if !strings.Contains(got, want) {
			t.Errorf("banner does not contain %q:\n%s", want, got)
		}
	}
}

// TestPrintBannerStatesTheEffectiveLevel configures the real logger from
// LOG_LEVEL and asserts the banner states the level that actually applies:
// the default, an explicit level, an accepted spelling and the fallback an
// invalid value keeps.
func TestPrintBannerStatesTheEffectiveLevel(t *testing.T) {
	previous := logger.Log
	t.Cleanup(func() { logger.Log = previous })

	for _, testCase := range []struct {
		name      string
		logLevel  string
		wantShown string
	}{
		{"default", "", "INFO"},
		{"debug", "DEBUG", "DEBUG"},
		{"debug spelled in lower case", "debug", "DEBUG"},
		{"error", "ERROR", "ERROR"},
		{"warning alias", "WARNING", "WARN"},
		{"invalid value falls back to info", "verbose", "INFO"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", testCase.logLevel)
			// The logger only supplies the effective level here; its own output
			// goes to a discard sink so the test stays quiet.
			logger.Configure(logger.Config{Output: zapcore.AddSync(io.Discard)})

			var buf bytes.Buffer
			version.PrintBanner(&buf, logger.Log.Level().CapitalString())

			want := "Log level: " + testCase.wantShown
			if !strings.Contains(buf.String(), want) {
				t.Errorf("LOG_LEVEL=%q: banner lacks %q:\n%s", testCase.logLevel, want, buf.String())
			}
		})
	}
}
