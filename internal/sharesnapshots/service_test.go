package sharesnapshots

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"openfox/internal/conversations"
)

func TestNewTokenIs32RandomBytesAndHashMatches(t *testing.T) {
	a, hashA, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := NewToken()
	if a == b {
		t.Fatal("两次生成的令牌相同")
	}
	// 32 字节的 base64url（无填充）长度为 43
	if len(a) != 43 || len(hashA) != 32 {
		t.Fatalf("令牌长度 %d，哈希长度 %d", len(a), len(hashA))
	}
	if !bytes.Equal(hashA, HashToken(a)) || bytes.Equal(hashA, HashToken(b)) {
		t.Fatal("哈希与令牌不对应")
	}
}

func TestExpiryOptions(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	for option, days := range map[string]int{"": 30, Expiry30d: 30, Expiry7d: 7} {
		got, err := expiryFor(option, now)
		if err != nil || got == nil || !got.Equal(now.Add(time.Duration(days)*24*time.Hour)) {
			t.Fatalf("%q -> %v %v", option, got, err)
		}
	}
	if got, err := expiryFor(ExpiryNever, now); err != nil || got != nil {
		t.Fatalf("never -> %v %v", got, err)
	}
	if _, err := expiryFor("1y", now); err == nil {
		t.Fatal("未知有效期应被拒绝")
	}
}

func TestStatusOf(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	cases := []struct {
		meta Meta
		want string
	}{
		{Meta{}, "ACTIVE"},
		{Meta{ExpiresAt: &future}, "ACTIVE"},
		{Meta{ExpiresAt: &past}, "EXPIRED"},
		{Meta{ExpiresAt: &past, RevokedAt: &past}, "REVOKED"},
	}
	for _, c := range cases {
		if got := statusOf(c.meta, now); got != c.want {
			t.Errorf("%+v = %s，期望 %s", c.meta, got, c.want)
		}
	}
}

func TestBuildContentTitleAndReplyIndex(t *testing.T) {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	source := conversations.ShareSource{Title: "来源标题", Items: []conversations.ShareItem{
		{ID: "a", AuthorKind: "USER", AuthorName: "林", Body: "第一条", CreatedAt: at},
		{ID: "b", AuthorKind: "AGENT", AuthorName: "熊猫", Body: "回复", CreatedAt: at, ReplyToID: "a"},
		{ID: "c", AuthorKind: "USER", AuthorName: "林", Body: "孤儿回复", CreatedAt: at, ReplyToID: "gone"},
	}}
	items, title := buildContent(source, "")
	if title != "来源标题" || items[1].ReplyTo != 1 || items[2].ReplyTo != 0 {
		t.Fatalf("title=%q items=%+v", title, items)
	}
	if _, title := buildContent(source, "  自定义 "); title != "自定义" {
		t.Fatalf("title=%q", title)
	}
	source.Title = ""
	if _, title := buildContent(source, ""); title != "第一条" {
		t.Fatalf("title=%q", title)
	}
	long := conversations.ShareSource{Items: []conversations.ShareItem{{ID: "x", Body: strings.Repeat("字", 500), CreatedAt: at}}}
	if _, title := buildContent(long, ""); len([]rune(title)) != MaxTitleRunes {
		t.Fatalf("标题长度 %d", len([]rune(title)))
	}
	if _, title := buildContent(conversations.ShareSource{}, ""); title == "" {
		t.Fatal("标题不能为空")
	}
}
