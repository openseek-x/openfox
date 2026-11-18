package telegram

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var _ API = (*Client)(nil)

const testToken = "123456789:synthetic_ONLY_test_AbCd-12345"
const testBot = `{"id":123456789,"is_bot":true,"first_name":"Test","username":"Current_Bot","can_join_groups":true,"can_read_all_group_messages":false,"has_topics_enabled":false}`
const testUser = `{"id":4503599627370495,"is_bot":false,"first_name":"A"}`
const testChat = `{"id":-4503599627370495,"type":"supergroup","title":"Local"}`
const testMessage = `{"message_id":42,"date":1800000000,"chat":` + testChat + `,"from":` + testUser + `,"text":"hello"}`

func tlsClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server, *http.Transport) {
	t.Helper()
	s := httptest.NewTLSServer(handler)
	t.Cleanup(s.Close)
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "api.telegram.org:443" {
			return nil, errors.New("unexpected external address")
		}
		return (&net.Dialer{}).DialContext(ctx, network, s.Listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	return NewClient(tr), s, tr
}
func result(w http.ResponseWriter, value string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"ok":true,"result":`+value+`}`)
}
func bodyMap(t *testing.T, r *http.Request) map[string]json.RawMessage {
	t.Helper()
	data, e := io.ReadAll(r.Body)
	if e != nil {
		t.Error(e)
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		t.Errorf("bad body")
	}
	return m
}
func sendInput() SendMessageInput {
	return SendMessageInput{ChatID: ID(-MaxID), ReplyToMessageID: 42, Text: "<plain> & 😀"}
}
func sentMessage() string {
	return `{"message_id":43,"date":1800000001,"chat":` + testChat + `,"from":` + testBot + `,"text":"<plain> & 😀","reply_to_message":` + testMessage + `}`
}
func TestBotMethodsUseFixedOriginAndTypedFacts(t *testing.T) {
	var calls atomic.Int32
	c, _, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Host != "api.telegram.org" || r.Method != "POST" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("request contract")
		}
		m := bodyMap(t, r)
		switch r.URL.Path {
		case "/bot" + testToken + "/getMe":
			if len(m) != 0 {
				t.Error("getMe body")
			}
			result(w, testBot)
		case "/bot" + testToken + "/getWebhookInfo":
			result(w, `{"url":"https://callback.example/hooks/secret","has_custom_certificate":false,"pending_update_count":2,"last_error_date":1800000000,"last_error_message":"`+testToken+`","max_connections":10,"allowed_updates":["message","my_chat_member"]}`)
		case "/bot" + testToken + "/setWebhook":
			if len(m) != 5 || string(m["drop_pending_updates"]) != "false" || string(m["allowed_updates"]) != `["message","my_chat_member"]` || string(m["max_connections"]) != "10" {
				t.Error("setWebhook contract")
			}
			result(w, "true")
		case "/bot" + testToken + "/deleteWebhook":
			if len(m) != 1 || string(m["drop_pending_updates"]) != "false" {
				t.Error("delete contract")
			}
			result(w, "true")
		case "/bot" + testToken + "/getChat":
			if string(m["chat_id"]) != "-4503599627370495" {
				t.Error("chat integer")
			}
			result(w, testChat)
		case "/bot" + testToken + "/getChatMember":
			if string(m["user_id"]) != "4503599627370495" {
				t.Error("member integer")
			}
			result(w, `{"status":"member","user":`+testUser+`}`)
		case "/bot" + testToken + "/sendMessage":
			if len(m) != 4 || string(m["text"]) != `"\u003cplain\u003e \u0026 😀"` || string(m["link_preview_options"]) != `{"is_disabled":true}` || string(m["reply_parameters"]) != `{"message_id":42,"allow_sending_without_reply":false}` {
				t.Error("send contract", string(m["text"]))
			}
			result(w, sentMessage())
		default:
			t.Error("unexpected path")
			w.WriteHeader(500)
		}
	})
	ctx := context.Background()
	b, e := c.GetMe(ctx, testToken)
	if e != nil || b.ID != 123456789 || b.Username != "Current_Bot" || b.CanJoinGroups == nil || !*b.CanJoinGroups || b.HasTopicsEnabled == nil || *b.HasTopicsEnabled {
		t.Fatalf("bot %+v %v", b, e)
	}
	hook, e := c.GetWebhookInfo(ctx, testToken)
	if e != nil || hook.PendingUpdateCount != 2 || !hook.LastErrorPresent || hook.URL != "https://callback.example/hooks/secret" {
		t.Fatalf("hook %v %v", hook, e)
	}
	for _, out := range []string{fmt.Sprint(hook), fmt.Sprintf("%#v", hook), func() string { x, _ := json.Marshal(hook); return string(x) }()} {
		if strings.Contains(out, "callback.example") || strings.Contains(out, testToken) {
			t.Fatal("webhook leak")
		}
	}
	if e = c.SetWebhook(ctx, testToken, WebhookInput{URL: hook.URL, Secret: "webhook_Synthetic-123", MaxConnections: 10}); e != nil {
		t.Fatal(e)
	}
	if e = c.DeleteWebhook(ctx, testToken); e != nil {
		t.Fatal(e)
	}
	if chat, e := c.GetChat(ctx, testToken, ID(-MaxID)); e != nil || chat.ID != ID(-MaxID) {
		t.Fatalf("chat %v", e)
	}
	if member, e := c.GetChatMember(ctx, testToken, ID(-MaxID), ID(MaxID)); e != nil || member.User.ID != ID(MaxID) {
		t.Fatalf("member %v", e)
	}
	if receipt, e := c.SendMessage(ctx, testToken, sendInput()); e != nil || receipt.MessageID != 43 || receipt.ReplyToMessageID != 42 {
		t.Fatalf("receipt %v %v", receipt, e)
	}
	if calls.Load() != 7 {
		t.Fatalf("calls %d", calls.Load())
	}
}
func TestErrorsAreSafeAndWritesAreNeverRetried(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		kind   error
	}{
		{"400 explicit", 400, `{"ok":false,"error_code":400,"description":"` + testToken + `","parameters":{"migrate_to_chat_id":-100200300400}}`, ErrRejected},
		{"200 rejected", 200, `{"ok":false,"error_code":400,"description":"invalid target"}`, ErrRejected},
		{"401", 401, `<token>` + testToken, ErrAuth}, {"403", 403, `{"ok":false}`, ErrForbidden},
		{"429", 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":9}}`, ErrRateLimited},
		{"body429", 200, `{"ok":false,"error_code":429,"parameters":{"retry_after":900000}}`, ErrRateLimited},
		{"500", 500, `{"ok":false,"error_code":500,"description":"` + testToken + `"}`, ErrUnknown},
		{"500 with rate body", 500, `{"ok":false,"error_code":429,"parameters":{"retry_after":1}}`, ErrUnknown},
		{"201 with rate body", 201, `{"ok":false,"error_code":429,"parameters":{"retry_after":1}}`, ErrUnknown},
		{"odd success", 201, `{"ok":true,"result":true}`, ErrUnknown},
		{"missing ok", 200, `{"result":true}`, ErrUnknown}, {"null result", 200, `{"ok":true,"result":null}`, ErrUnknown},
		{"duplicate", 200, `{"ok":false,"ok":true,"result":true}`, ErrUnknown},
		{"unknown code", 200, `{"ok":false,"error_code":123,"description":"` + testToken + `"}`, ErrUnknown},
		{"false success", 200, `{"ok":true,"result":false}`, ErrUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c, _, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			e := c.DeleteWebhook(context.Background(), testToken)
			if !errors.Is(e, tc.kind) {
				t.Fatalf("got %v want %v", e, tc.kind)
			}
			if calls.Load() != 1 {
				t.Fatal("retried")
			}
			assertSafeError(t, e)
			if tc.kind == ErrRateLimited {
				var api *Error
				if !errors.As(e, &api) || api.RetryAfter < time.Second || api.RetryAfter > 900000*time.Second {
					t.Fatal("retry bound")
				}
			}
			if tc.name == "400 explicit" {
				var api *Error
				errors.As(e, &api)
				if api.MigrateToChatID != -100200300400 {
					t.Fatal("migration fact")
				}
			}
		})
	}
}
func assertSafeError(t *testing.T, e error) {
	t.Helper()
	for _, s := range []string{fmt.Sprint(e), fmt.Sprintf("%+v", e), fmt.Sprintf("%#v", e), func() string { x, _ := json.Marshal(e); return string(x) }()} {
		if strings.Contains(s, testToken) || strings.Contains(s, url.QueryEscape(testToken)) || strings.Contains(s, "/bot") || strings.Contains(s, "evil.example") {
			t.Fatalf("unsafe error")
		}
	}
}
func TestTLSRedirectCancellationAndUnknown(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		var n atomic.Int32
		c, _, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			w.Header().Set("Location", "https://evil.example/bot"+testToken)
			w.WriteHeader(302)
		})
		_, e := c.SendMessage(context.Background(), testToken, sendInput())
		if !errors.Is(e, ErrUnknown) || n.Load() != 1 {
			t.Fatalf("%v calls%d", e, n.Load())
		}
		assertSafeError(t, e)
	})
	t.Run("unknown CA", func(t *testing.T) {
		c, _, tr := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS reached handler") })
		tr.TLSClientConfig = tr.TLSClientConfig.Clone()
		tr.TLSClientConfig.RootCAs = x509.NewCertPool()
		_, e := c.GetMe(context.Background(), testToken)
		if !errors.Is(e, ErrUnavailable) {
			t.Fatal(e)
		}
		assertSafeError(t, e)
	})
	t.Run("wrong hostname", func(t *testing.T) {
		c, _, tr := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("wrong TLS reached handler") })
		tr.TLSClientConfig = tr.TLSClientConfig.Clone()
		tr.TLSClientConfig.ServerName = "wrong.example"
		_, e := c.GetMe(context.Background(), testToken)
		if !errors.Is(e, ErrUnavailable) {
			t.Fatal(e)
		}
		assertSafeError(t, e)
	})
	t.Run("cancel before", func(t *testing.T) {
		var n atomic.Int32
		c, _, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1) })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, e := c.SendMessage(ctx, testToken, sendInput())
		if !errors.Is(e, context.Canceled) || n.Load() != 0 {
			t.Fatal(e)
		}
	})
	t.Run("cancel after write", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var n atomic.Int32
		c, _, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			io.Copy(io.Discard, r.Body)
			close(entered)
			<-release
			result(w, sentMessage())
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, e := c.SendMessage(ctx, testToken, sendInput()); done <- e }()
		<-entered
		cancel()
		e := <-done
		close(release)
		if !errors.Is(e, ErrUnknown) || n.Load() != 1 {
			t.Fatal(e)
		}
		assertSafeError(t, e)
	})
	t.Run("drop response", func(t *testing.T) {
		var n atomic.Int32
		c, _, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			io.Copy(io.Discard, r.Body)
			conn, _, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			conn.Close()
		})
		_, e := c.SendMessage(context.Background(), testToken, sendInput())
		if !errors.Is(e, ErrUnknown) || n.Load() != 1 {
			t.Fatal(e)
		}
		assertSafeError(t, e)
	})
	t.Run("oversize", func(t *testing.T) {
		c, _, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", MaxBodyBytes+1)) })
		_, e := c.SendMessage(context.Background(), testToken, sendInput())
		if !errors.Is(e, ErrUnknown) {
			t.Fatal(e)
		}
	})
	t.Run("transport reflection", func(t *testing.T) {
		c := NewClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("request %s reflected %s", r.URL, testToken)
		}))
		_, e := c.GetMe(context.Background(), testToken)
		if !errors.Is(e, ErrUnavailable) {
			t.Fatal(e)
		}
		assertSafeError(t, e)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestMalformedFactsAndWrongTargetAreNotSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, method, body string
		call               func(*Client) error
	}{
		{"missing is_bot", "getMe", `{"id":123,"first_name":"A","username":"A_Bot"}`, func(c *Client) error { _, e := c.GetMe(context.Background(), testToken); return e }},
		{"human bot", "getMe", testUser, func(c *Client) error { _, e := c.GetMe(context.Background(), testToken); return e }},
		{"wrong member", "getChatMember", `{"status":"member","user":` + testBot + `}`, func(c *Client) error {
			_, e := c.GetChatMember(context.Background(), testToken, -1, ID(MaxID))
			return e
		}},
		{"restricted missing facts", "getChatMember", `{"status":"restricted","user":` + testUser + `}`, func(c *Client) error {
			_, e := c.GetChatMember(context.Background(), testToken, -1, ID(MaxID))
			return e
		}},
		{"wrong chat", "getChat", `{"id":-1,"type":"group"}`, func(c *Client) error { _, e := c.GetChat(context.Background(), testToken, -2); return e }},
		{"null webhook", "getWebhookInfo", `null`, func(c *Client) error { _, e := c.GetWebhookInfo(context.Background(), testToken); return e }},
		{"missing webhook required", "getWebhookInfo", `{"url":""}`, func(c *Client) error { _, e := c.GetWebhookInfo(context.Background(), testToken); return e }},
		{"reflected token URL", "getWebhookInfo", `{"url":"https://callback.example/` + url.QueryEscape(testToken) + `","has_custom_certificate":false,"pending_update_count":0}`, func(c *Client) error { _, e := c.GetWebhookInfo(context.Background(), testToken); return e }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { result(w, tc.body) })
			e := tc.call(c)
			if !errors.Is(e, ErrIncomplete) {
				t.Fatalf("got%v", e)
			}
			assertSafeError(t, e)
		})
	}
	for name, body := range map[string]string{
		"wrong chat":     strings.Replace(sentMessage(), `"id":-4503599627370495`, `"id":-7`, 1),
		"wrong reply":    strings.Replace(sentMessage(), `"message_id":42`, `"message_id":99`, 1),
		"missing reply":  `{"message_id":43,"date":1800000001,"chat":` + testChat + `,"from":` + testBot + `,"text":"<plain> & 😀"}`,
		"topic":          strings.Replace(sentMessage(), `"message_id":43`, `"message_id":43,"message_thread_id":1`, 1),
		"wrong text":     strings.Replace(sentMessage(), `<plain> & 😀`, `changed`, 1),
		"scheduled zero": strings.Replace(sentMessage(), `"message_id":43`, `"message_id":0`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			c, _, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { result(w, body) })
			_, e := c.SendMessage(context.Background(), testToken, sendInput())
			if !errors.Is(e, ErrUnknown) {
				t.Fatal(e)
			}
		})
	}
}
func TestLocalInputsNeverReachTransport(t *testing.T) {
	var n atomic.Int32
	c := NewClient(roundTripFunc(func(r *http.Request) (*http.Response, error) { n.Add(1); return nil, errors.New("must not call") }))
	ctx := context.Background()
	for _, token := range []string{"", testToken + "/x", testToken + "?x=1", testToken + "\r\nX:x", "123:%2f", "https://evil.example", "123:abc def"} {
		if _, e := c.GetMe(ctx, token); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	for _, in := range []WebhookInput{{"http://callback.example/x", "good", 10}, {"https://user@callback.example/x", "good", 10}, {"https://callback.example:123/x", "good", 10}, {"https://callback.example/x?secret=x", "good", 10}, {"https://callback.example/x", "bad!", 10}, {"https://callback.example/x", "good", 0}, {"https://callback.example/x", "good", 101}} {
		if e := c.SetWebhook(ctx, testToken, in); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	for _, in := range []SendMessageInput{{0, 1, "x"}, {1, 0, "x"}, {1, 1, ""}, {1, 1, strings.Repeat("😀", 2049)}, {1, 1, "nul\x00"}, {ID(MaxID + 1), 1, "x"}} {
		if _, e := c.SendMessage(ctx, testToken, in); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	if n.Load() != 0 {
		t.Fatalf("local invalid inputs sent %d", n.Load())
	}
}

func TestExactWireTextLimitAndReadDeadline(t *testing.T) {
	t.Run("4096 UTF16 send", func(t *testing.T) {
		text := strings.Repeat("😀", 2048)
		quoted, _ := json.Marshal(text)
		var n atomic.Int32
		c, _, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			bodyMap(t, r)
			result(w, strings.Replace(sentMessage(), `"<plain> & 😀"`, string(quoted), 1))
		})
		in := sendInput()
		in.Text = text
		if _, e := c.SendMessage(context.Background(), testToken, in); e != nil || n.Load() != 1 {
			t.Fatal(e)
		}
	})
	t.Run("bounded deadline after headers", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		c, _, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true,"result":`)
			w.(http.Flusher).Flush()
			close(entered)
			<-release
		})
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() { _, e := c.SendMessage(ctx, testToken, sendInput()); done <- e }()
		select {
		case <-entered:
		case e := <-done:
			close(release)
			t.Fatalf("request did not reach the controlled response: %v", e)
		}
		e := <-done
		close(release)
		if !errors.Is(e, ErrUnknown) {
			t.Fatal(e)
		}
		assertSafeError(t, e)
	})
}

func TestRetryDelaysPreserveProviderAdvice(t *testing.T) {
	for _, tc := range []struct {
		name, body, header string
		delay              time.Duration
		invalid            bool
	}{
		{"body long", `{"retry_after":900000}`, "", 900000 * time.Second, false},
		{"header long", `{}`, "900000", 900000 * time.Second, false},
		{"duration overflow", `{"retry_after":9223372037}`, "", 0, true},
		{"integer overflow", `{"retry_after":999999999999999999999999999}`, "", 0, true},
		{"HTTP date", `{}`, "Wed, 21 Oct 2015 07:28:00 GMT", 0, true},
		{"fraction", `{"retry_after":1.5}`, "", 0, true},
		{"null", `{"retry_after":null}`, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			c, _, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(429)
				fmt.Fprint(w, `{"ok":false,"error_code":429,"parameters":`+tc.body+`}`)
			})
			_, e := c.SendMessage(context.Background(), testToken, sendInput())
			var api *Error
			if !errors.As(e, &api) {
				t.Fatal(e)
			}
			if tc.invalid {
				if !errors.Is(e, ErrRejected) || api.Code != "retry_after_invalid" || api.RetryAfter != 0 {
					t.Fatalf("unsafe retry %v %v", e, api.RetryAfter)
				}
			} else if !errors.Is(e, ErrRateLimited) || api.RetryAfter != tc.delay {
				t.Fatalf("delay %v want%v", api.RetryAfter, tc.delay)
			}
			if n.Load() != 1 {
				t.Fatal("retried")
			}
		})
	}
}
