package office

import (
	"strings"
	"testing"
)

func TestWordSourceOffsetsAndUnsupportedAtoms(t *testing.T) {
	raw := rewrite(t, fixture(t, "docx"), func(name string, data []byte) (string, []byte) {
		if name == "word/document.xml" {
			data = []byte(strings.Replace(string(data), "Original text", "中🙂文", 1))
		}
		return name, data
	})
	p, err := Read(raw, DOCX)
	if err != nil {
		t.Fatal(err)
	}
	a, b := p.View().Units[0], p.View().Units[1]
	if a.Source == nil || b.Source == nil || a.Source.Part != "word/document.xml" || a.Source.Paragraph != b.Source.Paragraph || a.Source.Offset != 0 || b.Source.Offset != 4 {
		t.Fatalf("wrong UTF-16 source positions: %+v %+v", a.Source, b.Source)
	}
	trusted, _ := p.Unit(a.ID)
	if trusted.Source == nil || *trusted.Source != *a.Source {
		t.Fatal("target and projection disagree")
	}
	raw = rewrite(t, raw, func(name string, data []byte) (string, []byte) {
		if name == "word/document.xml" {
			data = []byte(strings.Replace(string(data), "<w:t>中🙂文</w:t>", "<w:tab/><w:t>中🙂文</w:t>", 1))
		}
		return name, data
	})
	p, err = Read(raw, DOCX)
	if err != nil {
		t.Fatal(err)
	}
	if p.View().Units[0].Source != nil || p.View().Units[1].Source != nil {
		t.Fatal("non-text atom got a guessed source offset")
	}
}
