package runtime

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTextBufferBoundsUTF8WithoutStopping(t *testing.T) {
	b := TextBuffer{Limit: 128}
	b.WriteString(strings.Repeat("中🙂", 18))
	b.WriteString("end does not fit")
	b.WriteString(strings.Repeat("ignored", 10000))
	got := b.String()
	if len(got) > 128 || !utf8.ValidString(got) || !strings.HasSuffix(got, OutputTruncationNotice) || strings.Contains(got, "ignored") {
		t.Fatalf("invalid bounded output: %q", got)
	}
	b.Reset()
	b.WriteString("ok")
	if b.String() != "ok" {
		t.Fatal("reset retained truncation state")
	}
}

func TestOutputPreviewBoundaries(t *testing.T) {
	for _, limit := range []int{0, 1, 7, 63, 64, 128, 512} {
		text := strings.Repeat("文件🙂", 32)
		got := OutputPreview(text, limit)
		if len(got) > limit || !utf8.ValidString(got) {
			t.Fatalf("invalid preview at %d", limit)
		}
	}
	if OutputPreview("exact", 5) != "exact" {
		t.Fatal("exact boundary was clipped")
	}
}
