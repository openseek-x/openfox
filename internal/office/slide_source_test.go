package office

import (
	"strings"
	"testing"
)

func TestSlideSourceOffsets(t *testing.T) {
	raw := rewrite(t, fixture(t, "pptx"), func(name string, data []byte) (string, []byte) {
		if name == "ppt/slides/slide1.xml" {
			data = []byte(strings.Replace(string(data), "<a:t>Original text</a:t></a:r></a:p>", "<a:t>中🙂文</a:t></a:r><a:r><a:t>同段</a:t></a:r></a:p><a:p><a:r><a:t>第二段</a:t></a:r></a:p>", 1))
		}
		return name, data
	})
	p, err := Read(raw, PPTX)
	if err != nil {
		t.Fatal(err)
	}
	for i, offset := range []int{0, 4, 7} {
		u := p.View().Units[i]
		if u.SlideSource == nil || u.SlideSource.Part != "ppt/slides/slide1.xml" || u.SlideSource.Shape != 2 || u.SlideSource.Offset != offset {
			t.Fatalf("bad source: %+v", u.SlideSource)
		}
		trusted, _ := p.Unit(u.ID)
		if trusted.SlideSource == nil || *trusted.SlideSource != *u.SlideSource {
			t.Fatal("projection mismatch")
		}
	}
	for _, atom := range []string{"<a:br/>", "<a:fld id=\"f\" type=\"slidenum\"/>"} {
		changed := rewrite(t, raw, func(name string, data []byte) (string, []byte) {
			if name == "ppt/slides/slide1.xml" {
				data = []byte(strings.Replace(string(data), "<a:p>", "<a:p>"+atom, 1))
			}
			return name, data
		})
		q, err := Read(changed, PPTX)
		if err != nil {
			t.Fatal(err)
		}
		if q.View().Units[0].SlideSource != nil {
			t.Fatal("unsupported story got guessed offset")
		}
	}
	duplicate := rewrite(t, raw, func(name string, data []byte) (string, []byte) {
		if name == "ppt/slides/slide1.xml" {
			data = []byte(strings.Replace(string(data), `id="1"`, `id="2"`, 1))
		}
		return name, data
	})
	q, err := Read(duplicate, PPTX)
	if err != nil {
		t.Fatal(err)
	}
	if q.View().Units[0].SlideSource != nil {
		t.Fatal("ambiguous shape ID got a source position")
	}
}
