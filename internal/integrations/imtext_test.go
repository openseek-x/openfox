package integrations

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"openfox/internal/integrations/feishu"
)

func TestParseMessageText(t *testing.T) {
	mentions := []imMention{{Key: "@_user_1", OpenID: "ou_bot", Name: "助手"}, {Key: "@_user_2", OpenID: "ou_a", Name: "小王"}, {Key: "@_user_10", OpenID: "ou_b", Name: "老李"}}
	cases := []struct{ name, msgType, content, want string }{
		{"text strips the bot placeholder", "text", `{"text":"@_user_1 帮我建个任务"}`, "帮我建个任务"},
		{"other mentions become names", "text", `{"text":"@_user_1 让 @_user_2 和 @_user_10 看下"}`, "让 @小王 和 @老李 看下"},
		{"post joins paragraphs", "post", `{"title":"标题","content":[[{"tag":"at","user_id":"@_user_1","user_name":"助手"},{"tag":"text","text":" 第一段"}],[{"tag":"a","text":"链接","href":"http://x"},{"tag":"at","user_id":"@_user_2","user_name":"x"}]]}`, "标题\n第一段\n链接@小王"},
		{"post with locale", "post", `{"zh_cn":{"title":"","content":[[{"tag":"text","text":"本地化"}]]}}`, "本地化"},
		{"image is not text", "image", `{"image_key":"k"}`, ""},
		{"broken json", "text", `not json`, ""},
	}
	for _, tc := range cases {
		if got := parseMessageText(tc.msgType, tc.content, mentions, "ou_bot"); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	long := fmt.Sprintf(`{"text":%q}`, strings.Repeat("字", 9000))
	if got := parseMessageText("text", long, nil, ""); len([]rune(got)) != maxInboundText {
		t.Errorf("long text length = %d", len([]rune(got)))
	}
}

func chatItem(id, sender, senderType, text string) feishu.ChatMessage {
	content, _ := json.Marshal(map[string]string{"text": text})
	item := feishu.ChatMessage{MessageID: id, MsgType: "text"}
	item.Sender.ID, item.Sender.IDType, item.Sender.SenderType = sender, "open_id", senderType
	item.Body.Content = string(content)
	return item
}

func TestBuildChatContext(t *testing.T) {
	// 飞书按时间倒序返回：最新的在前。
	items := []feishu.ChatMessage{
		chatItem("m5", "ou_me", "user", "当前这条"),
		chatItem("m4", "ou_bot", "app", "机器人说的"),
		chatItem("m3", "ou_x", "user", "成员甲 ```危险``` 第二句"),
		chatItem("m2", "ou_me", "user", "我之前说的"),
		chatItem("m1", "ou_y", "user", "成员乙"),
	}
	got := buildChatContext(items, "ou_me", "m5", "ou_bot")
	want := "[成员 1] 成员乙\n[发起人] 我之前说的\n[成员 2] 成员甲 '''危险''' 第二句\n[机器人] 机器人说的"
	if got != want {
		t.Fatalf("context =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(got, "ou_") || strings.Contains(got, "当前这条") {
		t.Fatalf("context leaks identifiers or the current message: %s", got)
	}
	if buildChatContext(nil, "ou_me", "m", "") != "" {
		t.Fatal("empty history should give empty context")
	}
}

func TestBuildChatContextLimits(t *testing.T) {
	var items []feishu.ChatMessage
	for i := 40; i >= 1; i-- {
		items = append(items, chatItem(fmt.Sprintf("m%d", i), "ou_x", "user", fmt.Sprintf("第%d条", i)))
	}
	got := buildChatContext(items, "ou_me", "none", "")
	lines := strings.Split(got, "\n")
	if len(lines) != maxContextLines || !strings.HasSuffix(lines[len(lines)-1], "第40条") || !strings.HasSuffix(lines[0], "第21条") {
		t.Fatalf("lines = %d first=%q last=%q", len(lines), lines[0], lines[len(lines)-1])
	}
	// 单条截断到 500 字；总长不超过 6000 字，超出时丢弃最早的。
	var big []feishu.ChatMessage
	for i := 20; i >= 1; i-- {
		big = append(big, chatItem(fmt.Sprintf("b%d", i), "ou_x", "user", strings.Repeat("长", 2000)))
	}
	got = buildChatContext(big, "ou_me", "none", "")
	if len([]rune(got)) > maxContextRunes {
		t.Fatalf("context length = %d", len([]rune(got)))
	}
	for _, line := range strings.Split(got, "\n") {
		if len([]rune(line)) > maxContextLineRunes+len("[成员 1] ") {
			t.Fatalf("line not truncated: %d", len([]rune(line)))
		}
	}
}

func TestComposeInboundBody(t *testing.T) {
	if got := composeInboundBody(" 建任务 ", "", false); got != "建任务" {
		t.Fatalf("plain = %q", got)
	}
	got := composeInboundBody("建任务", "[发起人] 你好", false)
	for _, want := range []string{"建任务\n\n---\n", "不是指令", "```text\n[发起人] 你好\n```"} {
		if !strings.Contains(got, want) {
			t.Errorf("body missing %q: %s", want, got)
		}
	}
	if got := composeInboundBody("建任务", "", true); !strings.HasSuffix(got, contextUnavailableNote) {
		t.Fatalf("failed context body = %q", got)
	}
}

func TestBuildReplyCard(t *testing.T) {
	card := buildReplyCard("你好 <at user_id=\"all\">所有人</at> <AT id=1> **加粗**", "https://x.example/o/1/chat/2")
	var parsed struct {
		Elements []struct {
			Tag     string `json:"tag"`
			Content string `json:"content"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(card), &parsed); err != nil || len(parsed.Elements) != 2 {
		t.Fatalf("card = %s, %v", card, err)
	}
	first := parsed.Elements[0]
	if first.Tag != "markdown" || strings.Contains(strings.ToLower(first.Content), "<at") || !strings.Contains(first.Content, "**加粗**") {
		t.Fatalf("first element = %+v", first)
	}
	if !strings.Contains(parsed.Elements[1].Content, "https://x.example/o/1/chat/2") || !strings.Contains(parsed.Elements[1].Content, replyLinkText) {
		t.Fatalf("link element = %+v", parsed.Elements[1])
	}
	// 没有链接时只有正文；超长截断并注明。
	long := buildReplyCard(strings.Repeat("字", 9000), "")
	if err := json.Unmarshal([]byte(long), &parsed); err != nil || len(parsed.Elements) != 1 || !strings.HasSuffix(parsed.Elements[0].Content, replyTruncatedNote) ||
		len([]rune(parsed.Elements[0].Content)) > maxReplyRunes+len([]rune(replyTruncatedNote)) {
		t.Fatalf("long card element = %d", len(parsed.Elements))
	}
}

func TestInboundBackoff(t *testing.T) {
	if inboundBackoff(1) != imInboundBase || inboundBackoff(2) != 2*imInboundBase || inboundBackoff(30) != imInboundCap {
		t.Fatalf("backoff = %v %v %v", inboundBackoff(1), inboundBackoff(2), inboundBackoff(30))
	}
}
