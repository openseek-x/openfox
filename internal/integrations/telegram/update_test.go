package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestIDsAreExactIntegersAndStringOnOutput(t *testing.T) {
	for _, value := range []string{"4503599627370495", "-4503599627370495", "1", "0"} {
		var id ID
		if e := json.Unmarshal([]byte(value), &id); e != nil {
			t.Fatal(e)
		}
		encoded, e := json.Marshal(id)
		if e != nil || string(encoded) != `"`+value+`"` {
			t.Fatalf("id output %s %v", encoded, e)
		}
	}
	for _, value := range []string{`"123"`, `1.0`, `1e3`, `null`, `true`, `-0`, `4503599627370496`, `-4503599627370496`, `9223372036854775807`} {
		var id ID
		if json.Unmarshal([]byte(value), &id) == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	if _, e := json.Marshal(ID(MaxID + 1)); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}
func TestDecodePlainMemberAndMigrationFacts(t *testing.T) {
	t.Run("zero update identifier", func(t *testing.T) {
		u, e := DecodeUpdate([]byte(`{"update_id":0,"message":` + testMessage + `}`))
		if e != nil || u.ID != 0 || u.Message == nil {
			t.Fatalf("zero update ID: %+v %v", u, e)
		}
	})
	update, e := DecodeUpdate([]byte(`{"update_id":100,"message":` + testMessage + `}`))
	if e != nil || update.Message == nil || update.Message.Chat.ID != ID(-MaxID) || update.Message.From.ID != ID(MaxID) {
		t.Fatalf("%+v %v", update, e)
	}
	out, e := json.Marshal(update)
	if e != nil || !strings.Contains(string(out), `"id":"-4503599627370495"`) {
		t.Fatal("numeric API serialization", string(out), e)
	}
	member := `{"chat":` + testChat + `,"from":` + testUser + `,"date":1800000001,"old_chat_member":{"status":"member","user":` + testBot + `},"new_chat_member":{"status":"kicked","user":` + testBot + `,"until_date":0}}`
	update, e = DecodeUpdate([]byte(`{"update_id":99,"my_chat_member":` + member + `}`))
	if e != nil || update.MyChatMember == nil || update.MyChatMember.NewChatMember.Status != "kicked" {
		t.Fatal(e)
	}
	migration := `{"message_id":43,"date":1800000000,"chat":{"id":-5,"type":"group"},"migrate_to_chat_id":-1001234567890}`
	update, e = DecodeUpdate([]byte(`{"update_id":1,"message":` + migration + `}`))
	if e != nil || update.Kind != "migration" || update.Message.MigrateToChatID != -1001234567890 {
		t.Fatal(e)
	}
	// IDs are not treated as a monotonic high watermark: all three were decoded.
}
func TestUnsupportedShapesNeverBecomePlainInput(t *testing.T) {
	for name, field := range map[string]string{
		"topic": `"message_thread_id":1`, "private topic flag": `"is_topic_message":true`, "direct topic": `"direct_messages_topic":{"topic_id":3}`,
		"business": `"business_connection_id":"business-id"`, "guest": `"guest_query_id":"guest-id"`, "ephemeral": `"ephemeral_message_id":4`,
		"anonymous": `"sender_chat":` + testChat, "forward": `"forward_origin":{"type":"hidden_user"}`, "via bot": `"via_bot":` + testBot,
		"edited": `"edit_date":1800000001`, "photo": `"photo":[]`, "caption": `"caption":"@Current_Bot"`, "widget10.4": `"widget":{}`, "rich message": `"rich_message":{}`,
		"unknown new": `"future_shape":{}`, "service": `"new_chat_title":"rename"`, "paid": `"paid_star_count":1`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := `{"update_id":2,"message":` + strings.TrimSuffix(testMessage, "}") + `,` + field + `}}`
			u, e := DecodeUpdate([]byte(raw))
			if !errors.Is(e, ErrUnsupported) || u.ID != 2 || u.Message == nil {
				t.Fatalf("%+v %v", u, e)
			}
		})
	}
	for _, kind := range []string{"edited_message", "guest_message", "business_message", "channel_post", "callback_query", "widget_query", "unknown_future"} {
		t.Run(kind, func(t *testing.T) {
			u, e := DecodeUpdate([]byte(`{"update_id":3,"` + kind + `":{}}`))
			if !errors.Is(e, ErrUnsupported) || u.ID != 3 || u.Kind != "unsupported" {
				t.Fatal(e)
			}
		})
	}
	for name, msg := range map[string]string{
		"bot sender": strings.Replace(testMessage, testUser, testBot, 1), "no sender": strings.Replace(testMessage, `,"from":`+testUser, "", 1),
		"ephemeral0": strings.Replace(testMessage, `"message_id":42`, `"message_id":0`, 1),
		"forum":      strings.Replace(testMessage, `"type":"supergroup"`, `"type":"supergroup","is_forum":true`, 1),
		"channel":    strings.Replace(testMessage, `"type":"supergroup"`, `"type":"channel"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, e := DecodeUpdate([]byte(`{"update_id":4,"message":` + msg + `}`))
			if !errors.Is(e, ErrUnsupported) {
				t.Fatal(e)
			}
		})
	}
}
func TestMalformedUpdatesAreRejected(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate id":    `{"update_id":1,"update_id":2,"message":` + testMessage + `}`,
		"case alias":      `{"update_id":1,"message":` + strings.Replace(testMessage, `"message_id":42`, `"message_id":42,"MESSAGE_ID":9`, 1) + `}`,
		"float id":        `{"update_id":1.0,"message":` + testMessage + `}`,
		"missing bool":    `{"update_id":1,"message":` + strings.Replace(testMessage, `"is_bot":false,`, "", 1) + `}`,
		"two variants":    `{"update_id":1,"message":` + testMessage + `,"edited_message":` + testMessage + `}`,
		"null message":    `{"update_id":1,"message":null}`,
		"null entities":   `{"update_id":1,"message":` + strings.TrimSuffix(testMessage, "}") + `,"entities":null}}`,
		"lone surrogate":  `{"update_id":1,"message":` + strings.Replace(testMessage, `"hello"`, `"\ud800"`, 1) + `}`,
		"negative update": `{"update_id":-1,"message":` + testMessage + `}`,
		"zero date":       `{"update_id":1,"message":` + strings.Replace(testMessage, `1800000000`, `0`, 1) + `}`,
		"string user id":  `{"update_id":1,"message":` + strings.Replace(testMessage, `"id":4503599627370495`, `"id":"4503599627370495"`, 1) + `}`,
		"trailing":        `{"update_id":1,"message":` + testMessage + `} {}`,
		"oversize":        strings.Repeat(" ", MaxBodyBytes) + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, e := DecodeUpdate([]byte(raw))
			if !errors.Is(e, ErrInvalid) {
				t.Fatalf("got%v", e)
			}
		})
	}
	// A valid surrogate pair is decoded without loss; a literal backslash-u is text.
	for _, text := range []string{`"\ud83d\ude00"`, `"\\ud800"`, `"�"`} {
		raw := `{"update_id":1,"message":` + strings.Replace(testMessage, `"hello"`, text, 1) + `}`
		if _, e := DecodeUpdate([]byte(raw)); e != nil {
			t.Fatal(e)
		}
	}
}
func TestUTF16EntitiesAndCurrentBotTarget(t *testing.T) {
	bot := Bot{User: User{ID: 123456789, IsBot: true, FirstName: "Test", Username: "Current_Bot"}}
	base, e := parseMessage([]byte(testMessage), 0)
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name, text   string
		entities     []Entity
		mention      bool
		command, arg string
	}{
		{"emoji mention", "😀 @Current_Bot hi", []Entity{{Type: "mention", Offset: 3, Length: 12}}, true, "", ""},
		{"case insensitive", "@CURRENT_BOT", []Entity{{Type: "mention", Length: 12}}, true, "", ""},
		{"string alone", "@Current_Bot", nil, false, "", ""},
		{"other bot", "@Other_Bot", []Entity{{Type: "mention", Length: 10}}, false, "", ""},
		{"command group", "/ask@Current_Bot hello 😀", []Entity{{Type: "bot_command", Length: 16}}, true, "ask", "hello 😀"},
		{"plain group command", "/ask hello", []Entity{{Type: "bot_command", Length: 4}}, false, "", ""},
		{"other command", "/ask@Other_Bot hello", []Entity{{Type: "bot_command", Length: 14}}, false, "", ""},
		{"stable text mention", "bot", []Entity{{Type: "text_mention", Length: 3, User: &bot.User}}, true, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			m.Text = tc.text
			m.Entities = tc.entities
			facts, e := ParseText(m, bot)
			if e != nil || facts.Mentioned != tc.mention || facts.Command != tc.command || facts.CommandArgument != tc.arg {
				t.Fatalf("%+v %v", facts, e)
			}
		})
	}
	private := base
	private.Chat = Chat{ID: ID(MaxID), Type: "private"}
	private.Text = "/start random_code-123"
	private.Entities = []Entity{{Type: "bot_command", Length: 6}}
	facts, e := ParseText(private, bot)
	if e != nil || facts.Command != "start" || facts.CommandArgument != "random_code-123" {
		t.Fatal(facts, e)
	}
	m := base
	m.Text = "😀 @Current_Bot"
	m.Entities = []Entity{{Type: "mention", Offset: 1, Length: 1}}
	if _, e := ParseText(m, bot); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		offset, length int
		want           string
		valid          bool
	}{{0, 2, "😀", true}, {2, 1, " ", true}, {3, 12, "@Current_Bot", true}, {1, 1, "", false}, {0, 1, "", false}, {-1, 1, "", false}, {0, 99999, "", false}, {15, 1, "", false}} {
		got, e := UTF16Slice("😀 @Current_Bot", tc.offset, tc.length)
		if tc.valid && (e != nil || got != tc.want) || !tc.valid && e == nil {
			t.Fatalf("slice %+v got%q %v", tc, got, e)
		}
	}
	if UTF16Len("a😀中") != 4 {
		t.Fatal("length")
	}
	reply := base
	reply.ID = 55
	reply.From = &bot.User
	reply.Text = "previous"
	base.ReplyToMessage = &reply
	facts, e = ParseText(base, bot)
	if e != nil || !facts.ReplyToBot || facts.ReplyToMessageID != 55 {
		t.Fatal(facts, e)
	}
	reply.Chat.ID = -7
	facts, e = ParseText(base, bot)
	if e != nil || facts.ReplyToBot {
		t.Fatal("cross chat reply", e)
	}
}
func TestWebhookSecretSingleExactHeader(t *testing.T) {
	const secret = "only_synthetic-123"
	for _, tc := range []struct {
		name   string
		header http.Header
		want   bool
	}{
		{"normal", http.Header{"X-Telegram-Bot-Api-Secret-Token": {secret}}, true},
		{"lower", http.Header{"x-telegram-bot-api-secret-token": {secret}}, true},
		{"missing", http.Header{}, false},
		{"wrong", http.Header{"X-Telegram-Bot-Api-Secret-Token": {"wrong"}}, false},
		{"space", http.Header{"X-Telegram-Bot-Api-Secret-Token": {secret + " "}}, false},
		{"two", http.Header{"X-Telegram-Bot-Api-Secret-Token": {secret, secret}}, false},
		{"case duplicate", http.Header{"X-Telegram-Bot-Api-Secret-Token": {secret}, "x-telegram-bot-api-secret-token": {secret}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := VerifyWebhookSecret(secret, tc.header)
			if (e == nil) != tc.want {
				t.Fatal(e)
			}
			if e != nil && strings.Contains(fmt.Sprint(e), secret) {
				t.Fatal("secret leak")
			}
		})
	}
	if VerifyWebhookSecret("", http.Header{}) == nil {
		t.Fatal("empty config accepted")
	}
}

func TestTextAndEntityResourceBoundaries(t *testing.T) {
	base, e := parseMessage([]byte(testMessage), 0)
	if e != nil {
		t.Fatal(e)
	}
	bot := Bot{User: User{ID: 123456789, IsBot: true, FirstName: "Test", Username: "Current_Bot"}}
	base.Text = "/ask@Current_BotSuffix"
	base.Entities = []Entity{{Type: "bot_command", Length: 16}}
	if _, e = ParseText(base, bot); !errors.Is(e, ErrInvalid) {
		t.Fatalf("command prefix accepted %v", e)
	}
	for _, count := range []int{100, 101} {
		entities := strings.TrimSuffix(strings.Repeat(`{"type":"bold","offset":0,"length":1},`, count), ",")
		raw := `{"update_id":1,"message":` + strings.TrimSuffix(testMessage, "}") + `,"entities":[` + entities + `]}}`
		_, e := DecodeUpdate([]byte(raw))
		if (e == nil) != (count == 100) {
			t.Fatalf("entities %d: %v", count, e)
		}
	}
	text := strings.Repeat("😀", 4096)
	encoded, _ := json.Marshal(text)
	u, e := DecodeUpdate([]byte(`{"update_id":1,"message":` + strings.Replace(testMessage, `"hello"`, string(encoded), 1) + `}`))
	if e != nil || u.Message.Text != text {
		t.Fatalf("4096 rune text %v", e)
	}
	if _, e = UTF16Slice(text, 8190, 2); e != nil {
		t.Fatal(e)
	}
	deep := strings.Repeat(`{"next":`, 20) + `{}` + strings.Repeat(`}`, 20)
	if _, e = DecodeUpdate([]byte(`{"update_id":1,"future":` + deep + `}`)); !errors.Is(e, ErrInvalid) {
		t.Fatal("unbounded nesting", e)
	}
}

// Private destination and stable sender are distinct protocol facts. The
// binding service must validate their association, not equate their numbers.
func TestDecodePrivateChatDistinctFromSender(t *testing.T) {
	raw := `{"update_id":2,"message":` + strings.Replace(testMessage, testChat, `{"id":777888,"type":"private"}`, 1) + `}`
	u, err := DecodeUpdate([]byte(raw))
	if err != nil || u.Message == nil || u.Message.Chat.ID != 777888 || u.Message.From.ID != ID(MaxID) {
		t.Fatalf("distinct private route rejected: %+v %v", u, err)
	}
}
