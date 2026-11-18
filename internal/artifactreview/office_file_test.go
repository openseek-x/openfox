package artifactreview

import (
	"openfox/internal/office"
	"os"
	"testing"
)

func TestOfficeWholeFileReplacementBoundaries(t *testing.T) {
	raw, err := os.ReadFile("../office/testdata/sample.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	p, err := office.Read(raw, office.XLSX)
	if err != nil {
		t.Fatal(err)
	}
	u := p.View().Units[0]
	updated, err := p.Replace([]office.Change{{ID: u.ID, Expected: u.Text, Replacement: "Updated"}})
	if err != nil {
		t.Fatal(err)
	}
	comments := []Snapshot{{ThreadID: "one", Anchor: Anchor{Kind: "office", Target: u.ID, Quote: u.Text}}}
	in := OfficeReplacement{SHA256: Digest(string(raw)), Summary: "updated", Content: updated}
	if err = ValidateOfficeReplacement(string(raw), office.XLSX, comments, in); err != nil {
		t.Fatal(err)
	}
	comments = append(comments, Snapshot{ThreadID: "two", Anchor: comments[0].Anchor})
	if err = ValidateOfficeReplacement(string(raw), office.XLSX, comments, in); err != nil {
		t.Fatal("overlapping opinions blocked whole-file candidate", err)
	}
	same := in
	if in.Hash() != same.Hash() {
		t.Fatal("unstable retry")
	}
	same.Summary = "another"
	if in.Hash() == same.Hash() {
		t.Fatal("different retry same hash")
	}
	for _, bad := range []OfficeReplacement{{SHA256: "stale", Content: updated}, {SHA256: in.SHA256, Content: raw}, {SHA256: in.SHA256, Content: []byte("not Office")}, {SHA256: in.SHA256, Content: make([]byte, office.MaxBytes+1)}} {
		if ValidateOfficeReplacement(string(raw), office.XLSX, comments, bad) == nil {
			t.Fatal("unsafe file accepted")
		}
	}
	if ValidateOfficeReplacement(string(raw), office.DOCX, comments, in) == nil {
		t.Fatal("wrong format accepted")
	}
	comments[0].Anchor.Quote = "stale"
	if ValidateOfficeReplacement(string(raw), office.XLSX, comments, in) == nil {
		t.Fatal("stale quote accepted")
	}
}
