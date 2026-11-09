package collaboration

import (
	"reflect"
	"testing"
)

func TestGitStatusSources(t *testing.T) {
	if got := GitStatusSources("IN_PROGRESS"); !reflect.DeepEqual(got, []string{"TRIAGE", "BACKLOG", "TODO"}) {
		t.Fatalf("IN_PROGRESS sources = %v", got)
	}
	done := GitStatusSources("DONE")
	for _, terminal := range []string{"DONE", "CANCELLED", "DUPLICATE"} {
		for _, category := range append(done, GitStatusSources("IN_PROGRESS")...) {
			if category == terminal {
				t.Fatalf("terminal category %s must never be a source", terminal)
			}
		}
	}
	if GitStatusSources("CANCELLED") != nil {
		t.Fatal("unsupported target must have no sources")
	}
}
