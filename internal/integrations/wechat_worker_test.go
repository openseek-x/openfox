package integrations

import (
	"strings"
	"testing"
)

func TestSplitParts(t *testing.T) {
	if got := splitParts("  短文本  ", 10); len(got) != 1 || got[0] != "短文本" {
		t.Fatalf("short = %q", got)
	}
	text := strings.Repeat("长", 4000)
	parts := splitParts(text, wechatPartRunes)
	total := 0
	for _, p := range parts {
		if n := len([]rune(p)); n > wechatPartRunes {
			t.Fatalf("part too long: %d", n)
		} else {
			total += n
		}
	}
	if len(parts) != 3 || total != 4000 {
		t.Fatalf("parts = %d total = %d", len(parts), total)
	}
	// 优先在换行处断开。
	nl := strings.Repeat("a", 60) + "\n" + strings.Repeat("b", 60)
	if got := splitParts(nl, 100); len(got) != 2 || got[0] != strings.Repeat("a", 60) {
		t.Fatalf("newline split = %q", got)
	}
}

func TestMaskWeChatID(t *testing.T) {
	for in, want := range map[string]string{"wxid_abcdef@im.wechat": "wx***chat", "short": "s***", "": "***"} {
		if got := maskWeChatID(in); got != want {
			t.Errorf("maskWeChatID(%q) = %q, want %q", in, got, want)
		}
	}
}
