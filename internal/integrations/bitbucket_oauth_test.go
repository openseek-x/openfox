package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/integrations/bitbucket"
	"openfox/internal/secrets"
)

const bbSubject = "11111111-1111-4111-8111-111111111111"
const bbWorkspace = "22222222-2222-4222-8222-222222222222"
const bbSubjectOther = "33333333-3333-4333-8333-333333333333"

type bbOAuthFake struct {
	mu                                         sync.Mutex
	subject, scope, refreshMode                string
	admin                                      bool
	count, refreshes, identity, workspaceReads int
	entered                                    chan struct{}
	release                                    chan struct{}
}

func bbFixture(t *testing.T) (Service, *bbOAuthFake) {
	t.Helper()
	f := &bbOAuthFake{subject: bbSubject, admin: true}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.count++
		subject, scope, mode, admin := f.subject, f.scope, f.refreshMode, f.admin
		entered, release := f.entered, f.release
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		reply := func(v any) { json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/site/oauth2/access_token":
			if r.Header.Get("X-Fixture-Origin") != "https://bitbucket.org" {
				t.Error("token origin")
			}
			r.ParseForm()
			consumer, secret, ok := r.BasicAuth()
			if !ok || secret != "synthetic-secret" || (consumer != "org-consumer" && consumer != "personal-consumer") {
				t.Error("consumer")
			}
			if scope == "" {
				scope = "account pullrequest repository"
				if consumer == "personal-consumer" {
					scope = "account"
				}
			}
			token := "synthetic-access"
			refresh := "synthetic-refresh"
			if r.Form.Get("grant_type") == "refresh_token" {
				f.mu.Lock()
				f.refreshes++
				f.mu.Unlock()
				if entered != nil {
					select {
					case entered <- struct{}{}:
					default:
					}
				}
				if release != nil {
					<-release
				}
				if mode == "429" {
					w.Header().Set("Retry-After", "2")
					w.WriteHeader(429)
					return
				}
				if mode == "invalid" {
					w.WriteHeader(400)
					reply(map[string]any{"error": "invalid_grant"})
					return
				}
				if mode == "unknown" {
					c, _, _ := w.(http.Hijacker).Hijack()
					c.Close()
					return
				}
				token, refresh = "synthetic-renewed", "synthetic-rotated"
			}
			reply(map[string]any{"access_token": token, "refresh_token": refresh, "token_type": "bearer", "scopes": scope, "expires_in": 3600})
		case "/2.0/user":
			if r.Header.Get("X-Fixture-Origin") != "https://api.bitbucket.org" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer synthetic-") {
				t.Error("identity authorization")
			}
			f.mu.Lock()
			f.identity++
			f.mu.Unlock()
			reply(bitbucket.User{UUID: "{" + subject + "}", DisplayName: "Verified identity"})
		case "/2.0/user/workspaces":
			f.mu.Lock()
			f.workspaceReads++
			f.mu.Unlock()
			reply(map[string]any{"values": []any{map[string]any{"administrator": admin, "workspace": bitbucket.Workspace{UUID: "{" + bbWorkspace + "}", Slug: "controlled", Name: "Controlled"}}}})
		case "/2.0/workspaces/{" + bbWorkspace + "}":
			reply(bitbucket.Workspace{UUID: "{" + bbWorkspace + "}", Slug: "controlled", Name: "Controlled"})
		default:
			t.Errorf("unexpected fixture path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	tr := azureOAuthTestTransport{u, server.Client().Transport}
	box, e := secrets.New(bytes.Repeat([]byte{41}, secrets.KeySize))
	if e != nil {
		t.Fatal(e)
	}
	return Service{Box: box, PublicURL: "https://openfox.invalid", Bitbucket: &bitbucket.Client{App: bitbucket.App{ClientID: "org-consumer", ClientSecret: "synthetic-secret"}, Transport: tr}, BitbucketIdentity: &bitbucket.Client{App: bitbucket.App{ClientID: "personal-consumer", ClientSecret: "synthetic-secret"}, Transport: tr}}, f
}
func bbCallback(s Service, personal bool) BitbucketCallbackTarget {
	purpose := PurposeOrganization
	if personal {
		purpose = PurposePersonal
	}
	return BitbucketCallbackTarget{proof: bitbucketProof{Purpose: purpose, AppID: s.bitbucketClient(personal).App.ClientID, ConsumerFingerprint: bitbucketConsumerFingerprint(s.bitbucketClient(personal))}}
}
func TestBitbucketOAuthPersonalNoSecretsAndStrictScopes(t *testing.T) {
	s, f := bbFixture(t)
	out, e := s.VerifyBitbucketCallback(context.Background(), bbCallback(s, true), "synthetic-code")
	if e != nil || out.identity.UUID != bbSubject || out.token.AccessToken != "" || out.token.RefreshToken != "" || len(out.workspaces) != 0 {
		t.Fatal("personal verification", e)
	}
	f.mu.Lock()
	calls, workspace := f.count, f.workspaceReads
	f.scope = "account pullrequest"
	f.mu.Unlock()
	if calls != 2 || workspace != 0 {
		t.Fatal("personal used organization APIs")
	}
	_, e = s.VerifyBitbucketCallback(context.Background(), bbCallback(s, true), "synthetic-code")
	if !errors.Is(e, bitbucket.ErrScope) {
		t.Fatal(e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.count != 3 || f.identity != 1 {
		t.Fatal("broad scope reached identity read")
	}
}
func TestBitbucketOAuthOrganizationAdminAndEnvelopeBounds(t *testing.T) {
	s, f := bbFixture(t)
	out, e := s.VerifyBitbucketCallback(context.Background(), bbCallback(s, false), "synthetic-code")
	if e != nil || len(out.workspaces) != 1 {
		t.Fatal(e)
	}
	target := BitbucketCompleteTarget{proof: bitbucketProof{Purpose: PurposeOrganization, AppID: s.Bitbucket.App.ClientID, ConsumerFingerprint: bitbucketConsumerFingerprint(s.Bitbucket), Identity: &out.identity}, token: out.token, workspace: out.workspaces[0]}
	target, e = s.VerifyBitbucketComplete(context.Background(), target)
	if e != nil || !target.verified {
		t.Fatal(e)
	}
	f.mu.Lock()
	f.admin = false
	f.mu.Unlock()
	target.verified = false
	if _, e = s.VerifyBitbucketComplete(context.Background(), target); !errors.Is(e, ErrForbidden) {
		t.Fatal("lost administrator", e)
	}
	creds := connectionCredentials{AccessToken: strings.Repeat("a", 8192), RefreshToken: strings.Repeat("r", 8192), Scope: "account pullrequest", WebhookToken: "synthetic-hook", WebhookSecret: "synthetic-secret"}
	plain, _ := json.Marshal(creds)
	sealed, _, e := s.sealBitbucket(plain, connectionAAD(bbWorkspace, bbSubject))
	if e != nil || len(sealed) > 32768 || bytes.Contains(sealed, []byte(creds.AccessToken)) {
		t.Fatal("envelope", e)
	}
	got, e := s.openCredentials(bbWorkspace, bbSubject, sealed)
	if e != nil || got != creds {
		t.Fatal("envelope roundtrip", e)
	}
	if _, e = s.open(sealed, connectionAAD(bbSubject, bbWorkspace)); e == nil {
		t.Fatal("cross tenant decrypt")
	}
	limit := 32768 - keyIDLen - secrets.NonceSize - 16
	if _, _, e = s.sealBitbucket(bytes.Repeat([]byte{'a'}, limit), "limit"); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.sealBitbucket(bytes.Repeat([]byte{'a'}, limit+1), "limit"); !errors.Is(e, ErrInvalidRequest) {
		t.Fatal("just over limit", e)
	}
	target.token.ExpiresAt = time.Now().Add(-time.Second)
	if _, e = s.VerifyBitbucketComplete(context.Background(), target); !errors.Is(e, ErrNeedsReauth) {
		t.Fatal(e)
	}
}
func TestBitbucketOAuthConsumerChangeCannotSelectViaQuery(t *testing.T) {
	s, f := bbFixture(t)
	target := bbCallback(s, true)
	s.BitbucketIdentity.App.ClientSecret = "replacement"
	if _, e := s.VerifyBitbucketCallback(context.Background(), target, "synthetic-code"); !errors.Is(e, ErrBitbucketAppChanged) {
		t.Fatal(e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.count != 0 {
		t.Fatal("stale consumer made HTTP")
	}
}

func TestBitbucketOAuthWorkspaceBudgetAndAdministratorFilter(t *testing.T) {
	for _, mode := range []string{"non-admin", "over-100", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := bbFixture(t)
			page := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body any
				switch r.URL.Path {
				case "/site/oauth2/access_token":
					body = map[string]any{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh", "token_type": "bearer", "scopes": "account pullrequest", "expires_in": 3600}
				case "/2.0/user":
					body = bitbucket.User{UUID: bbSubject, DisplayName: "Controlled"}
				case "/2.0/user/workspaces":
					page++
					values := []any{}
					count := 1
					if mode == "over-100" && page == 1 {
						count = 100
					}
					for i := range count {
						id := fmt.Sprintf("%08x-2222-4222-8222-222222222222", page*100+i)
						values = append(values, map[string]any{"administrator": mode != "non-admin", "workspace": bitbucket.Workspace{UUID: id, Slug: "controlled", Name: "Controlled"}})
					}
					body = map[string]any{"values": values}
					if mode != "non-admin" {
						body.(map[string]any)["next"] = "https://api.bitbucket.org/2.0/user/workspaces?cursor=fixed"
					}
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			s.Bitbucket.Transport = azureOAuthTestTransport{u, server.Client().Transport}
			result, e := s.VerifyBitbucketCallback(context.Background(), bbCallback(s, false), "synthetic-code")
			if mode == "non-admin" {
				if e != nil || len(result.workspaces) != 0 {
					t.Fatal(e)
				}
			} else if !errors.Is(e, bitbucket.ErrIncomplete) {
				t.Fatal(e)
			}
			if page > 2 {
				t.Fatal("pagination unbounded", page)
			}
		})
	}
}
