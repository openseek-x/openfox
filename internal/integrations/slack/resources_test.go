package slack

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUsersConversationsAndPagination(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing token")
		}
		q := r.URL.Query()
		switch r.URL.Path {
		case "/api/users.info":
			if q.Get("user") != "W1" {
				t.Error("wrong user")
			}
			jsonReply(w, `{"ok":true,"user":{"id":"W1","team_id":"T1","name":"ordinary","deleted":false,"is_bot":false,"is_app_user":false,"is_restricted":false,"is_ultra_restricted":false}}`)
		case "/api/conversations.info":
			jsonReply(w, `{"ok":true,"channel":{"id":"C1","name":"modern-private","is_channel":true,"is_group":false,"is_im":false,"is_mpim":false,"is_private":true,"is_member":true,"is_shared":false,"is_ext_shared":false,"is_org_shared":false,"context_team_id":"T1","shared_team_ids":["T1"]}}`)
		case "/api/conversations.members":
			if q.Get("limit") != "200" {
				t.Error("members page limit")
			}
			if q.Get("cursor") == "" {
				jsonReply(w, `{"ok":true,"members":["W1"],"response_metadata":{"next_cursor":"opaque+/="}}`)
			} else if q.Get("cursor") == "opaque+/=" {
				jsonReply(w, `{"ok":true,"members":[],"response_metadata":{"next_cursor":""}}`)
			} else {
				t.Error("cursor changed")
			}
		case "/api/conversations.history":
			if q.Get("limit") != "15" || q.Get("latest") != "1700000000.000099" || q.Get("include_all_metadata") != "true" {
				t.Error("history arguments")
			}
			jsonReply(w, `{"ok":true,"messages":[{"type":"message","user":"W1","text":"context","ts":"1700000000.000001","metadata":{"event_type":"third_party","event_payload":{"nested":{"number":1}}}}],"has_more":true,"response_metadata":{"next_cursor":"next"}}`)
		case "/api/conversations.replies":
			if q.Get("ts") != "1700000000.000001" {
				t.Error("thread changed")
			}
			jsonReply(w, `{"ok":true,"messages":[{"type":"message","user":"W1","text":"root","ts":"1700000000.000001"},{"type":"message","bot_id":"B1","app_id":"A1","text":"reply","ts":"1700000000.000002","thread_ts":"1700000000.000001","metadata":{"event_type":"openfox_delivery","event_payload":{"delivery_id":"random-id","part":"1"}}}],"has_more":false}`)
		case "/api/conversations.open":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			if in["users"] != "W1" || in["return_im"] != true {
				t.Error("not exact single user")
			}
			jsonReply(w, `{"ok":true,"channel":{"id":"D1","is_im":true,"user":"W1"}}`)
		default:
			t.Error("unexpected path", r.URL.Path)
		}
	})
	ctx := context.Background()
	u, e := client.UserInfo(ctx, "token", "W1")
	if e != nil || u.IsStranger != nil || u.TeamID != "T1" {
		t.Fatal("ordinary user without optional stranger", e)
	}
	c, e := client.ConversationInfo(ctx, "token", "C1")
	if e != nil || c.IsPrivate == nil || !*c.IsPrivate || c.IsGroup == nil || *c.IsGroup {
		t.Fatal("modern private shape", e)
	}
	members, e := client.ConversationMembers(ctx, "token", "C1", "")
	if e != nil || members.Next != "opaque+/=" {
		t.Fatal(e)
	}
	last, e := client.ConversationMembers(ctx, "token", "C1", members.Next)
	if e != nil || last.Next != "" || len(last.Members) != 0 {
		t.Fatal(e)
	}
	h, e := client.History(ctx, "token", HistoryInput{Channel: "C1", Latest: "1700000000.000099", Limit: 15})
	if e != nil || !h.HasMore || h.Messages[0].TS != "1700000000.000001" || h.Messages[0].Metadata.EventPayload != nil {
		t.Fatal("history/coercion", e)
	}
	thread, e := client.Replies(ctx, "token", HistoryInput{Channel: "C1", ThreadTS: "1700000000.000001", Limit: 15})
	if e != nil || thread.HasMore || thread.Messages[1].Metadata.EventPayload["part"] != "1" {
		t.Fatal("thread", e)
	}
	dm, e := client.OpenDM(ctx, "token", "W1")
	if e != nil || dm.User != "W1" {
		t.Fatal(e)
	}
}
func TestMalformedPagesDoNotProveEmpty(t *testing.T) {
	for _, body := range []string{`{"ok":true,"messages":null}`, `{"ok":true}`, `{"ok":true,"messages":{}}`, `{"ok":true,"messages":[{"ts":"1.1"},{"ts":"1.1"}]}`, `{"ok":true,"messages":[],"response_metadata":{"next_cursor":"same"}}`} {
		t.Run(body, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, body) })
			_, e := c.History(context.Background(), "token", HistoryInput{Channel: "C1", Cursor: "same", Limit: 15})
			if !errors.Is(e, ErrIncomplete) {
				t.Fatal("malformed complete page", e)
			}
		})
	}
	t.Run("missing-more-is-incomplete-evidence", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, `{"ok":true,"messages":[]}`) })
		p, e := c.History(context.Background(), "token", HistoryInput{Channel: "C1", Limit: 15})
		if e != nil || !p.Limited {
			t.Fatal("absence assumed complete", e)
		}
	})
	t.Run("foreign-thread", func(t *testing.T) {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			jsonReply(w, `{"ok":true,"messages":[{"ts":"1.2","thread_ts":"1.3"}],"has_more":false}`)
		})
		_, e := c.Replies(context.Background(), "token", HistoryInput{Channel: "C1", ThreadTS: "1.1", Limit: 15})
		if !errors.Is(e, ErrIncomplete) {
			t.Fatal("foreign thread accepted")
		}
	})
	for _, body := range []string{`{"ok":true,"members":null}`, `{"ok":true,"members":["W1","W1"]}`} {
		t.Run("members-"+body, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { jsonReply(w, body) })
			if _, e := c.ConversationMembers(context.Background(), "token", "C1", ""); !errors.Is(e, ErrIncomplete) {
				t.Fatal(e)
			}
		})
	}
}
func TestSafeMessageAndUnfurl(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		switch r.URL.Path {
		case "/api/chat.postMessage":
			if in["text"] != "&lt;!everyone&gt; &amp; text" || in["mrkdwn"] != false || in["parse"] != "none" || in["reply_broadcast"] != false || in["unfurl_links"] != false || in["unfurl_media"] != false || in["thread_ts"] != "1.000001" {
				t.Error("unsafe text settings")
			}
			jsonReply(w, `{"ok":true,"channel":"C1","ts":"1.000002"}`)
		case "/api/chat.unfurl":
			previews := in["unfurls"].(map[string]any)
			p := previews["https://openfox.example.test/task/1"].(map[string]any)
			if p["title"] != "&lt;@W1&gt;" || len(p["mrkdwn_in"].([]any)) != 0 {
				t.Error("unsafe unfurl")
			}
			jsonReply(w, `{"ok":true}`)
		}
	})
	receipt, e := c.PostMessage(context.Background(), "token", PostMessageInput{Channel: "C1", ThreadTS: "1.000001", Text: "<!everyone> & text", Metadata: &MessageMetadata{EventType: "openfox_delivery", EventPayload: map[string]string{"delivery_id": "random-id", "part": "1"}}})
	if e != nil || receipt.TS != "1.000002" {
		t.Fatal(e)
	}
	if e = c.Unfurl(context.Background(), "token", "C1", "1.000001", map[string]UnfurlPreview{"https://openfox.example.test/task/1": {Title: "<@W1>", Text: "safe"}}); e != nil {
		t.Fatal(e)
	}
	for _, in := range []PostMessageInput{{Channel: "C1", Text: strings.Repeat("&", 801)}, {Channel: "C1", Text: "line\rbreak"}, {Channel: "C1", Text: "x", ThreadTS: "nan"}, {Channel: "C1", Text: "x", Metadata: &MessageMetadata{EventType: "event", EventPayload: map[string]string{"part": "a\nsecret"}}}} {
		if _, e = c.PostMessage(context.Background(), "token", in); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid sent", e)
		}
	}
	if _, e = c.OpenDM(context.Background(), "token", "W1,W2"); !errors.Is(e, ErrInvalid) {
		t.Fatal("MPIM accepted")
	}
	if _, e = c.History(context.Background(), "token", HistoryInput{Channel: "C1", Limit: 16}); !errors.Is(e, ErrInvalid) {
		t.Fatal("unbounded history")
	}
	if _, e = c.AuthTest(context.Background(), ""); !errors.Is(e, ErrInvalid) {
		t.Fatal("missing token sent")
	}
	if calls.Load() != 2 {
		t.Fatal("rejected input had HTTP side effects", calls.Load())
	}
}
func TestSignatureExactBytesAndTimeWindow(t *testing.T) {
	body := []byte(`{"type":"url_verification","challenge":"synthetic"}`)
	stamp := strconv.FormatInt(testNow.Unix(), 10)
	sign := func(b []byte, s string) string {
		m := hmac.New(sha256.New, []byte("synthetic-signing"))
		m.Write([]byte("v0:" + s + ":"))
		m.Write(b)
		return "v0=" + hex.EncodeToString(m.Sum(nil))
	}
	signature := sign(body, stamp)
	for _, offset := range []time.Duration{-300 * time.Second, 0, 300 * time.Second} {
		if e := VerifySignature("synthetic-signing", stamp, signature, body, testNow.Add(offset)); e != nil {
			t.Fatal(e)
		}
	}
	for _, tc := range []struct {
		body       []byte
		stamp, sig string
		now        time.Time
	}{{append(append([]byte{}, body...), '\n'), stamp, signature, testNow}, {body, stamp, signature, testNow.Add(301 * time.Second)}, {body, stamp, signature, testNow.Add(-301 * time.Second)}, {body, "0" + stamp, signature, testNow}, {body, stamp, "v1=" + signature[3:], testNow}, {make([]byte, MaxEventBytes+1), stamp, signature, testNow}} {
		if e := VerifySignature("synthetic-signing", tc.stamp, tc.sig, tc.body, tc.now); !errors.Is(e, ErrSignature) {
			t.Fatal("invalid signature accepted")
		}
	}
}
