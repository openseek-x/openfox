package artifactreview

import (
	"fmt"
	"openfox/internal/office"
	"os"
	"strings"
	"testing"
)

func TestSpreadsheetFeedbackRangeIsNotEditAuthority(t *testing.T) {
	raw, e := os.ReadFile("../../tests/frontend/e2e/fixtures/office-preview/complex.xlsx")
	if e != nil {
		t.Fatal(e)
	}
	p, e := office.Read(raw, office.XLSX)
	if e != nil {
		t.Fatal(e)
	}
	quote, e := p.RangeQuote("xl/worksheets/sheet1.xml", "B3:D4")
	if e != nil {
		t.Fatal(e)
	}
	a := Anchor{Kind: "office_range", Target: "xl/worksheets/sheet1.xml", Range: "B3:D4", Quote: quote}
	if e = ValidateAnchor(string(raw), office.XLSX, a); e != nil {
		t.Fatal(e)
	}
	snapshots := []Snapshot{{ThreadID: "range", Anchor: a}}
	if _, e = Apply(string(raw), office.XLSX, snapshots, Submission{SHA256: Digest(string(raw)), Edits: []Edit{{ThreadID: "range", Expected: quote, Replacement: "overwrite"}}}); e == nil {
		t.Fatal("feedback became edit authority")
	}
	for _, bad := range []Anchor{{Kind: "office_range", Target: a.Target, Range: a.Range, Quote: "stale"}, {Kind: "office_range", Target: "xl/worksheets/missing.xml", Range: "A1"}, {Kind: "office_range", Target: a.Target, Range: "A1:Z10000"}, {Kind: "office_range", Target: a.Target, Range: a.Range, Quote: quote, Start: 1}} {
		if ValidateAnchor(string(raw), office.XLSX, bad) == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	overlap := a
	overlap.Range = "C4:D5"
	if e = ValidateSelection([]Snapshot{{Anchor: a}, {Anchor: overlap}}); e != nil {
		t.Fatal("overlapping readonly feedback rejected", e)
	}
	other := a
	other.Target = "xl/worksheets/sheet2.xml"
	if e = ValidateSelection([]Snapshot{{Anchor: a}, {Anchor: other}}); e != nil {
		t.Fatal("different sheets conflict", e)
	}
	var cell office.Unit
	for _, u := range p.View().Units {
		if u.CellSource.Sheet == 1 {
			cell = u
			break
		}
	}
	exact := Snapshot{ThreadID: "cell", Anchor: Anchor{Kind: "office", Target: cell.ID, Quote: cell.Text}}
	out, e := Apply(string(raw), office.XLSX, append(snapshots, exact), Submission{SHA256: Digest(string(raw)), Edits: []Edit{{ThreadID: "cell", Expected: cell.Text, Replacement: "只改这个单元格"}}})
	if e != nil {
		t.Fatal(e)
	}
	updated, e := office.Read([]byte(out), office.XLSX)
	if e != nil {
		t.Fatal(e)
	}
	if q, _ := updated.RangeQuote(a.Target, a.Range); q != quote {
		t.Fatal("feedback range changed")
	}
}

func TestSpreadsheetMessageValidatesOriginalRanges(t *testing.T) {
	raw, err := os.ReadFile("../../tests/frontend/e2e/fixtures/office-preview/complex.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	p, err := office.Read(raw, office.XLSX)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{ID: "10000000-0000-4000-8000-000000000001", ArtifactID: "10000000-0000-4000-8000-000000000002", SHA256: Digest(string(raw))}
	for i, ref := range []string{"B3:D4", "A9:B9", "A1:D1", "A3:D3"} {
		quote, err := p.RangeQuote("xl/worksheets/sheet1.xml", ref)
		if err != nil {
			t.Fatal(err)
		}
		input.Comments = append(input.Comments, Snapshot{ThreadID: fmt.Sprintf("20000000-0000-4000-8000-%012d", i+1), Body: "Review original cells", Anchor: Anchor{Kind: "office_range", Target: "xl/worksheets/sheet1.xml", Range: ref, Quote: quote}})
	}
	for _, u := range p.View().Units {
		if u.CellSource.Sheet == 0 && (u.Label == "A7" || u.Label == "B7") {
			input.Comments = append(input.Comments, Snapshot{ThreadID: fmt.Sprintf("20000000-0000-4000-8000-%012d", len(input.Comments)+1), Body: "Review duplicate", Anchor: Anchor{Kind: "office", Target: u.ID, Quote: u.Text}})
		}
	}
	file := File{SHA256: input.SHA256, MIMEType: office.XLSX, Version: 1}
	validated, err := ValidateFileInput(input, file, string(raw))
	if err != nil || validated.validated == "" {
		t.Fatal("valid original feedback rejected", err)
	}
	input.Comments[0].Anchor.Quote = "stale formula"
	if _, err = ValidateFileInput(input, file, string(raw)); err == nil {
		t.Fatal("stale original quote accepted")
	}
	input.SHA256 = strings.Repeat("0", 64)
	if _, err = ValidateFileInput(input, file, string(raw)); err == nil {
		t.Fatal("stale source hash accepted")
	}
}
