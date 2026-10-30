package logger

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"
)

// testZone renders test times. Any zone works: a line shows the wall time of
// the zone the core converts to.
var testZone = time.FixedZone("UTC+8", 8*3600)

func TestLogZone(t *testing.T) {
	for _, tc := range []struct{ tz, want, invalid string }{
		{"", "Asia/Shanghai", ""},
		{"Asia/Shanghai", "Asia/Shanghai", ""},
		{"America/New_York", "America/New_York", ""},
		{":Europe/Paris", "Europe/Paris", ""},
		{" Asia/Tokyo ", "Asia/Tokyo", ""},
		{"UTC", "UTC", ""},
		{"Mars/Base", "Asia/Shanghai", "Mars/Base"},
	} {
		zone, invalid := logZone(tc.tz)
		if zone.String() != tc.want || invalid != tc.invalid {
			t.Errorf("logZone(%q) = %s, %q; want %s, %q", tc.tz, zone, invalid, tc.want, tc.invalid)
		}
	}
}

// configureWithTZ sets TZ for the test and restores both TZ and the logger after it.
func configureWithTZ(t *testing.T, tz string) *bytes.Buffer {
	t.Helper()
	// Registered first, so it runs last, after TZ is restored.
	t.Cleanup(func() { Configure(Config{Output: zapcore.AddSync(&bytes.Buffer{})}) })
	t.Setenv("TZ", tz)
	return capture(t, "")
}

// A line starts with the wall time of the TZ zone and carries no offset.
func TestTimestampsUseTheTZZoneWithoutOffset(t *testing.T) {
	buf := configureWithTZ(t, "America/New_York")
	Component("c").Info(context.Background(), "e", "m")

	requireRecentStamp(t, buf.String(), "America/New_York")
}

// An unknown TZ falls back to Asia/Shanghai and says so once.
func TestUnknownTZFallsBackToShanghai(t *testing.T) {
	buf := configureWithTZ(t, "Mars/Base")
	Component("c").Info(context.Background(), "e", "m")

	out := buf.String()
	if strings.Count(out, "event=config_invalid") != 1 ||
		!strings.Contains(out, ` WARN component=logger event=config_invalid tz="Mars/Base" `) {
		t.Errorf("want one config_invalid warning for TZ: %q", out)
	}
	requireRecentStamp(t, out, "Asia/Shanghai")
}

var stampRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}) (DEBUG|INFO|WARN|ERROR) `)

// requireRecentStamp checks that each line starts with a timestamp without an
// offset that, read in zone, is the current time.
func requireRecentStamp(t *testing.T, out, zone string) {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		m := stampRe.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("line does not start with a timestamp without offset: %q", line)
		}
		at, err := time.ParseInLocation("2006-01-02T15:04:05.000", m[1], loc)
		if err != nil {
			t.Fatal(err)
		}
		if d := time.Since(at); d < -time.Second || d > time.Minute {
			t.Errorf("timestamp %s read in %s is %v away from now", m[1], zone, d)
		}
	}
}
