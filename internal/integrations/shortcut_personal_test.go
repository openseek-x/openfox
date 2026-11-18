package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testShortcutWorkspace = "11111111-1111-4111-8111-111111111111"
const testShortcutUser = "22222222-2222-4222-8222-222222222222"

func shortcutTestGrant(t *testing.T, handler http.HandlerFunc) ShortcutPersonalGrant {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = srv.Certificate().DNSNames[0]
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "api.app.shortcut.com:443" {
			return nil, fmt.Errorf("unexpected target")
		}
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	t.Cleanup(tr.CloseIdleConnections)
	return ShortcutPersonalGrant{CompanyID: testShortcutWorkspace, UserID: testShortcutUser, email: "member@example.test", token: "test-only-secret", transport: tr}
}

func TestShortcutPersonalProtocolRejectsIncompleteProof(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want error
	}{
		{"valid", `{"id":"` + testShortcutUser + `","disabled":false,"state":"full","profile":{"deactivated":false,"email_address":" MEMBER@example.test ","name":"Member"}}`, nil},
		{"missing_disabled", `{"id":"` + testShortcutUser + `","state":"full","profile":{"deactivated":false,"email_address":"member@example.test"}}`, ErrShortcutAuth},
		{"missing_deactivated", `{"id":"` + testShortcutUser + `","disabled":false,"state":"full","profile":{"email_address":"member@example.test"}}`, ErrShortcutAuth},
		{"partial", `{"id":"` + testShortcutUser + `","disabled":false,"state":"partial","profile":{"deactivated":false,"email_address":"member@example.test"}}`, ErrShortcutAuth},
		{"bad_json", `{`, ErrUpstreamUnavailable},
		{"oversized", strings.Repeat(" ", 1<<20) + `{}`, ErrUpstreamUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := shortcutTestGrant(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Shortcut-Token") != "test-only-secret" || r.URL.RawQuery != "" {
					t.Error("wrong request")
				}
				if r.URL.Path == "/api/v3/member" {
					_, _ = fmt.Fprintf(w, `{"id":%q,"workspace2":{"id":%q}}`, testShortcutUser, testShortcutWorkspace)
					return
				}
				_, _ = w.Write([]byte(tc.body))
			})
			out, err := VerifyShortcutPersonal(context.Background(), g)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			if err == nil && !out.verified {
				t.Fatal("proof not verified")
			}
		})
	}
}

func TestShortcutPersonalProtocolRedirectRateAndCancellation(t *testing.T) {
	for _, status := range []int{301, 401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			g := shortcutTestGrant(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "https://other.invalid/token")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("test-only-secret"))
			})
			_, err := VerifyShortcutPersonal(context.Background(), g)
			if err == nil || strings.Contains(err.Error(), g.token) {
				t.Fatalf("secret or success: %v", err)
			}
			if calls != 1 {
				t.Fatal("followed redirect or retried")
			}
		})
	}
	g := shortcutTestGrant(t, func(w http.ResponseWriter, r *http.Request) { t.Error("canceled request reached network") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VerifyShortcutPersonal(ctx, g); err == nil {
		t.Fatal("accepted canceled request")
	}
}

func TestShortcutPersonalCommentContract(t *testing.T) {
	for _, tc := range []struct {
		name, author string
		status       int
		story        int
		valid        bool
	}{{"valid", testShortcutUser, 201, 10, true}, {"wrong_author", testShortcutWorkspace, 201, 10, false}, {"wrong_story", testShortcutUser, 201, 11, false}, {"wrong_status", testShortcutUser, 200, 10, false}} {
		t.Run(tc.name, func(t *testing.T) {
			g := shortcutTestGrant(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v3/stories/10/comments" || r.Header.Get("Shortcut-Token") != "test-only-secret" {
					t.Error("wrong endpoint/token")
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				if len(body) != 1 || body["text"] != "hello" {
					t.Error("spoofed author or unexpected body")
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"id": 123, "story_id": tc.story, "author_id": tc.author})
			})
			id, err := g.CreateComment(context.Background(), testShortcutWorkspace+"/10", "hello")
			if (err == nil) != tc.valid {
				t.Fatalf("id=%s err=%v", id, err)
			}
		})
	}
	g := shortcutTestGrant(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid source reached network") })
	for _, external := range []string{"10", testShortcutUser + "/10", testShortcutWorkspace + "/../10", testShortcutWorkspace + "/010"} {
		if _, err := g.CreateComment(context.Background(), external, "hello"); err == nil {
			t.Fatal("accepted invalid source")
		}
	}
}
