package logger

import (
	"strings"
	"time"
)

// defaultZoneName is the time zone of log timestamps when TZ is unset or
// names no known zone.
const defaultZoneName = "Asia/Shanghai"

// defaultZoneFallback stands in for Asia/Shanghai where the system has no zone
// database. That zone has had no daylight saving time since 1991.
var defaultZoneFallback = time.FixedZone("CST", 8*3600)

// logZone returns the time zone of log timestamps: the zone that tz (the TZ
// environment variable) names, or Asia/Shanghai when tz is empty or names no
// known zone. invalid is a tz value that was not used, or "".
func logZone(tz string) (zone *time.Location, invalid string) {
	name := strings.TrimPrefix(strings.TrimSpace(tz), ":")
	if name != "" && name != defaultZoneName {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc, ""
		}
		invalid = tz
	}
	if loc, err := time.LoadLocation(defaultZoneName); err == nil {
		return loc, invalid
	}
	return defaultZoneFallback, invalid
}
