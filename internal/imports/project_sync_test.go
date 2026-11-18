package imports

import "testing"

func TestSyncFieldPreservesConflictingEdits(t *testing.T) {
	for _, tc := range []struct {
		base, local, remote, in, out string
		conflict                     bool
	}{
		{"old", "old", "inbound", "inbound", "", false},
		{"old", "outbound", "old", "", "outbound", false},
		{"old", "same", "same", "", "", false},
		{"old", "local", "remote", "", "", true},
	} {
		in, out, conflict := syncField(tc.base, tc.local, tc.remote)
		if conflict != tc.conflict || (in == nil) != (tc.in == "") || (in != nil && *in != tc.in) || (out == nil) != (tc.out == "") || (out != nil && *out != tc.out) {
			t.Fatalf("unexpected resolution for %+v", tc)
		}
	}
	in, out, conflict := syncField("old", "", "old")
	if in != nil || out == nil || *out != "" || conflict {
		t.Fatal("clearing a field must be propagated")
	}
}

func TestFileEpicSelectionKeepsLegacyAndProjectOnlyImports(t *testing.T) {
	headers := []string{"Issue id", "Summary", "Issue Type"}
	p := buildPlan(SourceJiraCSV, headers, SuggestedConfig(headers).Columns)
	rows := []rawItem{{Cells: []string{"1", "task", "Task"}}, {Cells: []string{"2", "initiative", "Epic"}}}
	selected := true
	result := classify(p, rows, Options{FileProjects: &selected})
	if !result[0].Filtered || result[1].Filtered || !result[1].Epic {
		t.Fatal(result)
	}
	result = classify(p, rows, Options{ImportOpen: true})
	if result[1].Filtered || result[1].Epic {
		t.Fatal("older drafts must retain their task interpretation")
	}
}

func TestSyncStatusIndependentBaselines(t *testing.T) {
	str := func(s string) *string { return &s }
	mappings := map[string]string{"open": "todo", "done": "done", "running": "doing"}
	cases := []struct {
		name, oldSource, oldMapped, oldLocal, local, remote, direction, wantIn, wantOut string
		wantErr                                                                         error
	}{
		{name: "inbound", oldSource: "open", oldMapped: "todo", oldLocal: "todo", local: "todo", remote: "done", wantIn: "done"},
		{name: "outbound", oldSource: "open", oldMapped: "todo", oldLocal: "todo", local: "done", remote: "open", wantOut: "done"},
		{name: "conflict", oldSource: "open", oldMapped: "todo", oldLocal: "todo", local: "doing", remote: "done", wantErr: errSyncConflict},
		{name: "converged", oldSource: "open", oldMapped: "todo", oldLocal: "todo", local: "done", remote: "done"},
		{name: "inbound preserves local", oldSource: "open", oldMapped: "todo", oldLocal: "todo", local: "done", remote: "open", direction: "INBOUND"},
		{name: "unmapped", oldSource: "open", oldMapped: "todo", oldLocal: "todo", local: "todo", remote: "custom", wantErr: errSyncStatusMapping},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := syncedItem{status: str(tc.local), baselineStatus: str(tc.oldLocal), baselineSourceStatus: str(tc.oldSource), baselineMappedStatus: str(tc.oldMapped)}
			in, out, e := syncStatus(v, tc.remote, mappings, tc.direction)
			if e != tc.wantErr || (in == nil) != (tc.wantIn == "") || (in != nil && *in != tc.wantIn) || (out == nil) != (tc.wantOut == "") || (out != nil && *out != tc.wantOut) {
				t.Fatalf("in=%v out=%v error=%v", in, out, e)
			}
		})
	}
	if in, out, e := syncStatus(syncedItem{status: str("done")}, "open", mappings, ""); in != nil || out != nil || e != nil {
		t.Fatal("upgrade replayed an edit")
	}
	mappings["completed"] = "done"
	v := syncedItem{status: str("done"), baselineStatus: str("todo"), baselineSourceStatus: str("open"), baselineMappedStatus: str("todo")}
	if _, _, e := syncStatus(v, "open", mappings, ""); e != errSyncStatusMapping {
		t.Fatal("ambiguous reverse mapping accepted")
	}
}
