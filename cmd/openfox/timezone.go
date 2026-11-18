package main

import (
	"os"
	"runtime"
	"strings"
	"time"
)

// probeTimezone reports an identifiable IANA zone of this daemon, not the
// browser's zone or an offset abbreviation. Unidentifiable system zones remain
// absent. Windows' native zone IDs are not IANA names and are not guessed.
func probeTimezone() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	tz, set := os.LookupEnv("TZ")
	target, _ := os.Readlink("/etc/localtime")
	return timezoneName(tz, set, target)
}

func timezoneName(tz string, set bool, localtimeTarget string) string {
	if set {
		if tz == "" {
			return "UTC"
		}
		tz = strings.TrimPrefix(tz, ":")
	} else {
		// macOS and common Unix distributions link localtime into zoneinfo.
		_, tz, _ = strings.Cut(localtimeTarget, "/zoneinfo/")
	}
	if tz == "" || tz == "Local" || len(tz) > 128 || strings.HasPrefix(tz, "/") || strings.Contains(tz, "..") {
		return ""
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return ""
	}
	return tz
}
