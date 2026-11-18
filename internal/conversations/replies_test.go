package conversations

import (
	"strings"
	"testing"
)

func TestReplyPromptPreservesBodyAndMarksBoundedQuote(t *testing.T) {
	if got := ReplyPrompt("original", nil); got != "original" {
		t.Fatal(got)
	}
	quote := &MessageReply{AuthorKind: "AGENT", Seq: 3, Body: "line1\nline2", Truncated: true}
	got := ReplyPrompt("user reply", quote)
	if !strings.Contains(got, "> line1\n> line2\n> … [quoted text truncated]") || !strings.HasSuffix(got, "Reply:\nuser reply") || quote.Body != "line1\nline2" {
		t.Fatal(got)
	}
}
