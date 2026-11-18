package office

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func complexSheet(t *testing.T) []byte {
	t.Helper()
	raw, e := os.ReadFile("../../tests/frontend/e2e/fixtures/office-preview/complex.xlsx")
	if e != nil {
		t.Fatal(e)
	}
	return raw
}
func TestSpreadsheetRenderCopyPreservesSource(t *testing.T) {
	raw := complexSheet(t)
	before := append([]byte(nil), raw...)
	p, e := Read(raw, XLSX)
	if e != nil {
		t.Fatal(e)
	}
	units := append([]Unit(nil), p.View().Units...)
	rendered, e := p.SpreadsheetPreview()
	if e != nil {
		t.Fatal(e)
	}
	again, e := p.SpreadsheetPreview()
	if e != nil || !bytes.Equal(again, rendered) {
		t.Fatal("render copy is not deterministic", e)
	}
	if !bytes.Equal(raw, before) || !reflect.DeepEqual(units, p.View().Units) {
		t.Fatal("source changed")
	}
	meta := p.View().Spreadsheet
	if meta.SHA256 != fmt.Sprintf("%x", sha256.Sum256(rendered)) || len(meta.Sheets) != 2 || meta.Sheets[0].FrozenRows != 2 || meta.Sheets[0].FrozenColumns != 1 {
		t.Fatalf("bad metadata: %+v", meta)
	}
	if len(meta.Sheets[0].Drawings) != 2 || !bytes.Equal(meta.Sheets[0].Drawings[1].Data, p.parts["xl/media/image1.png"]) {
		t.Fatal("missing picture")
	}
	q, e := Read(rendered, XLSX)
	if e != nil {
		t.Fatal(e)
	}
	if len(units) != len(q.View().Units) {
		t.Fatal("render copy changed cell count")
	}
	for i, actual := range q.View().Units {
		// Removing view XML changes parser node ordinals in the disposable copy.
		// Authoritative IDs stay in p; renderer mappings use part + cell address.
		actual.ID = units[i].ID
		if !reflect.DeepEqual(units[i], actual) {
			t.Fatal("render copy changed cell content, address or permissions")
		}
	}
	for name, body := range p.parts {
		if name == "xl/charts/chart1.xml" || name == "xl/worksheets/sheet1.xml" {
			continue
		}
		if !bytes.Equal(q.parts[name], body) {
			t.Fatalf("unrelated part changed: %s", name)
		}
	}
	sheet, _ := q.xml("xl/worksheets/sheet1.xml")
	if len(sheet.all(sheetNS, "pane")) != 0 {
		t.Fatal("renderer should receive an unfrozen full sheet")
	}
	chart, _ := q.xml("xl/charts/chart1.xml")
	if len(chart.all(chartNS, "numCache")) != 2 || len(chart.all(chartNS, "strCache")) != 4 {
		t.Fatal("missing numeric/category/name caches")
	}
	if !strings.Contains(string(q.parts["xl/charts/chart1.xml"]), "第一阶段") {
		t.Fatal("categories replaced by indices")
	}
	original, _ := p.xml("xl/worksheets/sheet1.xml")
	if len(original.all(sheetNS, "pane")) != 1 {
		t.Fatal("original pane removed")
	}
	quote, e := p.RangeQuote("xl/worksheets/sheet1.xml", "B3:D4")
	if e != nil || quote != "10\t8\t=C3/B3\n20\t12\t=C4/B4" {
		t.Fatalf("wrong range: %q %v", quote, e)
	}
}
func TestSpreadsheetRenderRefusesUnresolvableDrawing(t *testing.T) {
	for _, tc := range []struct{ name, part, from, to string }{
		{"external cache", "xl/charts/chart1.xml", "'项目进展'!$B$3:$B$5", "'[other.xlsx]项目进展'!$B$3:$B$5"},
		{"missing formula cache", "xl/charts/chart1.xml", "'项目进展'!$B$3:$B$5", "'项目进展'!$D$3:$D$5"},
		{"invalid picture", "xl/media/image1.png", "", ""},
		{"picture mask", "xl/drawings/drawing1.xml", `prst="rect"`, `prst="ellipse"`},
		{"picture flip", "xl/drawings/drawing1.xml", `<spPr>`, `<spPr><a:xfrm xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" flipH="true"/>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := rewrite(t, complexSheet(t), func(name string, b []byte) (string, []byte) {
				if name == tc.part {
					if tc.name == "invalid picture" {
						b = []byte("bad image")
					} else {
						b = []byte(strings.ReplaceAll(string(b), tc.from, tc.to))
					}
				}
				return name, b
			})
			p, e := Read(raw, XLSX)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = p.SpreadsheetPreview(); e == nil || p.View().Spreadsheet != nil {
				t.Fatal("unsupported drawing must select PDF")
			}
		})
	}
}
func TestSpreadsheetRangeBounds(t *testing.T) {
	p, e := Read(complexSheet(t), XLSX)
	if e != nil {
		t.Fatal(e)
	}
	for _, r := range []string{"A0", "a1", "B2:A1", "XFE1", "A1048577", "A1:A257", "A1:B2:C3"} {
		if _, e = p.RangeQuote("xl/worksheets/sheet1.xml", r); e == nil {
			t.Fatalf("accepted %q", r)
		}
	}
	if _, e = p.RangeQuote("../other.xml", "A1"); e == nil {
		t.Fatal("unknown sheet accepted")
	}
}
