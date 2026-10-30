package logger

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
)

func linesWith(buf *bytes.Buffer, text string) []string {
	var found []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.Contains(line, text) {
			found = append(found, line)
		}
	}
	return found
}

// A config level overrides LOG_LEVEL; an empty value returns to LOG_LEVEL. Each
// level change logs one event, under the more verbose of the two levels.
func TestSetConfigLevelOverridesAndRestoresLOGLEVEL(t *testing.T) {
	buf := capture(t, "WARN")
	log := Component("worker")
	ctx := context.Background()

	SetConfigLevel("debug")
	log.Debug(ctx, "probe", "Debug shown")
	SetConfigLevel(" DEBUG ") // the same level: no event
	SetConfigLevel("")
	log.Info(ctx, "probe", "Info hidden")
	log.Warn(ctx, "probe", "Warn shown")

	changes := linesWith(buf, "event=log_level_changed")
	if len(changes) != 2 ||
		!strings.Contains(changes[0], "INFO component=logger event=log_level_changed from=WARN source=config to=DEBUG ") ||
		!strings.Contains(changes[1], "from=DEBUG source=env to=WARN ") {
		t.Fatalf("level changes = %q", changes)
	}
	got := buf.String()
	if !strings.Contains(got, "Debug shown") || strings.Contains(got, "Info hidden") || !strings.Contains(got, "Warn shown") {
		t.Errorf("events do not follow the level:\n%s", got)
	}
}

// The change event is written before logging gets quieter, so it still shows.
func TestSetConfigLevelChangeShowsWhenLoggingGetsQuieter(t *testing.T) {
	buf := capture(t, "")
	SetConfigLevel("error")
	Component("worker").Warn(context.Background(), "probe", "Warn hidden")

	if changes := linesWith(buf, "from=INFO source=config to=ERROR "); len(changes) != 1 {
		t.Errorf("want one visible change event:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "Warn hidden") {
		t.Errorf("WARN shows at ERROR:\n%s", buf.String())
	}
}

// An invalid value keeps the current level and warns once, not on every reload.
func TestSetConfigLevelInvalidValueKeepsTheLevel(t *testing.T) {
	buf := capture(t, "")
	SetConfigLevel("debug")
	SetConfigLevel("loud")
	SetConfigLevel("loud")
	Component("worker").Debug(context.Background(), "probe", "Debug shown")

	invalid := linesWith(buf, "event=config_invalid")
	if len(invalid) != 1 || !strings.Contains(invalid[0], "WARN component=logger event=config_invalid log_level=loud ") {
		t.Errorf("invalid value warnings = %q", invalid)
	}
	if !strings.Contains(buf.String(), "Debug shown") {
		t.Errorf("an invalid value changed the level:\n%s", buf.String())
	}
}

func TestConfigureClearsTheConfigLevel(t *testing.T) {
	capture(t, "")
	SetConfigLevel("debug")
	buf := capture(t, "")
	Component("worker").Debug(context.Background(), "probe", "Debug hidden")
	SetConfigLevel("debug")

	if strings.Contains(buf.String(), "Debug hidden") {
		t.Errorf("the config level survived Configure:\n%s", buf.String())
	}
	if len(linesWith(buf, "event=log_level_changed")) != 1 {
		t.Errorf("the same config value after Configure must apply again:\n%s", buf.String())
	}
}

// Config reloads change the level while other goroutines log.
func TestSetConfigLevelIsSafeWhileLogging(t *testing.T) {
	capture(t, "")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 200 {
			Component("worker").Debug(context.Background(), "probe", "Debug")
		}
	}()
	for _, value := range []string{"debug", "", "error", "info", "debug"} {
		SetConfigLevel(value)
	}
	wg.Wait()
}
