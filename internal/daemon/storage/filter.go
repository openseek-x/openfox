package storage

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
)

type CleanupFilter struct {
	Action      string          `json:"action"`
	Days        *int            `json:"days"`
	Categories  []string        `json:"categories"`
	Statuses    []string        `json:"statuses,omitempty"`
	MatchedRuns map[string]bool `json:"-"`
	PreviewID   string          `json:"preview_id,omitempty"`
}

var ErrInvalidFilter = errors.New("invalid storage cleanup filter")

func ParseCleanupFilter(raw []byte) (CleanupFilter, error) {
	var f CleanupFilter
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !json.Valid(raw) || d.Decode(&f) != nil || f.Days == nil || *f.Days < 0 || *f.Days > 3650 || len(f.Categories) == 0 || len(f.Categories) > 4 {
		return f, ErrInvalidFilter
	}
	switch f.Action {
	case "preview":
		if f.PreviewID != "" {
			return f, ErrInvalidFilter
		}
	case "clean":
		if _, err := hex.DecodeString(f.PreviewID); err != nil || len(f.PreviewID) != 64 {
			return f, ErrInvalidFilter
		}
	default:
		return f, ErrInvalidFilter
	}
	if len(f.Statuses) > 0 {
		if *f.Days != 0 || !slices.Equal(f.Categories, []string{CategoryTask}) || len(f.Statuses) > 7 {
			return f, ErrInvalidFilter
		}
		slices.Sort(f.Statuses)
		for i, v := range f.Statuses {
			if !slices.Contains([]string{"TRIAGE", "BACKLOG", "TODO", "IN_PROGRESS", "DONE", "CANCELLED", "DUPLICATE"}, v) || (i > 0 && f.Statuses[i-1] == v) {
				return f, ErrInvalidFilter
			}
		}
	}
	slices.Sort(f.Categories)
	for i, key := range f.Categories {
		if !slices.Contains([]string{CategoryTask, CategoryConversation, CategoryAutomation, CategoryWorkflow}, key) || (i > 0 && key == f.Categories[i-1]) {
			return f, ErrInvalidFilter
		}
	}
	return f, nil
}

// For a task-status request the center adds the eligible run IDs at claim time.
// Clients creating commands can never supply the envelope themselves.
type StatusCleanupClaim struct {
	Filter json.RawMessage `json:"filter"`
	Runs   []string        `json:"runs"`
}

func ParseClaimedFilter(raw []byte) (CleanupFilter, error) {
	var claim StatusCleanupClaim
	if json.Unmarshal(raw, &claim) != nil {
		return CleanupFilter{}, ErrInvalidFilter
	}
	if claim.Filter == nil {
		f, err := ParseCleanupFilter(raw)
		if len(f.Statuses) > 0 {
			return f, ErrInvalidFilter
		}
		return f, err
	}
	f, err := ParseCleanupFilter(claim.Filter)
	if err != nil || len(f.Statuses) == 0 || len(claim.Runs) > 500 {
		return f, ErrInvalidFilter
	}
	f.MatchedRuns = map[string]bool{}
	for _, id := range claim.Runs {
		f.MatchedRuns[id] = true
	}
	return f, nil
}
