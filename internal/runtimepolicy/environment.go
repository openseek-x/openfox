package runtimepolicy

import (
	"regexp"
	"strings"
)

var EnvironmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

var ReservedEnvironmentNames = []string{"CODEX_HOME", "HOME", "PATH"}
var ReservedEnvironmentPrefixes = []string{"OPENFOX_", "CODEX_", "DYLD_", "LD_"}

// ReservedEnvironment keeps center-side machine settings and the final daemon
// boundary consistent. Existing Agent environment storage remains unchanged.
func ReservedEnvironment(name string) bool {
	for _, value := range ReservedEnvironmentNames {
		if name == value {
			return true
		}
	}
	for _, prefix := range ReservedEnvironmentPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
