package artifactreview

import (
	"archive/zip"
	"bytes"
	"io"
	"math"
	"openfox/internal/office"
	"os"
	"strings"
	"testing"
)

func TestResolveTextConservativeAndUnicode(t *testing.T) {
	for _, tc := range []struct {
		name, old, next, quote, status string
		start                          int
	}{
		{"unicode", "前🙂内容后", "新增\n前🙂内容后", "🙂内容", "exact", len("前")},
		{"context", "a repeat b repeat c", "new a repeat b repeat c", "repeat", "exact", 2},
		{"duplicated context", "a repeat b", "a repeat b a repeat b", "repeat", "ambiguous", 2},
		{"whitespace", "a word\nnext b", "a word next b", "word\nnext", "approximate", 2},
		{"lost", "target", "gone", "target", "orphaned", 0},
		{"no positional guess", "abc x abc", "abc abc", "abc", "ambiguous", 0},
		{"bounded matches", "x", "x" + strings.Repeat("x", 101), "x", "ambiguous", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := Anchor{Kind: "text", Start: tc.start, End: tc.start + len(tc.quote), Quote: tc.quote}
			original := a
			r, err := ResolveAnchor(tc.old, tc.next, "text/plain", a)
			if err != nil || r.Status != tc.status {
				t.Fatalf("%+v %v", r, err)
			}
			if a.Start != original.Start || a.Quote != original.Quote {
				t.Fatal("mutated original")
			}
			if r.Status == "exact" {
				if err := ValidateAnchor(tc.next, "text/plain", *r.Anchor); err != nil {
					t.Fatal(err)
				}
			} else if r.Anchor != nil {
				t.Fatal("uncertain location authorized edit")
			}
		})
	}
	if _, err := ResolveAnchor("🙂", "next", "text/plain", Anchor{Kind: "text", Start: 1, End: 4, Quote: "🙂"}); err == nil {
		t.Fatal("split UTF-8")
	}
}
func rewriteOffice(t *testing.T, raw []byte, part string, edit func(string) string) []byte {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, f := range r.File {
		in, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(in)
		in.Close()
		if e != nil {
			t.Fatal(e)
		}
		if f.Name == part {
			data = []byte(edit(string(data)))
		}
		out, e := w.Create(f.Name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = out.Write(data); e != nil {
			t.Fatal(e)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestResolveOfficeReparseAfterInsertAndReorder(t *testing.T) {
	for _, ext := range []string{"docx", "xlsx", "pptx"} {
		t.Run(ext, func(t *testing.T) {
			old, err := os.ReadFile("../office/testdata/sample." + ext)
			if err != nil {
				t.Fatal(err)
			}
			mime := office.MIMEForName("a." + ext)
			p, _ := office.Read(old, mime)
			unit := p.View().Units[0]
			if ext == "xlsx" {
				unit = p.View().Units[1]
			} // unique numeric cell
			a := Anchor{Kind: "office", Target: unit.ID, Quote: unit.Text}
			next := old
			switch ext {
			case "docx":
				next = rewriteOffice(t, old, "word/document.xml", func(s string) string {
					return strings.Replace(s, "<w:body>", "<w:body><w:p><w:r><w:t>Inserted</w:t></w:r></w:p>", 1)
				})
			case "pptx":
				next = rewriteOffice(t, old, "ppt/presentation.xml", func(s string) string {
					return strings.Replace(s, `<p:sldId id="256" r:id="rId7"/><p:sldId id="257" r:id="rId8"/>`, `<p:sldId id="257" r:id="rId8"/><p:sldId id="256" r:id="rId7"/>`, 1)
				})
			case "xlsx":
				next = rewriteOffice(t, old, "xl/worksheets/sheet1.xml", func(s string) string {
					for _, c := range []string{"A", "B", "C", "D"} {
						s = strings.ReplaceAll(s, `r="`+c+`2"`, `r="`+c+`3"`)
						s = strings.ReplaceAll(s, `r="`+c+`1"`, `r="`+c+`2"`)
					}
					s = strings.ReplaceAll(s, `<row r="2">`, `<row r="3">`)
					s = strings.ReplaceAll(s, `<row r="1">`, `<row r="2">`)
					return strings.Replace(s, "<sheetData>", `<sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>Inserted</t></is></c></row>`, 1)
				})
			}
			r, err := ResolveAnchor(string(old), string(next), mime, a)
			if err != nil || r.Status != "exact" {
				t.Fatalf("%+v %v", r, err)
			}
			if err = ValidateAnchor(string(next), mime, *r.Anchor); err != nil {
				t.Fatal(err)
			}
			q, _ := office.Read(next, mime)
			u, _ := q.Unit(r.Anchor.Target)
			if ext == "docx" && u.Source.Paragraph != unit.Source.Paragraph+1 {
				t.Fatal("stale paragraph")
			}
			if ext == "xlsx" && u.CellSource.Row != unit.CellSource.Row+1 {
				t.Fatal("stale cell")
			}
			if ext == "pptx" && (r.Anchor.Target == a.Target || *u.SlideSource != *unit.SlideSource) {
				t.Fatal("stale slide identity")
			}
			if ext == "xlsx" {
				rangeAnchor := Anchor{Kind: "office_range", Target: unit.CellSource.Part, Range: "A1:B1"}
				rangeAnchor.Quote, _ = p.RangeQuote(rangeAnchor.Target, rangeAnchor.Range)
				rr, e := ResolveAnchor(string(old), string(next), mime, rangeAnchor)
				if e != nil || rr.Status != "exact" || rr.Anchor.Range != "A2:B2" {
					t.Fatalf("range %+v %v", rr, e)
				}
				repeated := p.View().Units[0]
				rr, e = ResolveAnchor(string(old), string(next), mime, Anchor{Kind: "office", Target: repeated.ID, Quote: repeated.Text})
				if e != nil || rr.Status != "ambiguous" {
					t.Fatalf("repeated cell %+v %v", rr, e)
				}
			}
		})
	}
}
func TestPDFTextFeedbackGeometryAndChangedVisual(t *testing.T) {
	a := Anchor{Kind: "pdf_text", Quote: "two lines", Page: 1, ViewBox: []float64{0, 0, 300, 400}, Quads: [][]float64{{10, 390, 110, 390, 110, 370, 10, 370}, {10, 360, 90, 360, 90, 340, 10, 340}}}
	if err := ValidateAnchor("%PDF-1.7 sample", "application/pdf", a); err != nil {
		t.Fatal(err)
	}
	r, err := ResolveAnchor("%PDF-1.7 sample", "%PDF-1.7 changed", "application/pdf", a)
	if err != nil || r.Status != "orphaned" {
		t.Fatal(r, err)
	}
	for _, n := range []float64{math.NaN(), math.Inf(1), 3010} {
		bad := a
		bad.Quads = append([][]float64(nil), a.Quads...)
		bad.Quads[0] = append([]float64(nil), a.Quads[0]...)
		bad.Quads[0][0] = n
		if ValidateAnchor("x", "application/pdf", bad) == nil {
			t.Fatal("invalid geometry")
		}
	}
	if _, err := Apply("pdf", "application/pdf", []Snapshot{{ThreadID: "t", Anchor: a}}, Submission{SHA256: Digest("pdf"), Edits: []Edit{{ThreadID: "t", Expected: a.Quote, Replacement: "changed"}}}); err == nil {
		t.Fatal("PDF text gave edit authority")
	}
}
