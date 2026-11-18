package office

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, ext string) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/sample." + ext)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func rewrite(t *testing.T, b []byte, fn func(string, []byte) (string, []byte)) []byte {
	t.Helper()
	z, e := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, f := range z.File {
		r, _ := f.Open()
		v, _ := io.ReadAll(r)
		r.Close()
		name, data := fn(f.Name, v)
		wr, e := w.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		_, _ = wr.Write(data)
	}
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	return out.Bytes()
}
func TestOfficeRoundTrip(t *testing.T) {
	for _, ext := range []string{"docx", "xlsx", "pptx"} {
		t.Run(ext, func(t *testing.T) {
			raw := fixture(t, ext)
			p, e := Read(raw, MIMEForName("f."+ext))
			if e != nil {
				t.Fatal(e)
			}
			if len(p.View().Units) < 2 {
				t.Fatal(p.View())
			}
			var selected Unit
			for _, u := range p.View().Units {
				if u.Text == "Original text" {
					selected = u
					break
				}
			}
			if !selected.Editable {
				t.Fatal(selected)
			}
			output, e := p.Replace([]Change{{selected.ID, selected.Text, "Updated & <text>"}})
			if e != nil {
				t.Fatal(e)
			}
			q, e := Read(output, p.mime)
			if e != nil {
				t.Fatal(e)
			}
			first := true
			for i, u := range p.View().Units {
				want := u.Text
				if u.ID == selected.ID {
					want = "Updated & <text>"
					first = false
				}
				if got := q.View().Units[i].Text; got != want {
					t.Fatalf("unit %s: %q != %q", u.ID, got, want)
				}
			}
			if first {
				t.Fatal("no replacement")
			}
			// No roundtrip serialization of untouched package parts (styles, header,
			// images, presentation geometry, metadata and relationships).
			targetPart := p.targets[selected.ID].part
			for name, data := range p.parts {
				if name == targetPart || (ext == "xlsx" && name == p.main) {
					continue
				}
				if !bytes.Equal(data, q.parts[name]) {
					t.Fatalf("unselected part changed: %s", name)
				}
			}
			if dir := os.Getenv("OPENFOX_OFFICE_EXPORT_DIR"); dir != "" {
				if e = os.MkdirAll(dir, 0o755); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(dir, "sample."+ext), output, 0o600); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestOfficeValidationAndSelection(t *testing.T) {
	raw := fixture(t, "docx")
	p, e := Read(raw, DOCX)
	if e != nil {
		t.Fatal(e)
	}
	u := p.View().Units[0]
	for name, changes := range map[string][]Change{
		"missing": {{"unknown", u.Text, "x"}}, "stale": {{u.ID, "wrong", "x"}}, "noop": {{u.ID, u.Text, u.Text}},
		"duplicate": {{u.ID, u.Text, "x"}, {u.ID, u.Text, "y"}}, "invalid XML": {{u.ID, u.Text, "\x01"}},
		"line break": {{u.ID, u.Text, "line\nline"}}, "oversize": {{u.ID, u.Text, strings.Repeat("x", 8193)}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := p.Replace(changes); e == nil {
				t.Fatal("accepted invalid change")
			}
		})
	}
	for name, b := range map[string][]byte{
		"not zip": []byte("plain"), "too big": make([]byte, MaxBytes+1),
		"traversal": rewrite(t, raw, func(n string, b []byte) (string, []byte) {
			if n == p.main {
				n = "../escape"
			}
			return n, b
		}),
		"duplicate": rewrite(t, raw, func(n string, b []byte) (string, []byte) {
			if n == p.main {
				n = "[Content_Types].xml"
			}
			return n, b
		}),
		"doctype": rewrite(t, raw, func(n string, b []byte) (string, []byte) {
			if n == p.main {
				b = append([]byte("<!DOCTYPE x>"), b...)
			}
			return n, b
		}),
		"macro": rewrite(t, raw, func(n string, b []byte) (string, []byte) {
			if n == p.main {
				n = "word/vbaProject.bin"
			}
			return n, b
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := Read(b, DOCX); e == nil {
				t.Fatal("accepted invalid package")
			}
		})
	}
	if _, e := Read(raw, PPTX); e == nil {
		t.Fatal("MIME mismatch")
	}
	bomb := rewrite(t, raw, func(n string, b []byte) (string, []byte) {
		if n == p.main {
			b = []byte(strings.Repeat("x", maxPart+1))
		}
		return n, b
	})
	if _, e := Read(bomb, DOCX); e == nil {
		t.Fatal("zip bomb accepted")
	}
}
func TestSpreadsheetTypedValuesAndCaches(t *testing.T) {
	raw := fixture(t, "xlsx")
	raw = rewrite(t, raw, func(n string, b []byte) (string, []byte) {
		if strings.Contains(n, "worksheets/sheet1.xml") {
			b = bytes.Replace(b, []byte("<f>B1*2</f><v></v>"), []byte("<f>B1*2</f><v>25</v>"), 1)
		}
		return n, b
	})
	p, e := Read(raw, XLSX)
	if e != nil {
		t.Fatal(e)
	}
	var n, b, f Unit
	for _, u := range p.View().Units {
		switch u.Type {
		case "number":
			n = u
		case "boolean":
			b = u
		case "formula":
			f = u
		}
	}
	if f.Editable || f.Text != "=B1*2" {
		t.Fatal(f)
	}
	for _, c := range []Change{{n.ID, n.Text, "NaN"}, {n.ID, n.Text, "=1+1"}, {b.ID, b.Text, "true"}, {f.ID, f.Text, "10"}} {
		if _, e := p.Replace([]Change{c}); e == nil {
			t.Fatal("accepted invalid typed edit", c)
		}
	}
	out, e := p.Replace([]Change{{n.ID, n.Text, "3.25"}, {b.ID, b.Text, "0"}})
	if e != nil {
		t.Fatal(e)
	}
	q, e := Read(out, XLSX)
	if e != nil {
		t.Fatal(e)
	}
	for _, part := range q.sheets {
		root, _ := q.xml(part)
		for _, c := range root.all(sheetNS, "c") {
			if c.child(sheetNS, "f") != nil && c.child(sheetNS, "v") != nil {
				t.Fatal("stale formula cache retained")
			}
		}
	}
	text := q.View().Units[0]
	out, e = q.Replace([]Change{{text.ID, text.Text, "=SUM(A1:A2)"}})
	if e != nil {
		t.Fatal(e)
	}
	q, e = Read(out, XLSX)
	if e != nil {
		t.Fatal(e)
	}
	if q.View().Units[0].Type != "text" {
		t.Fatal("text became formula")
	}
}
func TestProtectionAndMixedStyles(t *testing.T) {
	raw := fixture(t, "docx")
	p, e := Read(raw, DOCX)
	if e != nil {
		t.Fatal(e)
	}
	if p.View().Units[1].Text != " keep italic" {
		t.Fatal(p.View())
	}
	raw = rewrite(t, raw, func(n string, b []byte) (string, []byte) {
		if n == "word/settings.xml" {
			b = bytes.Replace(b, []byte("</w:settings>"), []byte(`<w:documentProtection w:enforcement="1"/></w:settings>`), 1)
		}
		return n, b
	})
	p, e = Read(raw, DOCX)
	if e != nil {
		t.Fatal(e)
	}
	for _, u := range p.View().Units {
		if u.Editable {
			t.Fatal("protected text editable")
		}
	}
}

func TestSharedStringsStayLocalAndRichTextReadOnly(t *testing.T) {
	raw := fixture(t, "xlsx")
	raw = rewrite(t, raw, func(n string, b []byte) (string, []byte) {
		if n == "xl/_rels/workbook.xml.rels" {
			b = bytes.Replace(b, []byte("</Relationships>"), []byte(`<Relationship Type="`+relNS+`/sharedStrings" Target="sharedStrings.xml" Id="shared"/></Relationships>`), 1)
		}
		if n == "xl/worksheets/sheet1.xml" {
			b = bytes.ReplaceAll(b, []byte(`t="inlineStr"><is><t>Original text</t></is>`), []byte(`t="s"><v>0</v>`))
		}
		return n, b
	})
	// Add a shared string part to the otherwise independently generated fixture.
	z, _ := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range z.File {
		if e := w.Copy(f); e != nil {
			t.Fatal(e)
		}
	}
	wr, _ := w.Create("xl/sharedStrings.xml")
	_, _ = wr.Write([]byte(`<sst xmlns="` + sheetNS + `" count="2" uniqueCount="1"><si><t>Shared_x000D_text</t></si></sst>`))
	_ = w.Close()
	raw = buf.Bytes()
	p, e := Read(raw, XLSX)
	if e != nil {
		t.Fatal(e)
	}
	u := p.View().Units[0]
	if u.Text != "Shared\rtext" || !u.Editable {
		t.Fatal(u)
	}
	out, e := p.Replace([]Change{{u.ID, u.Text, "Literal _x000D_ & 🙂\rline"}})
	if e != nil {
		t.Fatal(e)
	}
	q, e := Read(out, XLSX)
	if e != nil {
		t.Fatal(e)
	}
	if q.View().Units[0].Text != "Literal _x000D_ & 🙂\rline" {
		t.Fatal(q.View().Units[0])
	}
	if !bytes.Equal(p.parts["xl/sharedStrings.xml"], q.parts["xl/sharedStrings.xml"]) {
		t.Fatal("global shared string changed")
	}
	for _, u := range q.View().Units {
		if u.Label == "A2" && u.Text != "Shared\rtext" {
			t.Fatal("another shared reference changed")
		}
	}
	rich := rewrite(t, raw, func(n string, b []byte) (string, []byte) {
		if n == "xl/sharedStrings.xml" {
			b = bytes.Replace(b, []byte("<t>Shared_x000D_text</t>"), []byte("<r><rPr><b/></rPr><t>Rich</t></r>"), 1)
		}
		return n, b
	})
	p, e = Read(rich, XLSX)
	if e != nil {
		t.Fatal(e)
	}
	if p.View().Units[0].Editable {
		t.Fatal("rich shared string writable")
	}
}
func TestWordCrossParagraphFieldIsReadOnly(t *testing.T) {
	raw := rewrite(t, fixture(t, "docx"), func(n string, b []byte) (string, []byte) {
		if n == "word/document.xml" {
			b = bytes.Replace(b, []byte("<w:body>"), []byte(`<w:body><w:p><w:r><w:fldChar w:fldCharType="begin"/></w:r></w:p>`), 1)
			b = bytes.Replace(b, []byte("</w:body>"), []byte(`<w:p><w:r><w:fldChar w:fldCharType="end"/></w:r></w:p></w:body>`), 1)
		}
		return n, b
	})
	p, e := Read(raw, DOCX)
	if e != nil {
		t.Fatal(e)
	}
	for _, u := range p.View().Units {
		if u.Editable {
			t.Fatal("cross-paragraph field result writable")
		}
	}
}
