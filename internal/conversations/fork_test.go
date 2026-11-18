package conversations

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func forkFixture(n int) []forkMessage {
	items := make([]forkMessage, n)
	for i := range items {
		items[i] = forkMessage{ID: strings.Repeat("a", 36) + string(rune(i+1)), Seq: int64(i + 1), AuthorKind: "USER", Kind: "TEXT", Body: "中🦊 <quoted>\n", CreatedAt: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)}
	}
	return items
}
func TestForkSnapshotBudgetsAndReplies(t *testing.T) {
	if _, err := prepareForkMessages(forkFixture(500)); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareForkMessages(forkFixture(501)); !errors.Is(err, ErrForkContextTooLarge) {
		t.Fatal(err)
	}
	for _, over := range []int{0, 1} {
		items := forkFixture(4)
		encoded, err := prepareForkMessages(append([]forkMessage(nil), items...))
		if err != nil {
			t.Fatal(err)
		}
		remaining := MaxForkContentBytes - len(encoded) + over
		for i := range items {
			n := remaining / (len(items) - i)
			items[i].Body += strings.Repeat("x", n)
			remaining -= n
		}
		encoded, err = prepareForkMessages(items)
		if over == 0 && (err != nil || len(encoded) != MaxForkContentBytes) {
			t.Fatalf("boundary %d %v", len(encoded), err)
		}
		if over == 1 && !errors.Is(err, ErrForkContextTooLarge) {
			t.Fatalf("over=%v", err)
		}
	}
	items := forkFixture(3)
	original := items[0].ID
	items[1].ReplyToID = original
	items[2].ReplyToID = "future"
	items[2].Reply = &MessageReply{MessageID: "future", Body: "FUTURE SECRET"}
	encoded, err := prepareForkMessages(items)
	if err != nil {
		t.Fatal(err)
	}
	var result []forkMessage
	if err = json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if result[0].ID == original || result[1].ReplyToID != result[0].ID || result[1].Reply == nil || !result[1].Reply.Snapshot || result[1].Reply.MessageID != result[0].ID || result[1].Reply.Body != items[0].Body || result[2].Reply != nil || strings.Contains(string(encoded), "FUTURE") {
		t.Fatalf("bad mapping %s", encoded)
	}
}
