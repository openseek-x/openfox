package runtime

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestToolResultImageBoundsAndTextTruncation(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	valid := ToolResult{Text: "screenshot", OK: true, Image: &ToolImage{MIMEType: "image/png", Data: encoded.Bytes()}}
	if got := valid.Bounded(); !got.OK || got.Image == nil {
		t.Fatal("valid image rejected")
	}
	for _, payload := range []*ToolImage{
		{MIMEType: "image/jpeg", Data: encoded.Bytes()},
		{MIMEType: "image/svg+xml", Data: []byte("<svg/>")},
		{MIMEType: "image/png", Data: make([]byte, MaxToolImageBytes+1)},
		{MIMEType: "image/png", Data: []byte("invalid")},
	} {
		got := (ToolResult{OK: true, Image: payload}).Bounded()
		if got.OK || got.Image != nil {
			t.Fatal("invalid image escaped validation")
		}
	}
	encoded.Reset()
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, MaxToolImageEdge+1, 1))); err != nil {
		t.Fatal(err)
	}
	valid.Image.Data = encoded.Bytes()
	if valid.Bounded().OK {
		t.Fatal("oversized dimensions accepted")
	}
	text := TextResult(strings.Repeat("中文", MaxToolTextBytes), true).Bounded()
	if !text.OK || len(text.Text) > MaxToolTextBytes || !utf8.ValidString(text.Text) || !strings.HasSuffix(text.Text, "[truncated]") {
		t.Fatal("invalid truncation")
	}
}
