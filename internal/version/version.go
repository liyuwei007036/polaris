// Package version holds the build-time version of the polaris binary.
// Release builds inject the tag version via
// -ldflags "-X github.com/liyuwei007036/polaris/internal/version.Version=...";
// local builds keep the "dev" default so update checks stay silent.
package version

import (
	"strconv"
	"strings"
)

var Version = "dev"

// IsNewer reports whether latest is a strictly newer dotted-numeric version than
// current. Non-numeric versions ("dev", commit hashes) never compare as
// outdated, so development builds are not prompted to update.
func IsNewer(latest, current string) bool {
	latestParts, ok := parts(strings.TrimPrefix(strings.TrimSpace(latest), "v"))
	if !ok {
		return false
	}
	currentParts, ok := parts(strings.TrimPrefix(strings.TrimSpace(current), "v"))
	if !ok {
		return false
	}
	for i := 0; i < len(latestParts) || i < len(currentParts); i++ {
		l, c := 0, 0
		if i < len(latestParts) {
			l = latestParts[i]
		}
		if i < len(currentParts) {
			c = currentParts[i]
		}
		if l != c {
			return l > c
		}
	}
	return false
}

func parts(value string) ([]int, bool) {
	if value == "" {
		return nil, false
	}
	segments := strings.Split(value, ".")
	out := make([]int, len(segments))
	for i, segment := range segments {
		number, err := strconv.Atoi(segment)
		if err != nil || number < 0 {
			return nil, false
		}
		out[i] = number
	}
	return out, true
}
