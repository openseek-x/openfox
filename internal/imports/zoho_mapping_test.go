package imports

import (
	"context"
	"openfox/internal/integrations/zohobugtracker"
	"testing"
	"time"
)

func TestZohoRequiresExplicitStatus(t *testing.T) {
	ctx := context.Background()
	cfg := Config{Types: []TypeMapping{{Source: "Bug", UseDefault: true}}, Options: Options{ImportOpen: true, ImportDone: true}}
	infos := []rowInfo{{Data: rowData{SourceID: "2001", Title: "Bug", Status: "4001", TypeName: "Bug", Priority: "NONE", Zoho: &ZohoRecordMetadata{StatusLabel: "Open"}}}}
	l := lookups{statusByName: map[string]string{"4001": "00000000-0000-4000-8000-000000000001"}, typeByName: map[string]string{}, priorityLevels: 4, location: time.UTC}
	a, e := analyze(ctx, nil, "", cfg, infos, l, true, "zoho_bugtracker")
	if e != nil {
		t.Fatal(e)
	}
	if e = analyzeZoho(ctx, nil, "", cfg, infos, l, &a); e != nil {
		t.Fatal(e)
	}
	for _, issue := range a.Issues {
		if issue.Code == IssueStatusUnmapped {
			return
		}
	}
	t.Fatalf("no explicit status mapping, but no status_unmapped issue; cleared suggestion=%v issues=%v", a.Statuses[0].SuggestedStatusID, a.Issues)
}

func TestZohoRecordReporterDoesNotInventCreator(t *testing.T) {
	r := zohoRecord(zohobugtracker.Bug{ID: "10", Title: "Bug", ReporterID: "2060759", ReporterName: "Source reporter"})
	if r.Creator != (Person{}) || r.Assignee != (Person{}) {
		t.Fatalf("unverified creator or assignee: %#v %#v", r.Creator, r.Assignee)
	}
	if r.Reporter.ID != "us:2060759" {
		t.Fatalf("reporter=%#v", r.Reporter)
	}
	d := rowFromRecord(&r)
	users := collectUsers([]rowInfo{{Data: d}}, time.UTC)
	if len(users) != 1 || users[0].SourceID != "us:2060759" || users[0].Count != 1 || len(d.People) != 1 {
		t.Fatalf("source reporter counted more than once: users=%#v people=%#v", users, d.People)
	}
}
