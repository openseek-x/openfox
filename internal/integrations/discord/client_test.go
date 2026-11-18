package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "synthetic_discord_token_do_not_log"

type fixtureTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (t fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || (r.URL.Host != "discord.com" && r.URL.Host != "gateway.discord.gg" && r.URL.Host != "gateway-us-east1.discord.gg") {
		return nil, errors.New("test rejected unapproved origin")
	}
	c := r.Clone(r.Context())
	u := *r.URL
	u.Host = t.target.Host
	c.URL = &u
	c.Host = t.target.Host
	return t.inner.RoundTrip(c)
}
func tlsClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewTLSServer(h)
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL)
	return NewClient(fixtureTransport{u, s.Client().Transport}), s
}
func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func messageJSON(id, channel, author, content string, bot bool) map[string]any {
	return map[string]any{"id": id, "channel_id": channel, "author": map[string]any{"id": author, "username": "fixture", "bot": bot}, "content": content, "type": 0, "mentions": []any{}, "timestamp": "2026-11-15T00:00:00Z"}
}
func intent() MessageIntent {
	return MessageIntent{BotID: "11", ChannelID: "22", MessageID: "33", Content: "plain <@44> & 😀", Nonce: "unchanged_nonce"}
}
func sendResponse(in MessageIntent) map[string]any {
	v := messageJSON("44", string(in.ChannelID), string(in.BotID), in.Content, true)
	v["nonce"] = in.Nonce
	v["message_reference"] = map[string]any{"type": 0, "message_id": in.MessageID, "channel_id": in.ChannelID}
	return v
}
func TestResourcesUseFixedAuthenticatedOrigin(t *testing.T) {
	var calls atomic.Int64
	c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bot "+testToken || r.Method != "GET" || r.Header.Get("User-Agent") != userAgent || r.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong authentication/method")
		}
		switch r.URL.Path {
		case "/api/v10/users/@me":
			jsonResponse(w, map[string]any{"id": "11", "bot": true, "username": "Bot"})
		case "/api/v10/applications/@me":
			jsonResponse(w, map[string]any{"id": "12", "name": "Application", "bot_public": false, "bot_require_code_grant": false, "bot": map[string]any{"id": "11", "bot": true}, "flags": 1 << 19})
		case "/api/v10/gateway/bot":
			jsonResponse(w, map[string]any{"url": "wss://gateway.discord.gg/", "shards": 1, "session_start_limit": map[string]any{"total": 1000, "remaining": 10, "reset_after": 1000, "max_concurrency": 1}})
		case "/api/v10/channels/22":
			jsonResponse(w, map[string]any{"id": "22", "type": 1, "recipients": []any{map[string]any{"id": "55", "username": "Person"}}})
		case "/api/v10/channels/22/permissions/member/55":
			if r.URL.Query().Get("implicit") != "true" {
				t.Error("implicit missing")
			}
			jsonResponse(w, map[string]any{"permissions": "18446744073709620224"})
		case "/api/v10/guilds/66/members/55":
			jsonResponse(w, map[string]any{"user": map[string]any{"id": "55"}, "pending": false, "communication_disabled_until": nil})
		case "/api/v10/guilds/66/channels":
			jsonResponse(w, []any{map[string]any{"id": "22", "type": 0, "guild_id": "66", "name": "text"}, map[string]any{"id": "23", "type": 11, "guild_id": "66"}})
		case "/api/v10/channels/22/messages/33":
			jsonResponse(w, messageJSON("33", "22", "55", "hello", false))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(400)
		}
	})
	ctx := context.Background()
	me, e := c.GetMe(ctx, testToken)
	if e != nil || me.ID != "11" {
		t.Fatal(e)
	}
	app, e := c.GetApplication(ctx, testToken)
	if e != nil || app.ID == me.ID || !app.MessageContentEnabled() {
		t.Fatal(e)
	}
	gw, e := c.GetGatewayBot(ctx, testToken)
	if e != nil || gw.Shards != 1 {
		t.Fatal(e)
	}
	ch, e := c.GetChannel(ctx, testToken, "22")
	if e != nil || !DMRecipient(ch, "55") || DMRecipient(ch, "22") {
		t.Fatal(e)
	}
	p, e := c.GetMemberPermissions(ctx, testToken, "22", "55")
	if e != nil || !p.Has(PermissionReadHistory|PermissionSendMessages|PermissionViewChannel) {
		t.Fatal(p, e)
	}
	member, e := c.GetGuildMember(ctx, testToken, "66", "55")
	if e != nil || member.User.ID != "55" {
		t.Fatal(e)
	}
	channels, e := c.ListChannels(ctx, testToken, "66")
	if e != nil || len(channels) != 2 || !channels[0].Supported() || channels[1].Supported() {
		t.Fatal(e)
	}
	if _, e = c.GetMessage(ctx, testToken, "22", "33"); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 8 {
		t.Fatal(calls.Load())
	}
}
func TestGuildPaginationAndHistoryBoundaries(t *testing.T) {
	c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v10/users/@me/guilds":
			if r.URL.Query().Get("limit") != "3" || r.URL.Query().Get("after") != "100" {
				t.Error("bounded cursor")
			}
			jsonResponse(w, []any{map[string]any{"id": "103", "name": "third"}, map[string]any{"id": "101", "name": "first"}, map[string]any{"id": "102", "name": "second"}})
		case "/api/v10/channels/22/messages":
			if r.URL.Query().Get("before") != "100" || r.URL.Query().Get("limit") != "20" || len(r.URL.Query()) != 2 {
				t.Error("wrong original history scope")
			}
			jsonResponse(w, []any{messageJSON("99", "22", "55", "last", false), messageJSON("98", "22", "55", "older", false)})
		}
	})
	page, e := c.ListGuilds(context.Background(), testToken, "100", 2)
	if e != nil || page.Next != "102" || len(page.Guilds) != 2 {
		t.Fatal(page, e)
	}
	history, e := c.GetHistory(context.Background(), testToken, "22", "100")
	if e != nil || len(history) != 2 {
		t.Fatal(e)
	}
}
func TestNullDuplicateAndWrongScopeFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		call       func(*Client) error
	}{
		{"null guilds", "null", func(c *Client) error { _, e := c.ListGuilds(context.Background(), testToken, "", 50); return e }},
		{"null channels", "null", func(c *Client) error { _, e := c.ListChannels(context.Background(), testToken, "66"); return e }},
		{"null history", "null", func(c *Client) error { _, e := c.GetHistory(context.Background(), testToken, "22", "100"); return e }},
		{"duplicate id", `{"id":"11","id":"12","username":"Bot","bot":true}`, func(c *Client) error { _, e := c.GetMe(context.Background(), testToken); return e }},
		{"numeric id", `{"id":11,"username":"Bot","bot":true}`, func(c *Client) error { _, e := c.GetMe(context.Background(), testToken); return e }},
		{"wrong channel", `{"id":"23","type":0,"guild_id":"66"}`, func(c *Client) error { _, e := c.GetChannel(context.Background(), testToken, "22"); return e }},
		{"missing type", `{"id":"22","guild_id":"66"}`, func(c *Client) error { _, e := c.GetChannel(context.Background(), testToken, "22"); return e }},
		{"listed missing type", `[{"id":"22","guild_id":"66"}]`, func(c *Client) error { _, e := c.ListChannels(context.Background(), testToken, "66"); return e }},
		{"missing reset", `{"url":"wss://gateway.discord.gg/","shards":1,"session_start_limit":{"total":1000,"remaining":10,"max_concurrency":1}}`, func(c *Client) error { _, e := c.GetGatewayBot(context.Background(), testToken); return e }},
		{"missing permissions", `{}`, func(c *Client) error {
			_, e := c.GetMemberPermissions(context.Background(), testToken, "22", "55")
			return e
		}},
		{"guild duplicate", `[{"id":"1","name":"one"},{"id":"1","name":"again"}]`, func(c *Client) error { _, e := c.ListGuilds(context.Background(), testToken, "", 50); return e }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })
			if !errors.Is(tc.call(c), ErrResponse) {
				t.Fatal("malformed accepted")
			}
		})
	}
	for name, rows := range map[string][]any{"same as trigger": {messageJSON("100", "22", "55", "", false)}, "wrong channel": {messageJSON("99", "23", "55", "", false)}, "wrong order": {messageJSON("98", "22", "55", "", false), messageJSON("99", "22", "55", "", false)}} {
		t.Run(name, func(t *testing.T) {
			c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, rows) })
			if _, e := c.GetHistory(context.Background(), testToken, "22", "100"); !errors.Is(e, ErrResponse) {
				t.Fatal(e)
			}
		})
	}
}
func TestSendWireAndUnknownNeverAutomaticallyRetry(t *testing.T) {
	for _, mode := range []string{"success", "201", "disconnect", "500", "wrong channel", "wrong author", "wrong reference", "wrong nonce", "wrong content", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var v map[string]any
				if json.NewDecoder(r.Body).Decode(&v) != nil {
					t.Error("decode")
				}
				mentions := v["allowed_mentions"].(map[string]any)
				ref := v["message_reference"].(map[string]any)
				if r.Method != "POST" || v["content"] != intent().Content || v["enforce_nonce"] != true || v["tts"] != false || len(mentions["parse"].([]any)) != 0 || mentions["replied_user"] != false || ref["fail_if_not_exists"] != true {
					t.Error("unsafe wire")
				}
				if mode == "disconnect" {
					conn, _, _ := w.(http.Hijacker).Hijack()
					_ = conn.Close()
					return
				}
				if mode == "201" {
					w.WriteHeader(201)
				}
				if mode == "500" {
					w.WriteHeader(500)
				}
				if mode == "malformed" {
					fmt.Fprint(w, `{`)
					return
				}
				out := sendResponse(intent())
				switch mode {
				case "wrong channel":
					out["channel_id"] = "23"
				case "wrong author":
					out["author"] = map[string]any{"id": "99", "bot": true}
				case "wrong reference":
					out["message_reference"] = map[string]any{"message_id": "99", "channel_id": "22"}
				case "wrong nonce":
					out["nonce"] = "different"
				case "wrong content":
					out["content"] = "different"
				}
				jsonResponse(w, out)
			})
			_, e := c.CreateMessage(context.Background(), testToken, intent())
			if mode == "success" {
				if e != nil {
					t.Fatal(e)
				}
			} else if !errors.Is(e, ErrUnknown) {
				t.Fatal(e)
			}
			if calls.Load() != 1 {
				t.Fatal("automatic write retry", calls.Load())
			}
		})
	}
}
func TestRateLimitAndSafeErrors(t *testing.T) {
	for _, retry := range []string{"0.125", "900000", "9223372036854775808", "0", "-1", `"1.5"`} {
		t.Run(retry, func(t *testing.T) {
			c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-RateLimit-Bucket", "bucket")
				w.Header().Set("X-RateLimit-Scope", "shared")
				w.WriteHeader(429)
				fmt.Fprintf(w, `{"message":%q,"retry_after":%s,"global":true}`, testToken, retry)
			})
			_, e := c.CreateMessage(context.Background(), testToken, intent())
			var rate *Error
			if !errors.As(e, &rate) || !errors.Is(e, ErrRateLimited) || !rate.Global || strings.Contains(e.Error(), testToken) {
				t.Fatal(e)
			}
			want := time.Duration(0)
			if retry == "0.125" {
				want = 125 * time.Millisecond
			}
			if retry == "900000" {
				want = 900000 * time.Second
			}
			if rate.RetryAfter != want {
				t.Fatal(rate.RetryAfter)
			}
		})
	}
	for code, kind := range map[int]error{401: ErrAuth, 403: ErrForbidden, 404: ErrNotFound, 503: ErrUnavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code); fmt.Fprint(w, testToken) })
			_, e := c.GetMe(context.Background(), testToken)
			if !errors.Is(e, kind) || strings.Contains(e.Error(), testToken) {
				t.Fatal(e)
			}
		})
	}
}
func TestCertificateRedirectCancellationAndBodyBudget(t *testing.T) {
	t.Run("certificate", func(t *testing.T) {
		_, s := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted request reached handler") })
		u, _ := url.Parse(s.URL)
		c := NewClient(fixtureTransport{u, http.DefaultTransport})
		_, e := c.GetMe(context.Background(), testToken)
		if !errors.Is(e, ErrUnavailable) || strings.Contains(e.Error(), testToken) {
			t.Fatal(e)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		var calls atomic.Int64
		c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Redirect(w, r, "https://untrusted.example/"+testToken, 302)
		})
		_, e := c.CreateMessage(context.Background(), testToken, intent())
		if !errors.Is(e, ErrUnknown) || calls.Load() != 1 || strings.Contains(e.Error(), testToken) {
			t.Fatal(e)
		}
	})
	t.Run("cancel after write", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { cancel() })
		_, e := c.CreateMessage(ctx, testToken, intent())
		if !errors.Is(e, ErrUnknown) {
			t.Fatal(e)
		}
	})
	t.Run("large response", func(t *testing.T) {
		c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", MaxResponseBytes+1)))
		})
		_, e := c.GetMe(context.Background(), testToken)
		if !errors.Is(e, ErrResponse) {
			t.Fatal(e)
		}
	})
}
func TestIDsMentionAndChannelBoundaries(t *testing.T) {
	for _, s := range []string{"1", "18446744073709551615"} {
		var id ID
		if json.Unmarshal([]byte(`"`+s+`"`), &id) != nil || string(id) != s {
			t.Fatal(s)
		}
	}
	for _, s := range []string{`"0"`, `"01"`, `"18446744073709551616"`, `1`, `null`, `"-1"`} {
		var id ID
		if json.Unmarshal([]byte(s), &id) == nil {
			t.Fatal(s)
		}
	}
	m := Message{Content: "😀 <@11> hello <@22>", Mentions: []User{{ID: "11"}}}
	text, ok := MentionText(m, "11")
	if !ok || text != "😀  hello <@22>" {
		t.Fatal(text, ok)
	}
	m.Mentions = nil
	if _, ok = MentionText(m, "11"); ok {
		t.Fatal("text-only mention")
	}
	u, e := InstallationURL("12")
	if e != nil || !strings.Contains(u, "permissions=68608") || strings.Contains(u, "identify") {
		t.Fatal(u, e)
	}
	for _, raw := range []string{"ws://gateway.discord.gg/", "wss://gateway.discord.gg.evil/", "wss://evil.discord.gg/", "wss://gateway.discord.gg:443/", "wss://user@gateway.discord.gg/", "wss://gateway.discord.gg/path", "wss://gateway.discord.gg/?token=secret"} {
		if validGatewayURL(raw) {
			t.Fatal(raw)
		}
	}
	if !validGatewayURL("wss://gateway-us-east1.discord.gg/") {
		t.Fatal("resume host")
	}
}

func TestMessageAndRateFactsAreBounded(t *testing.T) {
	for _, key := range []string{"type", "content", "timestamp"} {
		t.Run("missing_"+key, func(t *testing.T) {
			v := messageJSON("33", "22", "55", "hello", false)
			delete(v, key)
			raw, _ := json.Marshal(v)
			if _, e := DecodeMessage(raw); !errors.Is(e, ErrResponse) {
				t.Fatal(e)
			}
		})
	}
	m := Message{ID: "33", ChannelID: "22", Author: User{ID: "55"}, Content: "hello"}
	if !OrdinaryMessage(m, Channel{ID: "22", Type: 1}) {
		t.Fatal("independent DM IDs rejected")
	}
	guild := ID("66")
	m.GuildID = &guild
	if OrdinaryMessage(m, Channel{ID: "22", Type: 1}) {
		t.Fatal("guild in DM accepted")
	}
	for _, text := range []string{strings.Repeat("😀", 1001), " ", string([]byte{0xff})} {
		c := NewClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Error("invalid text reached transport")
			return nil, ErrInvalid
		}))
		in := intent()
		in.Content = text
		if _, e := c.CreateMessage(context.Background(), testToken, in); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Bucket", "fixture_bucket")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset-After", "0.0000000001")
		jsonResponse(w, map[string]any{"id": "11", "bot": true, "username": "Bot"})
	})
	var observed RateLimit
	ctx := WithRateObserver(context.Background(), func(v RateLimit) { observed = v })
	if _, e := c.GetMe(ctx, testToken); e != nil {
		t.Fatal(e)
	}
	if observed.Bucket != "fixture_bucket" || observed.Remaining != 0 || observed.ResetAfter != time.Nanosecond || observed.Route != "GET /users/@me" {
		t.Fatal(observed)
	}
}
