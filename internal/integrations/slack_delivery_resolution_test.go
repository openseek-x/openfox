package integrations

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"openfox/internal/integrations/slack"
)

func TestSlackDeliveryRetryPreservesPlatformDelay(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		want  time.Duration
		retry bool
	}{
		{"normal", &slack.Error{Kind: slack.ErrRateLimited, RetryAfter: 7 * time.Minute}, 7 * time.Minute, true},
		{"day", &slack.Error{Kind: slack.ErrRateLimited, RetryAfter: 24 * time.Hour}, 24 * time.Hour, true},
		{"over_budget", &slack.Error{Kind: slack.ErrRateLimited, RetryAfter: 24*time.Hour + time.Second}, 0, false},
		{"missing_invalid", &slack.Error{Kind: slack.ErrRateLimited}, 0, false},
		{"invalid", slack.ErrInvalid, 0, false}, {"unknown", slack.ErrUnknown, 0, false}, {"revoked", slack.ErrAuth, 0, false},
		{"read_failure", slack.ErrUnavailable, 8 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, retry := slackRetryDelay(tc.err, 3)
			if d != tc.want || retry != tc.retry {
				t.Fatalf("got %v %v", d, retry)
			}
		})
	}
}
func TestSlackDeliveryResolutionReadsOnlyExactReceiptTLS(t *testing.T) {
	d := slackDeliveryRow{ChannelID: "C1", ThreadTS: "1700000000.000100", Marker: "controlled-random-marker", Part: 2, Body: slackDeliveryBody{Text: "hello <&>"}}
	work := SlackDeliveryResolution{action: "verify", remoteTS: "1700000000.000200", delivery: d, app: SlackApp{TeamID: "T1"}, grant: slack.TokenGrant{AccessToken: "synthetic-token"}, target: slackReceiptTarget{AppID: "A1", BotID: "B1", BotUserID: "U1", TeamID: "T1", ChannelID: d.ChannelID, ThreadTS: d.ThreadTS, Marker: d.Marker, Part: d.Part, Body: d.Body}}
	mode := "match"
	calls := 0
	service := Service{SlackTransport: slackTestTLS(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/conversations.replies" || r.Method != http.MethodGet {
			t.Error("unexpected method", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		q := r.URL.Query()
		if q.Get("oldest") != work.remoteTS || q.Get("latest") != work.remoteTS || q.Get("inclusive") != "true" || q.Get("include_all_metadata") != "true" || q.Get("limit") != "15" || q.Get("ts") != d.ThreadTS {
			t.Error("unbounded or incorrect lookup", q)
		}
		if mode == "rate" {
			w.Header().Set("Retry-After", "200")
			w.WriteHeader(429)
			return
		}
		messages := []slack.Message{}
		if mode != "absent" {
			m := slack.Message{Type: "message", TS: work.remoteTS, ThreadTS: d.ThreadTS, AppID: "A1", BotID: "B1", User: "U1", Team: "T1", Text: slack.EscapeText(d.Body.Text), Metadata: slackPartRequest(d.ChannelID, d.ThreadTS, d.Marker, d.Part, d.Body).Metadata}
			if mode == "other" {
				m.BotID = "B2"
			}
			messages = append(messages, m)
		}
		slackTestReply(w, map[string]any{"ok": true, "messages": messages, "has_more": false})
	})}
	for _, m := range []string{"match", "absent", "other", "rate"} {
		mode = m
		got := service.ExecuteSlackDeliveryResolution(context.Background(), work)
		if got.matched != (m == "match") {
			t.Fatal("wrong positive proof", m, got)
		}
		if m == "rate" && got.rateAfter != 200*time.Second {
			t.Fatal("rate delay lost")
		}
	}
	work.action = "skip"
	service.ExecuteSlackDeliveryResolution(context.Background(), work)
	if calls != 4 {
		t.Fatal("skip performed a platform call")
	}
	if errors.Is(slack.ErrUnknown, slack.ErrUnavailable) {
		t.Fatal("unknown must not enter read retry policy")
	}
}
