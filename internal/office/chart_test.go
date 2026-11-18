package office

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestChartWorkbookIsBoundedAndPreserved(t *testing.T) {
	raw, err := os.ReadFile("../../tests/frontend/e2e/fixtures/office-preview/styled.pptx")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Read(raw, PPTX)
	if err != nil {
		t.Fatal(err)
	}
	var embedded string
	for name := range p.parts {
		if strings.Contains(name, "/embeddings/") {
			embedded = name
		}
	}
	if embedded == "" {
		t.Fatal("fixture has no embedded chart workbook")
	}
	var target Unit
	for _, u := range p.View().Units {
		if u.Text == "Original text" {
			target = u
		}
	}
	out, err := p.Replace([]Change{{ID: target.ID, Expected: target.Text, Replacement: "Updated text"}})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := Read(out, PPTX)
	if err != nil || !bytes.Equal(p.parts[embedded], updated.parts[embedded]) {
		t.Fatal("chart workbook changed", err)
	}
	for _, bad := range []struct {
		name      string
		transform func(string, []byte) (string, []byte)
	}{
		{"unreferenced", func(n string, b []byte) (string, []byte) {
			if strings.Contains(n, "charts/_rels/") {
				b = bytes.ReplaceAll(b, []byte(relNS+"/package"), []byte(relNS+"/oleObject"))
			}
			return n, b
		}},
		{"invalid workbook", func(n string, b []byte) (string, []byte) {
			if n == embedded {
				b = []byte("not an xlsx")
			}
			return n, b
		}},
		{"macro workbook", func(n string, b []byte) (string, []byte) {
			if n == embedded {
				b = rewrite(t, b, func(inner string, data []byte) (string, []byte) {
					if inner == "[Content_Types].xml" {
						data = bytes.ReplaceAll(data, []byte("spreadsheetml.sheet"), []byte("macroEnabled"))
					}
					return inner, data
				})
			}
			return n, b
		}},
		{"nested workbook", func(n string, b []byte) (string, []byte) {
			if n == embedded {
				b = rewrite(t, b, func(inner string, data []byte) (string, []byte) {
					if inner == "[Content_Types].xml" {
						inner = "xl/embeddings/nested.xlsx"
					}
					return inner, data
				})
			}
			return n, b
		}},
	} {
		t.Run(bad.name, func(t *testing.T) {
			if _, err := Read(rewrite(t, raw, bad.transform), PPTX); err == nil {
				t.Fatal("unsafe embedding accepted")
			}
		})
	}
	// 外层和图表工作簿共用预算，不能通过嵌套 ZIP 重置限额。
	if _, err := readPackage(raw, PPTX, &readBudget{expanded: maxExpanded - 1}, true); err == nil {
		t.Fatal("expanded budget ignored")
	}
	if _, err := readPackage(raw, PPTX, &readBudget{entries: 2040}, true); err == nil {
		t.Fatal("entry budget ignored")
	}
}
