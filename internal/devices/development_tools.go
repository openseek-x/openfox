package devices

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// DevelopmentTool is a read-only version probe made by the device daemon.
// Empty path/version mean they were not available; CheckedAt is the remote clock.
type DevelopmentTool struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Path      string    `json:"path"`
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
}

// ParseDevelopmentTools keeps malformed device reports from breaking the detail
// page. Missing data from older daemons remains an empty (not "missing") list.
func ParseDevelopmentTools(capabilities json.RawMessage) []DevelopmentTool {
	result := make([]DevelopmentTool, 0, 4)
	var document struct {
		Tools []json.RawMessage `json:"development_tools"`
	}
	if json.Unmarshal(capabilities, &document) != nil {
		return result
	}
	seen := map[string]bool{}
	for _, raw := range document.Tools {
		var item DevelopmentTool
		if json.Unmarshal(raw, &item) != nil || seen[item.Name] || item.CheckedAt.IsZero() {
			continue
		}
		switch item.Name {
		case "node", "npm", "python", "uv":
		default:
			continue
		}
		switch item.Status {
		case "AVAILABLE", "MISSING", "TIMEOUT", "ERROR":
		default:
			continue
		}
		if len(item.Version) > 256 || len(item.Path) > 4096 ||
			strings.IndexFunc(item.Version, unicode.IsControl) >= 0 || strings.IndexFunc(item.Path, unicode.IsControl) >= 0 {
			continue
		}
		if item.Status == "AVAILABLE" && (item.Version == "" || item.Path == "") {
			continue
		}
		seen[item.Name] = true
		result = append(result, item)
	}
	return result
}
