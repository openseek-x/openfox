package artifactreview

import (
	"errors"
	"openfox/internal/office"
	"os"
	"testing"
)

func TestOfficeAnchorsAndScope(t *testing.T) {
	raw, e := os.ReadFile("../office/testdata/sample.xlsx")
	if e != nil {
		t.Fatal(e)
	}
	p, e := office.Read(raw, office.XLSX)
	if e != nil {
		t.Fatal(e)
	}
	u := p.View().Units[0]
	a := Anchor{Kind: "office", Target: u.ID, Quote: u.Text}
	text := string(raw)
	if e = ValidateAnchor(text, office.XLSX, a); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []Anchor{{Kind: "office", Target: "unselected", Quote: u.Text}, {Kind: "office", Target: u.ID, Quote: "stale"}, {Kind: "office", Target: u.ID, Quote: u.Text, Start: 1}, {Kind: "text", Target: u.ID, Quote: u.Text}} {
		if e = ValidateAnchor(text, office.XLSX, bad); e == nil {
			t.Fatal("invalid anchor accepted")
		}
	}
	snapshots := []Snapshot{{ThreadID: "t1", Anchor: a}}
	sub := Submission{SHA256: Digest(text), Edits: []Edit{{ThreadID: "t1", Expected: u.Text, Replacement: "New value"}}}
	out, e := Apply(text, office.XLSX, snapshots, sub)
	if e != nil {
		t.Fatal(e)
	}
	q, e := office.Read([]byte(out), office.XLSX)
	if e != nil || q.View().Units[0].Text != "New value" {
		t.Fatal(e)
	}
	sub.Edits[0].ThreadID = "t2"
	if _, e = Apply(text, office.XLSX, snapshots, sub); e == nil {
		t.Fatal("unselected thread accepted")
	}
	sub.Edits[0].ThreadID = "t1"
	sub.SHA256 = "stale"
	if _, e = Apply(text, office.XLSX, snapshots, sub); e == nil {
		t.Fatal("stale SHA accepted")
	}
	if e = ValidateSelection(append(snapshots, Snapshot{ThreadID: "t2", Anchor: a})); !errors.Is(e, ErrOverlap) {
		t.Fatal(e)
	}
	other := a
	other.Target = p.View().Units[1].ID
	if e = ValidateSelection(append(snapshots, Snapshot{ThreadID: "t2", Anchor: other})); e != nil {
		t.Fatal(e)
	}
	for _, u := range p.View().Units {
		if u.Type == "formula" {
			if e = ValidateAnchor(text, office.XLSX, Anchor{Kind: "office", Target: u.ID, Quote: u.Text}); e == nil {
				t.Fatal("formula editable")
			}
		}
	}
}

func TestOfficeRegionFeedbackDoesNotAuthorizeSourceEdits(t *testing.T) {
	raw, err := os.ReadFile("../office/testdata/sample.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	p, err := office.Read(raw, office.XLSX)
	if err != nil {
		t.Fatal(err)
	}
	unit := p.View().Units[0]
	region := Anchor{Kind: "region", Page: 1, X: .1, Y: .2, Width: .3, Height: .4}
	if err = ValidateAnchor(string(raw), office.XLSX, region); err != nil {
		t.Fatal(err)
	}
	snapshots := []Snapshot{{ThreadID: "region", Anchor: region}, {ThreadID: "text", Anchor: Anchor{Kind: "office", Target: unit.ID, Quote: unit.Text}}}
	if err = ValidateSelection(snapshots); err != nil {
		t.Fatal(err)
	}
	sub := Submission{SHA256: Digest(string(raw)), Edits: []Edit{{ThreadID: "text", Expected: unit.Text, Replacement: "Updated"}}}
	if _, err = Apply(string(raw), office.XLSX, snapshots, sub); err != nil {
		t.Fatal(err)
	}
	sub.Edits[0].ThreadID = "region"
	if _, err = Apply(string(raw), office.XLSX, snapshots, sub); err == nil {
		t.Fatal("region authorized source edit")
	}
}
