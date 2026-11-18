package integrations

import (
	"strings"
	"testing"
)

func TestWeComQuoteIsBoundedAndLabelled(t *testing.T) {
	quote := strings.Repeat("中", 2001)
	got := wecomQuotedText("创建任务", "text", quote)
	if strings.Count(got, "中") != 2000 || !strings.Contains(got, "已截取前2,000字") {
		t.Fatal("quote must visibly truncate by Unicode codepoint")
	}
	if got := wecomQuotedText("body", "image", "ignored"); got != "body" {
		t.Fatal("non-text quote included")
	}
	if validWeComChatID(" group") || validWeComChatID("g\n") || validWeComChatID(strings.Repeat("x", 129)) || !validWeComChatID("group-1") {
		t.Fatal("invalid group identifier accepted")
	}
}
