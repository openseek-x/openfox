package artifactreview

import (
	"openfox/internal/office"
	"sort"
	"unicode/utf8"
)

func validateOfficeAnchor(p *office.Package, a Anchor) error {
	u, ok := p.Unit(a.Target)
	if a.Kind != "office" || a.Range != "" || !ok || !u.Editable || len(a.Quote) > MaxQuoteBytes || a.Row != 0 || a.Column != 0 || a.Page != 0 || a.X != 0 || a.Y != 0 || a.Width != 0 || a.Height != 0 {
		return ErrInvalid
	}
	if a.Start == 0 && a.End == 0 {
		if a.Quote != u.Text {
			return ErrInvalid
		}
		return nil
	}
	// Partial text selections require a source-mapped Word or slide text run.
	if (u.Source == nil && u.SlideSource == nil) || a.Start < 0 || a.End <= a.Start || a.End > len(u.Text) || !utf8.ValidString(u.Text[:a.Start]) || !utf8.ValidString(u.Text[a.Start:a.End]) || a.Quote != u.Text[a.Start:a.End] {
		return ErrInvalid
	}
	return nil
}
func applyOffice(text, mime string, snapshots []Snapshot, sub Submission) (string, error) {
	if Digest(text) != sub.SHA256 || len(sub.Edits) == 0 || len(sub.Edits) > 20 || len(sub.Summary) > 8192 || !ValidText(sub.Summary) {
		return "", ErrInvalid
	}
	p, err := office.Read([]byte(text), mime)
	if err != nil {
		return "", ErrInvalid
	}
	selected := []Snapshot{}
	for _, edit := range sub.Edits {
		for _, snapshot := range snapshots {
			if snapshot.ThreadID == edit.ThreadID {
				selected = append(selected, snapshot)
			}
		}
	}
	if err = ValidateSelection(selected); err != nil {
		return "", err
	}
	allowed := map[string]Anchor{}
	for _, s := range snapshots {
		if _, ok := allowed[s.ThreadID]; ok {
			return "", ErrInvalid
		}
		if s.Anchor.Kind == "region" || s.Anchor.Kind == "office_range" {
			if err = ValidateAnchor(text, mime, s.Anchor); err != nil {
				return "", err
			}
			continue
		}
		if err = validateOfficeAnchor(p, s.Anchor); err != nil {
			return "", err
		}
		allowed[s.ThreadID] = s.Anchor
	}
	type replacement struct {
		start, end int
		text       string
	}
	groups := map[string][]replacement{}
	seen := map[string]bool{}
	for _, e := range sub.Edits {
		a, ok := allowed[e.ThreadID]
		if !ok || seen[e.ThreadID] || e.Expected != a.Quote || e.Replacement == a.Quote {
			return "", ErrInvalid
		}
		seen[e.ThreadID] = true
		u, _ := p.Unit(a.Target)
		end := a.End
		if a.Start == 0 && end == 0 {
			end = len(u.Text)
		}
		groups[a.Target] = append(groups[a.Target], replacement{a.Start, end, e.Replacement})
	}
	changes := make([]office.Change, 0, len(groups))
	for id, edits := range groups {
		u, _ := p.Unit(id)
		sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
		value := u.Text
		for _, edit := range edits {
			value = value[:edit.start] + edit.text + value[edit.end:]
		}
		changes = append(changes, office.Change{ID: id, Expected: u.Text, Replacement: value})
	}
	out, err := p.Replace(changes)
	if err != nil {
		return "", ErrInvalid
	}
	return string(out), nil
}
