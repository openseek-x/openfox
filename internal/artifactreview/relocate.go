package artifactreview

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"openfox/internal/office"
)

// Resolution never changes the original anchor. Only exact results carry an
// actionable target. Positional IDs alone are not identities across exports.
type Resolution struct {
	Status string  `json:"status"`
	Reason string  `json:"reason"`
	Anchor *Anchor `json:"anchor,omitempty"`
}

func resolved(a Anchor) Resolution                { return Resolution{"exact", "matched", &a} }
func unresolved(status, reason string) Resolution { return Resolution{Status: status, Reason: reason} }
func choose(matches []Anchor) Resolution {
	if len(matches) == 1 {
		return resolved(matches[0])
	}
	if len(matches) > 1 {
		return unresolved("ambiguous", "multiple_matches")
	}
	return unresolved("orphaned", "not_found")
}
func occurrences(text, quote string) []int {
	if quote == "" {
		return nil
	}
	var found []int
	for from := 0; from <= len(text)-len(quote); {
		n := strings.Index(text[from:], quote)
		if n < 0 {
			break
		}
		n += from
		found = append(found, n)
		if len(found) > 100 {
			break
		}
		from = n + 1
	}
	return found
}
func contextMatch(old, next string, start, end, at int) bool {
	// Match up to 32 Unicode characters on both sides; never split a rune.
	leftAt, rightAt := start, end
	for i := 0; i < 32 && leftAt > 0; i++ {
		_, n := utf8.DecodeLastRuneInString(old[:leftAt])
		leftAt -= n
	}
	for i := 0; i < 32 && rightAt < len(old); i++ {
		_, n := utf8.DecodeRuneInString(old[rightAt:])
		rightAt += n
	}
	left, right := old[leftAt:start], old[end:rightAt]
	return len(left)+len(right) > 0 && strings.HasSuffix(next[:at], left) && strings.HasPrefix(next[at+end-start:], right)
}

func ResolveAnchor(old, next, mime string, a Anchor) (Resolution, error) {
	if !ValidSource(next, mime) {
		return Resolution{}, ErrInvalid
	}
	if err := ValidateAnchor(old, mime, a); err != nil {
		return Resolution{}, err
	}
	if old == next {
		return resolved(a), nil
	}
	switch a.Kind {
	case "text":
		var matches, contextual []Anchor
		for _, at := range occurrences(next, a.Quote) {
			b := a
			b.Start = at
			b.End = at + len(a.Quote)
			matches = append(matches, b)
			if contextMatch(old, next, a.Start, a.End, at) {
				contextual = append(contextual, b)
			}
		}
		if len(matches) > 100 {
			return unresolved("ambiguous", "multiple_matches"), nil
		}
		if len(matches) > 1 && len(contextual) == 1 {
			return resolved(contextual[0]), nil
		}
		if len(matches) == 0 && strings.Contains(strings.Join(strings.Fields(next), " "), strings.Join(strings.Fields(a.Quote), " ")) {
			return unresolved("approximate", "text_changed"), nil
		}
		return choose(matches), nil
	case "cell":
		fields, err := CSVFields(next)
		if err != nil {
			return Resolution{}, err
		}
		var matches []Anchor
		for _, row := range fields {
			for _, b := range row {
				if b.Quote == a.Quote && a.Quote != "" {
					matches = append(matches, b)
				}
			}
		}
		return choose(matches), nil
	case "office", "office_range":
		before, err := office.Read([]byte(old), mime)
		if err != nil {
			return Resolution{}, err
		}
		after, err := office.Read([]byte(next), mime)
		if err != nil {
			return Resolution{}, err
		}
		if a.Kind == "office_range" {
			return resolveSheetRange(after, a), nil
		}
		unit, _ := before.Unit(a.Target)
		var matches, stable []Anchor
		for _, u := range after.View().Units {
			if !u.Editable || u.Text != unit.Text {
				continue
			}
			b := a
			b.Target = u.ID
			if validateOfficeAnchor(after, b) != nil {
				continue
			}
			matches = append(matches, b)
			// Slide part + shape + run offset survive slide reordering. Neither a
			// paragraph index nor a cell address survives insertion before it.
			if unit.SlideSource != nil && u.SlideSource != nil && *unit.SlideSource == *u.SlideSource {
				stable = append(stable, b)
			}
		}
		if len(stable) == 1 {
			return resolved(stable[0]), nil
		}
		if len(matches) == 0 {
			for _, u := range after.View().Units {
				if a.Quote != "" && strings.Contains(u.Text, a.Quote) {
					return unresolved("approximate", "unit_changed"), nil
				}
			}
		}
		return choose(matches), nil
	default:
		// Visual geometry cannot establish content identity on changed bytes.
		return unresolved("orphaned", "visual_changed"), nil
	}
}

func cellRef(row, col int) string {
	col++
	s := ""
	for col > 0 {
		col--
		s = string(rune('A'+col%26)) + s
		col /= 26
	}
	return s + strconv.Itoa(row+1)
}
func resolveSheetRange(p *office.Package, a Anchor) Resolution {
	r1, c1, r2, c2, _ := office.RangeBounds(a.Range)
	cells := map[[2]int]string{}
	for _, u := range p.View().Units {
		if c := u.CellSource; c != nil && c.Part == a.Target {
			cells[[2]int{c.Row, c.Column}] = u.Text
		}
	}
	lines := strings.Split(a.Quote, "\n")
	// Find a nonempty pivot; blank rectangles are intrinsically ambiguous.
	pr, pc, pivot := 0, 0, ""
	for r, line := range lines {
		for c, v := range strings.Split(line, "\t") {
			if pivot == "" && v != "" {
				pr, pc, pivot = r, c, v
			}
		}
	}
	if pivot == "" {
		return unresolved("ambiguous", "empty_range")
	}
	var matches []Anchor
	for at, v := range cells {
		if v != pivot {
			continue
		}
		row, col := at[0]-pr, at[1]-pc
		if row < 0 || col < 0 || row+r2-r1 >= 1048576 || col+c2-c1 >= 16384 {
			continue
		}
		rows := make([]string, r2-r1+1)
		for r := range rows {
			cols := make([]string, c2-c1+1)
			for c := range cols {
				cols[c] = cells[[2]int{row + r, col + c}]
			}
			rows[r] = strings.Join(cols, "\t")
		}
		if strings.Join(rows, "\n") != a.Quote {
			continue
		}
		b := a
		b.Range = cellRef(row, col)
		if r2 != r1 || c2 != c1 {
			b.Range += ":" + cellRef(row+r2-r1, col+c2-c1)
		}
		matches = append(matches, b)
		if len(matches) > 1 {
			break
		}
	}
	return choose(matches)
}

// PDF text is client-extracted feedback, not authority to edit PDF bytes.
func validatePDFText(a Anchor) error {
	if a.Start != 0 || a.End != 0 || a.Target != "" || a.Range != "" || a.Row != 0 || a.Column != 0 || a.X != 0 || a.Y != 0 || a.Width != 0 || a.Height != 0 || a.Page < 1 || a.Page > 1000 || a.Quote == "" || len(a.Quote) > MaxQuoteBytes || !utf8.ValidString(a.Quote) || len(a.Quads) == 0 || len(a.Quads) > 128 || len(a.ViewBox) != 4 {
		return ErrInvalid
	}
	for _, v := range a.ViewBox {
		if !finiteCoordinate(v) {
			return ErrInvalid
		}
	}
	if a.ViewBox[2] <= a.ViewBox[0] || a.ViewBox[3] <= a.ViewBox[1] {
		return ErrInvalid
	}
	for _, q := range a.Quads {
		if len(q) != 8 {
			return ErrInvalid
		}
		area := 0.0
		for i := 0; i < 8; i += 2 {
			j := (i + 2) % 8
			area += q[i]*q[j+1] - q[j]*q[i+1]
		}
		if area > -0.000001 && area < 0.000001 {
			return ErrInvalid
		}
		for i, v := range q {
			if !finiteCoordinate(v) || v < a.ViewBox[i%2]-1 || v > a.ViewBox[i%2+2]+1 {
				return ErrInvalid
			}
		}
	}
	return nil
}
func finiteCoordinate(n float64) bool { return n >= -1e7 && n <= 1e7 }
