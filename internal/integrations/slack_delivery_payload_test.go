package integrations

import (
	"strings"
	"testing"
	"unicode/utf8"

	"openfox/internal/integrations/slack"
)

func TestSlackDeliveryTextBudgets(t *testing.T) {
	link := "https://openfox.example.test/o/org/chat/conversation"
	bodies := []string{"hello <!channel>", strings.Repeat("界🙂", 4000), strings.Repeat("&", 8000), strings.Repeat("<>&", 3000)}
	for n := 1; n <= 20; n++ {
		bodies = append(bodies, strings.Repeat("&<", n)+strings.Repeat("&", 8000-2*n))
	}
	for _, body := range bodies {
		parts, err := slackTextParts(body, link, 10)
		if err != nil || len(parts) > 10 || len(parts) == 0 {
			t.Fatalf("parts=%d error=%v", len(parts), err)
		}
		for _, part := range parts {
			if !utf8.ValidString(part) || utf8.RuneCountInString(slack.EscapeText(part)) > 4000 {
				t.Fatal("invalid wire budget")
			}
		}
		if !strings.HasSuffix(parts[len(parts)-1], link) {
			t.Fatal("protected link lost")
		}
		joined := strings.TrimSuffix(strings.Join(parts, ""), "\n\n"+link)
		if joined != body && !strings.HasSuffix(joined, "…") {
			t.Fatal("silent truncation")
		}
	}
	parts, err := slackTextParts(strings.Repeat("&", 8000), link, 1)
	if err != nil || len(parts) != 1 || !strings.Contains(parts[0], "…") {
		t.Fatalf("notification=%v %v", parts, err)
	}
	for _, body := range []string{"", "bad\x00text", "bad\x1btext", string([]byte{0xff})} {
		if _, err := slackTextParts(body, link, 10); err == nil {
			t.Fatal("invalid body accepted")
		}
	}
}

func TestSlackDeliveryReceiptRequiresExactEvidence(t *testing.T) {
	target := slackReceiptTarget{AppID: "A1", BotID: "B1", BotUserID: "U1", TeamID: "T1", ChannelID: "C1", ThreadTS: "100.01", Marker: "random-marker", Part: 2, Body: slackDeliveryBody{Text: "hello & <secret>"}}
	base := slack.Message{Type: "message", Subtype: "bot_message", AppID: "A1", BotID: "B1", User: "U1", Team: "T1", ThreadTS: "100.01", TS: "100.02", Text: slack.EscapeText(target.Body.Text), Metadata: slackPartRequest("C1", "100.01", target.Marker, 2, target.Body).Metadata}
	if !slackReceiptMatches(target, "T1", "C1", base) {
		t.Fatal("own receipt rejected")
	}
	rewritten := base
	rewritten.Text = "Slack-normalized :tada:"
	if !slackReceiptMatches(target, "T1", "C1", rewritten) {
		t.Fatal("server rendering invalidated delivery evidence")
	}
	forged := base
	forged.Metadata = slackPartRequest("C1", "100.01", target.Marker, 2, slackDeliveryBody{Text: "different body"}).Metadata
	if slackReceiptMatches(target, "T1", "C1", forged) {
		t.Fatal("different original body hash accepted")
	}
	if slackReceiptMatches(target, "T2", "C1", base) || slackReceiptMatches(target, "T1", "C2", base) {
		t.Fatal("wrong authenticated route accepted")
	}
	for _, change := range []func(*slack.Message){
		func(m *slack.Message) { m.AppID = "A2" }, func(m *slack.Message) { m.BotID = "B2" }, func(m *slack.Message) { m.User = "U2" },
		func(m *slack.Message) { m.Team = "T2" }, func(m *slack.Message) { m.ThreadTS = "100.03" }, func(m *slack.Message) { m.Metadata = nil },
		func(m *slack.Message) { m.Subtype = "message_changed" },
		func(m *slack.Message) {
			m.Metadata = &slack.MessageMetadata{EventType: slackDeliveryEvent, EventPayload: map[string]string{"delivery_id": "wrong", "part": "2"}}
		},
	} {
		m := base
		change(&m)
		if slackReceiptMatches(target, "T1", "C1", m) {
			t.Fatal("mismatched receipt accepted")
		}
	}
}
