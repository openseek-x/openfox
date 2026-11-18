// Package artifactreview validates file feedback and exact edits in normal chat turns.
package artifactreview

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"openfox/internal/conversations"
	"openfox/internal/office"
)

const MaxFileBytes = 5 * 1024 * 1024
const MaxQuoteBytes = 8192
const MaxToolBytes = 256 * 1024
const MaxVisualBytes = 10 * 1024 * 1024

var (
	ErrInvalid     = errors.New("invalid artifact review input")
	ErrConflict    = errors.New("artifact review changed; refresh and retry")
	ErrUnavailable = errors.New("artifact review source is unavailable")
	ErrUnsupported = errors.New("AI revision requires supported text and a single-agent Codex conversation")
	ErrOverlap     = errors.New("selected annotation ranges overlap")
)

// Offsets are UTF-8 bytes in the immutable artifact, zero-based and half-open.
// For CSV, start/end and quote refer to the raw field, including CSV quoting.
// For Office, offsets are within the target's text; 0/0 selects the whole unit.
type Anchor struct {
	Quads   [][]float64 `json:"quads,omitempty"`
	ViewBox []float64   `json:"view_box,omitempty"`
	Kind    string      `json:"kind"`
	Start   int         `json:"start"`
	End     int         `json:"end"`
	Quote   string      `json:"quote"`
	Target  string      `json:"target,omitempty"`
	Range   string      `json:"range,omitempty"`
	Row     int         `json:"row,omitempty"`
	Column  int         `json:"column,omitempty"`
	// Region coordinates are fractions of the unrotated, displayed image/page.
	Page   int     `json:"page,omitempty"`
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	Width  float64 `json:"width,omitempty"`
	Height float64 `json:"height,omitempty"`
}

func VisualMIME(mime string) bool {
	return mime == "application/pdf" || mime == "image/png" || mime == "image/jpeg" || mime == "image/webp" || mime == "image/gif"
}

func TextMIME(mime string) bool {
	return mime == "text/plain" || mime == "text/markdown" || mime == "text/csv"
}

func RevisionMIME(mime string) bool { return TextMIME(mime) || office.MIME(mime) }

func ValidSource(text, mime string) bool {
	if office.MIME(mime) {
		_, err := office.Read([]byte(text), mime)
		return err == nil
	}
	if TextMIME(mime) {
		return ValidText(text)
	}
	return VisualMIME(mime) && len(text) > 0 && len(text) <= MaxVisualBytes
}

// Selection validation applies to actual edits. Discussion and request
// snapshots may overlap; applying overlapping replacements is unsafe.
func ValidateSelection(snapshots []Snapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	kind := snapshots[0].Anchor.Kind
	for _, snapshot := range snapshots {
		if snapshot.Anchor.Kind != kind {
			// Office 原版式的区域反馈可与精确文字批注共存；区域永远不能充当修改锚点。
			var regions, units, ranges []Snapshot
			for _, item := range snapshots {
				switch item.Anchor.Kind {
				case "region":
					regions = append(regions, item)
				case "office":
					units = append(units, item)
				case "office_range":
					ranges = append(ranges, item)
				default:
					return ErrInvalid
				}
			}
			if err := ValidateSelection(regions); err != nil {
				return err
			}
			if err := ValidateSelection(ranges); err != nil {
				return err
			}
			return ValidateSelection(units)
		}
	}
	if kind == "pdf_text" {
		return nil
	}
	if kind == "office_range" {
		// Ranges carry feedback, never edit authority. Overlapping comments are
		// valid; disjointness is required only for source edits below.
		for _, s := range snapshots {
			_, _, _, _, ok := office.RangeBounds(s.Anchor.Range)
			if !ok || s.Anchor.Target == "" {
				return ErrInvalid
			}
		}
		return nil
	}
	if kind == "region" {
		for i, a := range snapshots {
			for _, b := range snapshots[:i] {
				if a.Anchor.Page == b.Anchor.Page && a.Anchor.X < b.Anchor.X+b.Anchor.Width && b.Anchor.X < a.Anchor.X+a.Anchor.Width && a.Anchor.Y < b.Anchor.Y+b.Anchor.Height && b.Anchor.Y < a.Anchor.Y+a.Anchor.Height {
					return ErrOverlap
				}
			}
		}
		return nil
	}
	if len(snapshots) > 0 && snapshots[0].Anchor.Kind == "office" {
		for i, s := range snapshots {
			if s.Anchor.Kind != "office" || s.Anchor.Target == "" {
				return ErrInvalid
			}
			for _, before := range snapshots[:i] {
				a, b := s.Anchor, before.Anchor
				if a.Target == b.Target && ((a.Start == 0 && a.End == 0) || (b.Start == 0 && b.End == 0) || a.Start < b.End && b.Start < a.End) {
					return ErrOverlap
				}
			}
		}
		return nil
	}
	anchors := make([]Anchor, len(snapshots))
	for i, s := range snapshots {
		anchors[i] = s.Anchor
	}
	sort.Slice(anchors, func(i, j int) bool { return anchors[i].Start < anchors[j].Start })
	for i := 1; i < len(anchors); i++ {
		if anchors[i].Start < anchors[i-1].End || anchors[i].Start == anchors[i-1].Start {
			return ErrOverlap
		}
	}
	return nil
}

type Edit struct {
	ThreadID    string `json:"thread_id"`
	Expected    string `json:"expected"`
	Replacement string `json:"replacement"`
}
type Snapshot struct {
	Discussion *conversations.CommentReference `json:"discussion,omitempty"`
	ThreadID   string                          `json:"thread_id"`
	Anchor     Anchor                          `json:"anchor"`
	Body       string                          `json:"body"`
}
type Submission struct {
	SHA256  string `json:"sha256"`
	Edits   []Edit `json:"edits"`
	Summary string `json:"summary"`
}

func Digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func ValidText(text string) bool {
	return len(text) <= MaxFileBytes && utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}
func ValidateAnchor(text, mime string, a Anchor) error {
	if a.Kind == "pdf_text" {
		if mime != "application/pdf" || !ValidSource(text, mime) {
			return ErrInvalid
		}
		return validatePDFText(a)
	}
	if len(a.Quads) > 0 || len(a.ViewBox) > 0 {
		return ErrInvalid
	}
	if a.Kind == "office_range" {
		if mime != office.XLSX || a.Start != 0 || a.End != 0 || a.Row != 0 || a.Column != 0 || a.Page != 0 || a.X != 0 || a.Y != 0 || a.Width != 0 || a.Height != 0 {
			return ErrInvalid
		}
		p, err := office.Read([]byte(text), mime)
		if err != nil {
			return ErrInvalid
		}
		quote, err := p.RangeQuote(a.Target, a.Range)
		if err != nil || quote != a.Quote {
			return ErrInvalid
		}
		return nil
	}
	if a.Range != "" {
		return ErrInvalid
	}
	if a.Kind == "office" {
		p, err := office.Read([]byte(text), mime)
		if err != nil {
			return ErrInvalid
		}
		return validateOfficeAnchor(p, a)
	}
	if a.Target != "" {
		return ErrInvalid
	}
	if a.Kind == "region" {
		if !ValidSource(text, mime) || (!VisualMIME(mime) && !office.MIME(mime)) || a.Start != 0 || a.End != 0 || a.Quote != "" || a.Row != 0 || a.Column != 0 || a.Page < 1 || a.Page > 1000 || (!office.MIME(mime) && mime != "application/pdf" && a.Page != 1) {
			return ErrInvalid
		}
		for _, v := range []float64{a.X, a.Y, a.Width, a.Height} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return ErrInvalid
			}
		}
		if a.X < 0 || a.Y < 0 || a.Width <= 0 || a.Height <= 0 || a.X+a.Width > 1.000000001 || a.Y+a.Height > 1.000000001 {
			return ErrInvalid
		}
		return nil
	}
	if !TextMIME(mime) {
		return ErrInvalid
	}
	if !ValidText(text) || a.Start < 0 || a.End < a.Start || a.End > len(text) || len(a.Quote) > MaxQuoteBytes || !utf8.ValidString(text[:a.Start]) || !utf8.ValidString(text[:a.End]) || text[a.Start:a.End] != a.Quote {
		return ErrInvalid
	}
	switch a.Kind {
	case "text":
		if a.Start == a.End || mime == "text/csv" {
			return ErrInvalid
		}
	case "cell":
		if mime != "text/csv" {
			return ErrInvalid
		}
		fields, err := CSVFields(text)
		if err != nil {
			return err
		}
		if a.Row < 0 || a.Row >= len(fields) || a.Column < 0 || a.Column >= len(fields[a.Row]) {
			return ErrInvalid
		}
		want := fields[a.Row][a.Column]
		if want.Start != a.Start || want.End != a.End {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// CSVFields uses encoding/csv for validation and physical positions. Empty lines
// are skipped, as in encoding/csv. Quoted multiline fields retain raw byte spans.
func CSVFields(text string) ([][]Anchor, error) {
	offset := 0
	if strings.HasPrefix(text, "\ufeff") {
		text = strings.TrimPrefix(text, "\ufeff")
		offset = 3
	}
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	lines := []int{0}
	for i := range text {
		if text[i] == '\n' {
			lines = append(lines, i+1)
		}
	}
	var rows [][]Anchor
	count := 0
	for {
		values, err := r.Read()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, ErrInvalid
		}
		count += len(values)
		if count > 100000 {
			return nil, ErrInvalid
		}
		fields := make([]Anchor, len(values))
		for c := range values {
			line, col := r.FieldPos(c)
			start := lines[line-1] + col - 1
			end := start
			if start < len(text) && text[start] == '"' {
				end++
				for end < len(text) {
					if text[end] == '"' {
						end++
						if end < len(text) && text[end] == '"' {
							end++
							continue
						}
						break
					}
					end++
				}
			} else {
				for end < len(text) && text[end] != ',' && text[end] != '\n' {
					end++
				}
			}
			if end > start && text[end-1] == '\r' && (end == len(text) || text[end] == '\n') {
				end--
			}
			fields[c] = Anchor{Kind: "cell", Start: start + offset, End: end + offset, Quote: text[start:end], Row: len(rows), Column: c}
		}
		rows = append(rows, fields)
	}
}

// Apply replaces only explicitly selected spans. It rejects duplicate, overlapping,
// stale or unselected edits. A CSV replacement is a decoded cell value.
func Apply(text, mime string, snapshots []Snapshot, sub Submission) (string, error) {
	if office.MIME(mime) {
		return applyOffice(text, mime, snapshots, sub)
	}
	if !TextMIME(mime) || !ValidText(text) || Digest(text) != sub.SHA256 || len(sub.Edits) == 0 || len(sub.Edits) > 20 || (len(sub.Summary) > 8192 || strings.ContainsRune(sub.Summary, 0)) {
		return "", ErrInvalid
	}
	allowed := map[string]Anchor{}
	for _, s := range snapshots {
		allowed[s.ThreadID] = s.Anchor
	}
	type replacement struct {
		anchor Anchor
		value  string
	}
	patches := make([]replacement, 0, len(sub.Edits))
	seen := map[string]bool{}
	for _, e := range sub.Edits {
		a, ok := allowed[e.ThreadID]
		if !ok || seen[e.ThreadID] || e.Expected != a.Quote || !utf8.ValidString(e.Replacement) || strings.ContainsRune(e.Replacement, 0) {
			return "", ErrInvalid
		}
		if err := ValidateAnchor(text, mime, a); err != nil {
			return "", err
		}
		seen[e.ThreadID] = true
		value := e.Replacement
		if a.Kind == "cell" {
			value = "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\""
		}
		if value == a.Quote {
			return "", ErrInvalid
		}
		patches = append(patches, replacement{a, value})
	}
	sort.Slice(patches, func(i, j int) bool { return patches[i].anchor.Start < patches[j].anchor.Start })
	var out strings.Builder
	pos := 0
	lastStart := -1
	for _, p := range patches {
		if p.anchor.Start < pos || p.anchor.Start == lastStart {
			return "", ErrInvalid
		}
		lastStart = p.anchor.Start
		out.WriteString(text[pos:p.anchor.Start])
		out.WriteString(p.value)
		pos = p.anchor.End
		if out.Len() > MaxFileBytes {
			return "", ErrInvalid
		}
	}
	out.WriteString(text[pos:])
	if !ValidText(out.String()) || out.String() == text {
		return "", ErrInvalid
	}
	return out.String(), nil
}
