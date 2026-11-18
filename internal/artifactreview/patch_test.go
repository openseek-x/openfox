package artifactreview

import (
	"encoding/csv"
	"strings"
	"testing"
)

func TestExactUTF8Edits(t *testing.T) {
	text := "前🙂重复\r\n重复结束"
	a := Anchor{Kind: "text", Start: 7, End: 13, Quote: "重复"}
	snapshots := []Snapshot{{ThreadID: "one", Anchor: a}}
	sub := Submission{SHA256: Digest(text), Edits: []Edit{{ThreadID: "one", Expected: "重复", Replacement: "精简"}}}
	got, err := Apply(text, "text/markdown", snapshots, sub)
	if err != nil || got != "前🙂精简\r\n重复结束" {
		t.Fatalf("%q %v", got, err)
	}
	for name, mutate := range map[string]func(*Submission){
		"wrong sha":   func(s *Submission) { s.SHA256 = strings.Repeat("0", 64) },
		"wrong quote": func(s *Submission) { s.Edits[0].Expected = "其他" },
		"unselected":  func(s *Submission) { s.Edits[0].ThreadID = "other" },
		"no-op":       func(s *Submission) { s.Edits[0].Replacement = "重复" },
		"nul":         func(s *Submission) { s.Edits[0].Replacement = "\x00" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := sub
			copy.Edits = append([]Edit(nil), sub.Edits...)
			mutate(&copy)
			if _, err := Apply(text, "text/plain", snapshots, copy); err == nil {
				t.Fatal("accepted invalid edit")
			}
		})
	}
	a.Start = 4
	a.End = 7
	a.Quote = text[4:7]
	if ValidateAnchor(text, "text/plain", a) == nil {
		t.Fatal("split UTF-8 accepted")
	}
	overlap := []Snapshot{{ThreadID: "a", Anchor: Anchor{Kind: "text", Start: 0, End: 7, Quote: text[:7]}}, {ThreadID: "b", Anchor: Anchor{Kind: "text", Start: 3, End: 13, Quote: text[3:13]}}}
	if ValidateSelection(overlap) != ErrOverlap {
		t.Fatal("overlapping replacements must be rejected")
	}
	if _, err := Apply(text, "text/plain", overlap, Submission{SHA256: Digest(text), Edits: []Edit{{ThreadID: "a", Expected: text[:7], Replacement: "A"}, {ThreadID: "b", Expected: text[3:13], Replacement: "B"}}}); err == nil {
		t.Fatal("overlap accepted")
	}
}
func TestCSVSpansAndEscaping(t *testing.T) {
	text := "名称,说明,数量\r\n\"上,海\",\"多行\r\n\"\"引号\"\"\",\r\n\n北京,完成,2\n"
	fields, err := CSVFields(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields[1][1].Quote != "\"多行\r\n\"\"引号\"\"\"" {
		t.Fatalf("%+v", fields)
	}
	a := fields[1][2]
	if err = ValidateAnchor(text, "text/csv", a); err != nil {
		t.Fatal(err)
	}
	got, err := Apply(text, "text/csv", []Snapshot{{ThreadID: "a", Anchor: a}}, Submission{SHA256: Digest(text), Edits: []Edit{{ThreadID: "a", Expected: "", Replacement: "3,\"4\"\n5"}}})
	if err != nil {
		t.Fatal(err)
	}
	r := csv.NewReader(strings.NewReader(got))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil || records[1][2] != "3,\"4\"\n5" {
		t.Fatalf("%q %v", got, err)
	}
	a.Row = 2
	if ValidateAnchor(text, "text/csv", a) == nil {
		t.Fatal("wrong row accepted")
	}
	for _, bad := range []string{"a,\"unterminated", "a,b\"c", "\"a\"junk,b"} {
		if _, err := CSVFields(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestCSVBOMIsPreservedOutsideTheFirstCell(t *testing.T) {
	text := "\ufeff\"标题\",值\r\n甲,1"
	rows, err := CSVFields(text)
	if err != nil {
		t.Fatal(err)
	}
	a := rows[0][0]
	if a.Start != 3 || a.Quote != "\"标题\"" {
		t.Fatalf("BOM anchor: %+v", a)
	}
	changed, err := Apply(text, "text/csv", []Snapshot{{ThreadID: "a", Anchor: a}}, Submission{SHA256: Digest(text), Edits: []Edit{{ThreadID: "a", Expected: a.Quote, Replacement: "名称"}}})
	if err != nil || !strings.HasPrefix(changed, "\ufeff\"名称\",") {
		t.Fatalf("%q %v", changed, err)
	}
}

func TestOverlappingDiscussionAllowsSingleReplacement(t *testing.T) {
	text := "abcdef"
	a := Snapshot{ThreadID: "11111111-1111-4111-8111-111111111111", Body: "first", Anchor: Anchor{Kind: "text", Start: 0, End: 3, Quote: "abc"}}
	b := Snapshot{ThreadID: "22222222-2222-4222-8222-222222222222", Body: "second", Anchor: Anchor{Kind: "text", Start: 1, End: 4, Quote: "bcd"}}
	input := Input{ID: "33333333-3333-4333-8333-333333333333", ArtifactID: "44444444-4444-4444-8444-444444444444", SHA256: Digest(text), Comments: []Snapshot{a, b}}
	if _, err := ValidateFileInput(input, File{SHA256: Digest(text), MIMEType: "text/plain"}, text); err != nil {
		t.Fatal("overlapping discussion rejected", err)
	}
	got, err := Apply(text, "text/plain", input.Comments, Submission{SHA256: Digest(text), Edits: []Edit{{ThreadID: a.ThreadID, Expected: "abc", Replacement: "A"}}})
	if err != nil || got != "Adef" {
		t.Fatalf("%q %v", got, err)
	}
}
