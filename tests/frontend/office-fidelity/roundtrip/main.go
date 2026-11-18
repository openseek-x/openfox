// Generates disposable candidate files through the production OOXML editor.
// The original fixtures are read-only; output is used by the built-app E2E.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"openfox/internal/office"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func parts(raw []byte) map[string][]byte {
	z, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	must(err)
	out := map[string][]byte{}
	for _, f := range z.File {
		r, err := f.Open()
		must(err)
		b, err := io.ReadAll(r)
		must(err)
		must(r.Close())
		out[f.Name] = b
	}
	return out
}
func main() {
	if len(os.Args) != 2 {
		panic("usage: go run ./tests/frontend/office-fidelity/roundtrip OUTPUT_DIRECTORY")
	}
	dir := os.Args[1]
	must(os.MkdirAll(dir, 0700))
	for _, sample := range []struct{ name, mime, before, after, part string }{
		{"complex.docx", office.DOCX, "中文报告 · 协作进展", "中文报告 · 已核验", "word/document.xml"},
		{"complex.xlsx", office.XLSX, "10", "42", "xl/worksheets/sheet1.xml"},
		{"styled.pptx", office.PPTX, "中文报告 · 产品进展", "中文报告 · 已核验", "ppt/slides/slide1.xml"},
	} {
		raw, err := os.ReadFile(filepath.Join("tests/frontend/e2e/fixtures/office-preview", sample.name))
		must(err)
		p, err := office.Read(raw, sample.mime)
		must(err)
		id := ""
		for _, u := range p.View().Units {
			if u.Editable && u.Text == sample.before && (sample.mime != office.XLSX || u.Label == "B3" && u.CellSource.Sheet == 0) {
				id = u.ID
				break
			}
		}
		if id == "" {
			panic("missing editable fixture unit")
		}
		updated, err := p.Replace([]office.Change{{ID: id, Expected: sample.before, Replacement: sample.after}})
		must(err)
		after, err := office.Read(updated, sample.mime)
		must(err)
		found := false
		for _, u := range after.View().Units {
			if u.ID == id && u.Text == sample.after {
				found = true
			}
		}
		if !found {
			panic("replacement did not roundtrip")
		}
		originalParts, candidateParts := parts(raw), parts(updated)
		if len(originalParts) != len(candidateParts) {
			panic("unexpected package part changes")
		}
		changed := []string{}
		for name, content := range originalParts {
			if bytes.Equal(content, candidateParts[name]) {
				continue
			}
			if name != sample.part && !(sample.mime == office.XLSX && (name == "xl/workbook.xml" || strings.HasPrefix(name, "xl/worksheets/"))) {
				panic("unrelated part changed: " + name)
			}
			changed = append(changed, name)
		}
		path := filepath.Join(dir, sample.name)
		must(os.WriteFile(path, updated, 0600))
		if sample.mime == office.XLSX {
			preview, err := after.SpreadsheetPreview()
			must(err)
			must(os.WriteFile(path+".render.xlsx", preview, 0600))
		}
		projection, err := json.Marshal(struct {
			office.View
			SHA256 string `json:"sha256"`
		}{after.View(), fmt.Sprintf("%x", sha256.Sum256(updated))})
		must(err)
		must(os.WriteFile(path+".json", projection, 0600))
		fmt.Printf("%s: %q -> %q, source %x, candidate %x, %d unchanged parts, modified %v\n", sample.name, sample.before, sample.after, sha256.Sum256(raw), sha256.Sum256(updated), len(originalParts)-len(changed), changed)
	}
}
