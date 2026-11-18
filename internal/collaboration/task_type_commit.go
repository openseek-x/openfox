package collaboration

import (
	"regexp"
	"strings"
	"unicode"
)

var commitTypePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,31}$`)

// NormalizeCommitType keeps NULL/empty as the inherited feat default. Reject
// control characters before trimming so a newline cannot become an instruction.
func NormalizeCommitType(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if strings.IndexFunc(*value, unicode.IsControl) >= 0 {
		return nil, invalidInput("invalid commit type")
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil, nil
	}
	if !commitTypePattern.MatchString(normalized) {
		return nil, invalidInput("invalid commit type")
	}
	return &normalized, nil
}
