package teams

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"openfox/internal/platform/safehttp"
)

func testActivity() Activity {
	r := testRoute()
	a := Activity{Type: "message", ID: r.ActivityID, Timestamp: time.Now().Format(time.RFC3339Nano), ServiceURL: r.ServiceURL, ChannelID: "msteams", From: Account{ID: r.FromID, AADObjectID: testAAD}, Recipient: Account{ID: r.RecipientID}, Text: "controlled text"}
	a.Conversation.ID = r.ConversationID
	a.Conversation.Type = "personal"
	a.ChannelData.Tenant.ID = testTenant
	return a
}

func TestTeamsAuthenticatedActivityRoutingAndMentions(t *testing.T) {
	p := newPeer(t)
	ctx := context.Background()
	token := p.jwt(t, claims(time.Now()), nil)
	a := testActivity()
	a.Conversation.ID = strings.Repeat("opaque/", 40)
	data, _ := json.Marshal(a)
	got, err := p.client.AuthenticateActivity(ctx, token, data, testTenant, testApp)
	if err != nil || !got.Supported || got.Route.ConversationID != a.Conversation.ID || len(got.Route.ChatKey()) > 128 {
		t.Fatal("opaque personal route", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*Activity)
		want   error
	}{
		{"tenant", func(a *Activity) { a.ChannelData.Tenant.ID = testApp }, ErrUnauthorized},
		{"conversation_tenant", func(a *Activity) { a.Conversation.TenantID = testApp }, ErrUnauthorized},
		{"recipient", func(a *Activity) { a.Recipient.ID = "28:" + testTenant }, ErrUnauthorized},
		{"channel", func(a *Activity) { a.ChannelID = "emulator" }, ErrUnauthorized},
		{"preview_thread", func(a *Activity) { a.ChannelData.ReplyChain = json.RawMessage(`{"id":"new"}`) }, ErrUnsupported},
		{"other_cloud", func(a *Activity) { a.ServiceURL = "https://smba.infra.gcc.teams.microsoft.com/teams" }, ErrUnauthorized},
		{"agentic_sender", func(a *Activity) { a.From.AgenticUserID = testAAD }, ErrUnsupported},
		{"agentic_recipient", func(a *Activity) { a.Recipient.AgenticAppBlueprintID = testApp }, ErrUnsupported},
		{"bot_sender", func(a *Activity) { a.From.Role = "bot" }, ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := a
			tc.mutate(&v)
			raw, _ := json.Marshal(v)
			_, err := p.client.AuthenticateActivity(ctx, token, raw, testTenant, testApp)
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
	a.Conversation.Type = "groupChat"
	a.Text = "<at>Bot</at> controlled group"
	data, _ = json.Marshal(a)
	got, err = p.client.AuthenticateActivity(ctx, token, data, testTenant, testApp)
	if err != nil || got.Supported || got.Mentioned {
		t.Fatal("text alone is not mention", err)
	}
	a.Entities = append(a.Entities, struct {
		Type      string  `json:"type"`
		Text      string  `json:"text"`
		Mentioned Account `json:"mentioned"`
	}{Type: "mention", Text: "<at>Bot</at>", Mentioned: Account{ID: a.Recipient.ID}})
	data, _ = json.Marshal(a)
	got, err = p.client.AuthenticateActivity(ctx, token, data, testTenant, testApp)
	if err != nil || !got.Supported || got.Text != "controlled group" {
		t.Fatal("verified mention", err)
	}
	a.Conversation.Type = "channel"
	a.ChannelData.Team.ID = "19:team/@opaque"
	a.ChannelData.Channel.ID = "19:channel/@opaque"
	data, _ = json.Marshal(a)
	first, err := p.client.AuthenticateActivity(ctx, token, data, testTenant, testApp)
	if err != nil {
		t.Fatal(err)
	}
	a.Conversation.ID = "second opaque thread"
	data, _ = json.Marshal(a)
	second, err := p.client.AuthenticateActivity(ctx, token, data, testTenant, testApp)
	if err != nil || first.Route.ScopeKey() != second.Route.ScopeKey() || first.Route.ChatKey() == second.Route.ChatKey() {
		t.Fatal("channel scope and thread mixed", err)
	}
}

func TestTeamsTLSAndDNSFailClosed(t *testing.T) {
	for _, mode := range []string{"untrusted_ca", "wrong_hostname"} {
		t.Run(mode, func(t *testing.T) {
			p := newPeer(t)
			transport := p.client.http.Transport.(*http.Transport).Clone()
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			if mode == "untrusted_ca" {
				transport.TLSClientConfig.RootCAs = x509.NewCertPool()
			} else {
				transport.TLSClientConfig.ServerName = "wrong.invalid"
			}
			c := NewClientWithHTTP(&http.Client{Transport: transport})
			t.Cleanup(c.CloseIdleConnections)
			_, err := c.Token(context.Background(), Credentials{TenantID: testTenant, AppID: testApp, Secret: "controlled-secret"}, ConnectorScope)
			var failure *Error
			if !errors.As(err, &failure) || failure.Unknown || strings.Contains(err.Error(), "controlled-secret") {
				t.Fatalf("TLS failure classification: %v", err)
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if len(p.counts) != 0 {
				t.Fatal("untrusted TLS reached origin")
			}
		})
	}
	c := NewClientWithHTTP(safehttp.Client(safehttp.Options{LookupIP: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }}))
	defer c.CloseIdleConnections()
	_, err := c.Token(context.Background(), Credentials{TenantID: testTenant, AppID: testApp, Secret: "controlled-secret"}, ConnectorScope)
	var failure *Error
	if !errors.As(err, &failure) || failure.Unknown {
		t.Fatal("private DNS accepted", err)
	}
}

func TestTeamsXMLBindingTextStaysText(t *testing.T) {
	p := newPeer(t)
	a := testActivity()
	a.TextFormat = "xml"
	a.Text = "<p>/openfox bind <b>controlled-code</b></p>"
	data, _ := json.Marshal(a)
	v, err := p.client.AuthenticateActivity(context.Background(), p.jwt(t, claims(time.Now()), nil), data, testTenant, testApp)
	if err != nil || v.Text != "/openfox bind controlled-code" {
		t.Fatalf("XML text %q %v", v.Text, err)
	}
	a.Text = "<!DOCTYPE x SYSTEM 'https://example.invalid/secret'><p>unsafe</p>"
	data, _ = json.Marshal(a)
	if _, err = p.client.AuthenticateActivity(context.Background(), p.jwt(t, claims(time.Now()), nil), data, testTenant, testApp); !errors.Is(err, ErrUnsupported) {
		t.Fatal("directive accepted", err)
	}
}
