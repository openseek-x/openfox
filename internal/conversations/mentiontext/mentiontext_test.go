package mentiontext

import (
	"reflect"
	"testing"
)

const (
	userID  = "11111111-1111-4111-8111-111111111111"
	agentID = "22222222-2222-4222-8222-222222222222"
	crewID  = "33333333-3333-4333-8333-333333333333"
)

func TestParseAllKinds(t *testing.T) {
	body := "你好 @[张三](openfox://mention/user/" + userID + ") 和 @[助手](openfox://mention/agent/" + agentID +
		")，@[小队A](openfox://mention/crew/" + crewID + ") 以及 @[全体成员](openfox://mention/all)"
	want := []Token{
		{KindUser, userID, "张三"}, {KindAgent, agentID, "助手"}, {KindCrew, crewID, "小队A"}, {KindAll, "", "全体成员"},
	}
	if got := Parse(body); !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse = %#v, want %#v", got, want)
	}
}

func TestParseLowercasesIDAndIgnoresMalformed(t *testing.T) {
	upper := "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	got := Parse("@[A](openfox://mention/user/" + upper + ")")
	if len(got) != 1 || got[0].ID != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("ID 应转小写: %#v", got)
	}
	for _, body := range []string{
		"", "plain @name", "@[x](openfox://mention/user/not-a-uuid)", "@[x](openfox://mention/all/" + userID + ")",
		"@[x](openfox://mention/team/" + userID + ")", "[x](openfox://mention/user/" + userID + ")",
		"@[x](https://mention/user/" + userID + ")", "@[a\nb](openfox://mention/user/" + userID + ")",
	} {
		if tokens := Parse(body); len(tokens) != 0 {
			t.Errorf("Parse(%q) = %#v, want none", body, tokens)
		}
	}
}

func TestParseEmptyNameAndAdjacentTokens(t *testing.T) {
	body := "@[](openfox://mention/all)@[b](openfox://mention/user/" + userID + ")"
	got := Parse(body)
	if len(got) != 2 || got[0].Kind != KindAll || got[0].Name != "" || got[1].Name != "b" {
		t.Fatalf("got %#v", got)
	}
}

func TestPlain(t *testing.T) {
	cases := map[string]string{
		"": "",
		"无令牌 @名字 [链接](https://x.com)":                                                               "无令牌 @名字 [链接](https://x.com)",
		"请 @[张三](openfox://mention/user/" + userID + ") 看，cc @[全体成员](openfox://mention/all)":        "请 @张三 看，cc @全体成员",
		"@[小队](openfox://mention/crew/" + crewID + ")\n```\n@[x](openfox://mention/agent/bad)\n```": "@小队\n```\n@[x](openfox://mention/agent/bad)\n```",
	}
	for in, want := range cases {
		if got := Plain(in); got != want {
			t.Errorf("Plain(%q) = %q, want %q", in, got, want)
		}
	}
}
