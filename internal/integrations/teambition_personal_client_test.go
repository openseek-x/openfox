package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type personalTransportFunc func(*http.Request) (*http.Response, error)

func (f personalTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTeambitionPersonalIdentityHTTP(t *testing.T) {
	for _, scenario := range []string{"success", "subject", "disabled", "external", "missing-role", "email", "empty", "duplicate", "wrong-member", "401", "403", "429", "500", "redirect", "malformed", "error-message", "oversized", "timeout", "token-type"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") || r.Header.Get("X-Operator-Id") != "" {
					t.Errorf("unexpected method or identity headers")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				result := any(nil)
				switch r.URL.Path {
				case "/api/oauth/userAccessToken":
					if body["code"] != "one-time-code" || body["grantType"] != "authorizationCode" || body["expires"] != float64(600) || len(body) != 3 {
						t.Errorf("exchange contract=%v", body)
					}
					if scenario == "timeout" {
						<-r.Context().Done()
						return
					}
					if scenario == "redirect" {
						w.Header().Set("Location", "https://unexpected.invalid/secret")
						w.WriteHeader(302)
						return
					}
					statuses := map[string]int{"401": 401, "403": 403, "429": 429, "500": 500}
					if status := statuses[scenario]; status > 0 {
						w.WriteHeader(status)
						return
					}
					if scenario == "malformed" {
						_, _ = w.Write([]byte("{bad"))
						return
					}
					if scenario == "oversized" {
						_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
						return
					}
					typ := "Bearer"
					if scenario == "token-type" {
						typ = "Basic"
					}
					result = map[string]any{"accessToken": "transient-user-token", "refreshToken": "never-persist-refresh", "userId": "remote-user", "tokenType": typ, "expires": 600}
				case "/api/oauth/userInfo":
					if len(body) != 1 || body["userAccessToken"] != "transient-user-token" {
						t.Errorf("identity contract=%v", body)
					}
					id := "remote-user"
					if scenario == "subject" {
						id = "other-user"
					}
					result = map[string]string{"userId": id, "name": "Remote Person"}
				case "/api/org/member/batchGet":
					if r.Header.Get("X-Tenant-Id") != "enterprise" || r.Header.Get("X-Tenant-Type") != "organization" || len(body) != 1 {
						t.Errorf("tenant contract=%v", body)
					}
					ids, ok := body["userIds"].([]any)
					if !ok || len(ids) != 1 || ids[0] != "remote-user" {
						t.Errorf("identity list=%v", body)
					}
					member := map[string]any{"userId": "remote-user", "role": 0, "isDisabled": 0, "email": " Person@Example.Test "}
					switch scenario {
					case "disabled":
						member["isDisabled"] = 1
					case "external":
						member["role"] = -1
					case "missing-role":
						delete(member, "role")
					case "email":
						member["email"] = "other@example.test"
					case "wrong-member":
						member["userId"] = "wrong"
					}
					members := []any{member}
					if scenario == "empty" {
						members = []any{}
					}
					if scenario == "duplicate" {
						members = append(members, member)
					}
					result = members
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				failure := ""
				if scenario == "error-message" {
					failure = "upstream secret detail"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 777, "result": result, "errorMessage": failure})
			}))
			defer server.Close()
			upstream, _ := url.Parse(server.URL)
			transport := server.Client().Transport
			client := TeambitionPersonalClient{Transport: personalTransportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme != "https" || r.URL.Host != "open.teambition.com" {
					t.Fatalf("nonofficial origin %s", r.URL.Host)
				}
				c := r.Clone(r.Context())
				u := *r.URL
				c.URL = &u
				c.URL.Scheme = upstream.Scheme
				c.URL.Host = upstream.Host
				return transport.RoundTrip(c)
			})}
			ctx := context.Background()
			if scenario == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			got, err := client.Verify(ctx, ProviderTokenInput{Method: "app", Account: "app", Secret: "test-only-secret", CompanyID: "enterprise"}, "one-time-code", "person@example.test")
			if scenario == "success" {
				if err != nil || got.ID != "remote-user" || got.Email != "person@example.test" || calls != 3 {
					t.Fatalf("identity=%+v calls=%d err=%v", got, calls, err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid identity accepted")
			}
			want := ErrPersonalIdentity
			switch scenario {
			case "401":
				want = ErrNeedsReauth
			case "403":
				want = ErrPersonalCapability
			case "429":
				want = ErrPersonalRateLimited
			case "500", "redirect", "malformed", "error-message", "oversized", "timeout":
				want = ErrUpstreamUnavailable
			}
			if !errors.Is(err, want) {
				t.Fatalf("error=%v want=%v", err, want)
			}
			if strings.Contains(err.Error(), "test-only-secret") || strings.Contains(err.Error(), "transient-user-token") || strings.Contains(err.Error(), "upstream secret detail") {
				t.Fatal("error leaked credential")
			}
			if scenario == "redirect" && calls != 1 {
				t.Fatal("redirect followed")
			}
		})
	}
}
