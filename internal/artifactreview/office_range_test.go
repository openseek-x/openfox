package artifactreview

import (
	"errors"
	"openfox/internal/office"
	"os"
	"strings"
	"testing"
)

func TestOfficePartialRangesPreserveUnselectedText(t *testing.T) {
	for _, ext := range []string{"docx", "pptx"} {
		t.Run(ext, func(t *testing.T) {
			mime := office.MIMEForName("file." + ext)
			raw, err := os.ReadFile("../office/testdata/sample." + ext)
			if err != nil {
				t.Fatal(err)
			}
			p, err := office.Read(raw, mime)
			if err != nil {
				t.Fatal(err)
			}
			u := p.View().Units[0]
			raw, err = p.Replace([]office.Change{{ID: u.ID, Expected: u.Text, Replacement: "前🙂中后末"}})
			if err != nil {
				t.Fatal(err)
			}
			p, _ = office.Read(raw, mime)
			u = p.View().Units[0]
			a := Anchor{Kind: "office", Target: u.ID, Start: len("前"), End: len("前🙂"), Quote: "🙂"}
			b := Anchor{Kind: "office", Target: u.ID, Start: len("前🙂中"), End: len("前🙂中后"), Quote: "后"}
			for _, anchor := range []Anchor{a, b} {
				if err = ValidateAnchor(string(raw), mime, anchor); err != nil {
					t.Fatal(err)
				}
			}
			snapshots := []Snapshot{{ThreadID: "a", Anchor: a}, {ThreadID: "b", Anchor: b}}
			if err = ValidateSelection(snapshots); err != nil {
				t.Fatal(err)
			}
			sub := Submission{SHA256: Digest(string(raw)), Edits: []Edit{{ThreadID: "a", Expected: "🙂", Replacement: "开始"}, {ThreadID: "b", Expected: "后", Replacement: "结束"}}}
			out, err := Apply(string(raw), mime, snapshots, sub)
			if err != nil {
				t.Fatal(err)
			}
			q, err := office.Read([]byte(out), mime)
			if err != nil || q.View().Units[0].Text != "前开始中结束末" || q.View().Units[1].Text != p.View().Units[1].Text {
				t.Fatalf("range escaped: %v", err)
			}
			bad := a
			bad.Start++
			if ValidateAnchor(string(raw), mime, bad) == nil {
				t.Fatal("split UTF-8 accepted")
			}
			bad = a
			bad.Quote = "other"
			if ValidateAnchor(string(raw), mime, bad) == nil {
				t.Fatal("stale quote accepted")
			}
			bad = a
			bad.End = len(u.Text) + 1
			if ValidateAnchor(string(raw), mime, bad) == nil {
				t.Fatal("out-of-range accepted")
			}
			bad = a
			bad.End = len("前🙂中")
			bad.Quote = "🙂中"
			if !errors.Is(ValidateSelection([]Snapshot{{Anchor: a}, {Anchor: bad}}), ErrOverlap) {
				t.Fatal("overlap accepted")
			}
			whole := Anchor{Kind: "office", Target: u.ID, Quote: u.Text}
			if !errors.Is(ValidateSelection([]Snapshot{{Anchor: a}, {Anchor: whole}}), ErrOverlap) {
				t.Fatal("whole-unit overlap accepted")
			}
			withOverlap := append(append([]Snapshot(nil), snapshots...), Snapshot{ThreadID: "whole", Anchor: whole})
			if _, err = Apply(string(raw), mime, withOverlap, sub); err != nil {
				t.Fatal("unapplied overlapping opinion blocked edits", err)
			}
			conflict := sub
			conflict.Edits = append(append([]Edit(nil), sub.Edits...), Edit{ThreadID: "whole", Expected: u.Text, Replacement: "whole"})
			if _, err = Apply(string(raw), mime, withOverlap, conflict); !errors.Is(err, ErrOverlap) {
				t.Fatal("overlapping edits accepted", err)
			}
			sub.SHA256 = strings.Repeat("0", 64)
			if _, err = Apply(string(raw), mime, snapshots, sub); err == nil {
				t.Fatal("stale file accepted")
			}
		})
	}
}
