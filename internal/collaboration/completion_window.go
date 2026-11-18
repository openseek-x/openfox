package collaboration

import "time"

// Completion windows are elapsed hours, independent of calendar days and DST.
func completionDuration(window string) time.Duration {
	switch window {
	case "last_7_days":
		return 7 * 24 * time.Hour
	case "last_30_days":
		return 30 * 24 * time.Hour
	default:
		return 0
	}
}

// resolveCompletion shares one server anchor across SQL statements and pages.
// It freezes the window, not the mutable task data or the caller's permissions.
func (f *TaskFilter) resolveCompletion(cursor *taskCursor) error {
	if cursor != nil {
		if cursor.CompletedWindow != f.CompletedWindow ||
			(cursor.CompletedAsOf == nil) != (f.CompletedWindow == "") {
			return errInvalidCursor
		}
		if cursor.CompletedAsOf != nil {
			if f.CompletedAsOf != nil && !f.CompletedAsOf.Equal(*cursor.CompletedAsOf) {
				return errInvalidCursor
			}
			f.CompletedAsOf = cursor.CompletedAsOf
		}
	}
	f.ensureCompletionAnchor()
	return nil
}

// Other consumers of the shared filter (search and triage facets) construct a
// single query without a paging anchor. They still need one stable cutoff.
func (f *TaskFilter) ensureCompletionAnchor() {
	if f.CompletedWindow != "" && f.CompletedAsOf == nil {
		now := time.Now().UTC().Truncate(time.Microsecond)
		f.CompletedAsOf = &now
	}
}
