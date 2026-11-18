package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/integrations/azuredevops"
	"openfox/internal/membership"
	"openfox/internal/secrets"
)

const azureTestTenant = "11111111-1111-4111-8111-111111111111"
const azureTestApp = "22222222-2222-4222-8222-222222222222"
const azureTestIdentityApp = "33333333-3333-4333-8333-333333333333"
const azureTestSubject = "44444444-4444-4444-8444-444444444444"
const azureTestAccount = "55555555-5555-4555-8555-555555555555"
const azureTestScope = "vso.profile vso.code vso.threads_full vso.work vso.identity offline_access"

type azureOAuthTestTransport struct {
	base      *url.URL
	transport http.RoundTripper
}

func (tr azureOAuthTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	u := *r.URL
	c.URL = &u
	c.Header.Set("X-Fixture-Origin", u.Scheme+"://"+u.Host)
	c.URL.Scheme, c.URL.Host, c.Host = tr.base.Scheme, tr.base.Host, tr.base.Host
	return tr.transport.RoundTrip(c)
}
func azureOAuthFixture(t *testing.T, handler http.HandlerFunc) Service {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	tr := azureOAuthTestTransport{u, server.Client().Transport}
	box, err := secrets.New(bytes.Repeat([]byte{31}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return Service{
		Box: box, PublicURL: "http://127.0.0.1:18149",
		Azure:         &azuredevops.Client{App: azuredevops.App{ClientID: azureTestApp, ClientSecret: "fixture-organization-secret"}, Transport: tr},
		AzureIdentity: &azuredevops.Client{App: azuredevops.App{ClientID: azureTestIdentityApp, ClientSecret: "fixture-identity-secret"}, Transport: tr},
	}
}
func azureOAuthReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func azureTestCallback(personal bool) AzureCallbackTarget {
	p := azureProof{Purpose: PurposeOrganization, AppID: azureTestApp, TenantID: azureTestTenant}
	if personal {
		p.Purpose, p.AppID = PurposePersonal, azureTestIdentityApp
		p.ParentID, p.AccountID, p.OrganizationName = azureTestApp, azureTestAccount, "fixture-org"
		p.ParentVersion, p.GrantGeneration = 1, 1
	}
	return AzureCallbackTarget{proof: p, verifier: strings.Repeat("v", 43)}
}
func azureTestComplete() AzureCompleteTarget {
	return AzureCompleteTarget{
		proof:   azureProof{Purpose: PurposeOrganization, AppID: azureTestApp, TenantID: azureTestTenant, SubjectID: azureTestSubject},
		account: AzureAccount{ID: azureTestAccount, Name: "fixture-org"},
		token:   azuredevops.Token{AccessToken: "fixture-access", RefreshToken: "fixture-refresh", Scope: azureTestScope, ExpiresAt: time.Now().Add(time.Hour)},
	}
}
func TestAzureOAuthOrganizationHTTPAndLongTokenEnvelopes(t *testing.T) {
	var calls atomic.Int32
	access, refresh := strings.Repeat("a", 32<<10), strings.Repeat("r", 32<<10)
	s := azureOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/" + azureTestTenant + "/oauth2/v2.0/token":
			if r.Header.Get("X-Fixture-Origin") != "https://login.microsoftonline.com" || r.Method != "POST" {
				t.Error("wrong token endpoint")
			}
			_ = r.ParseForm()
			if r.Form.Get("client_id") != azureTestApp || r.Form.Get("client_secret") != "fixture-organization-secret" || r.Form.Get("code_verifier") != strings.Repeat("v", 43) || r.Form.Get("scope") != azuredevops.ResourceScope+" offline_access" || r.Form.Get("redirect_uri") != "http://127.0.0.1:18149/api/v1/integrations/azure_devops/callback" {
				t.Error("wrong confidential PKCE form")
			}
			azureOAuthReply(w, map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": 3600, "scope": azureTestScope})
		case "/_apis/profile/profiles/me":
			if r.Header.Get("Authorization") != "Bearer "+access {
				t.Error("wrong profile token")
			}
			azureOAuthReply(w, map[string]any{"id": azureTestSubject, "displayName": "委托人", "emailAddress": "display-only@example.test"})
		case "/_apis/accounts":
			if r.URL.Query().Get("memberId") != azureTestSubject || r.Header.Get("X-Fixture-Origin") != "https://app.vssps.visualstudio.com" {
				t.Error("not authenticated subject")
			}
			azureOAuthReply(w, map[string]any{"value": []any{map[string]any{"accountId": azureTestAccount, "accountName": "fixture-org", "accountUri": "https://untrusted.invalid/ignored"}}})
		case "/fixture-org/_apis/identities":
			if r.URL.Query().Get("identityIds") != azureTestSubject || r.Header.Get("X-Fixture-Origin") != "https://vssps.dev.azure.com" {
				t.Error("wrong identity query")
			}
			azureOAuthReply(w, map[string]any{"value": []any{map[string]any{"id": azureTestSubject, "descriptor": "fixture;self", "isActive": true, "isContainer": false}}})
		case "/fixture-org/_apis/projects":
			if r.URL.Query().Get("$top") != "100" || r.Header.Get("X-Fixture-Origin") != "https://dev.azure.com" {
				t.Error("missing bounded capability probe")
			}
			azureOAuthReply(w, map[string]any{"value": []any{}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	result, err := s.VerifyAzureCallback(context.Background(), AzureRuntime{}, "fixture-org-id", azureTestCallback(false), "fixture-code")
	if err != nil || result.identity.ID != azureTestSubject || len(result.accounts) != 1 || result.token.AccessToken != access {
		t.Fatalf("callback failed: %v", err)
	}
	// Exercise the actual pending and grant formats with maximum protocol tokens.
	// Transaction/CAS behavior is covered separately by the owner's real PG suite.
	plain, _ := json.Marshal(result.token)
	sealed, _, err := s.sealAzure(plain, oauthAAD(azureTestTenant, azureTestApp))
	if err != nil || bytes.Contains(sealed, []byte(access)) {
		t.Fatalf("pending envelope: %v", err)
	}
	opened, err := s.open(sealed, oauthAAD(azureTestTenant, azureTestApp))
	if err != nil {
		t.Fatal(err)
	}
	target := azureTestComplete()
	if err = json.Unmarshal(opened, &target.token); err != nil {
		t.Fatal(err)
	}
	target, err = s.VerifyAzureComplete(context.Background(), target)
	if err != nil || !target.verified || target.identity.Descriptor != "fixture;self" || len(target.accounts) != 1 {
		t.Fatalf("complete verification: %v", err)
	}
	plain, _ = json.Marshal(azureCredentials{AccessToken: target.token.AccessToken, RefreshToken: target.token.RefreshToken})
	sealed, _, err = s.sealAzure(plain, azureGrantAAD(azureTestTenant, azureTestSubject))
	if err != nil {
		t.Fatal(err)
	}
	opened, err = s.open(sealed, azureGrantAAD(azureTestTenant, azureTestSubject))
	var creds azureCredentials
	if err != nil || json.Unmarshal(opened, &creds) != nil || creds.AccessToken != access || creds.RefreshToken != refresh {
		t.Fatal("grant envelope lost long tokens")
	}
	if _, err = s.open(sealed, azureGrantAAD(azureTestAccount, azureTestSubject)); err == nil {
		t.Fatal("cross-organization decrypt succeeded")
	}
	if _, _, err = s.sealAzure(bytes.Repeat([]byte{'x'}, secrets.MaxOAuthPlaintext+1), "fixture"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("oversize error: %v", err)
	}
	if calls.Load() != 6 {
		t.Fatalf("unexpected HTTP count %d", calls.Load())
	}
}
func TestAzureOAuthScopeFailsBeforeIdentityReads(t *testing.T) {
	cases := []struct {
		name, scope string
		personal    bool
	}{
		{"missing", "", false}, {"broad", "user_impersonation", false}, {"missing_threads", "vso.profile vso.code vso.work vso.identity", false}, {"write_extra", azureTestScope + " vso.work_write", false},
		{"personal_missing", "", true}, {"personal_offline", "vso.profile offline_access", true}, {"personal_work", "vso.profile vso.work", true}, {"personal_broad", "user_impersonation", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := azureOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/"+azureTestTenant+"/oauth2/v2.0/token" {
					t.Error("scope failure reached identity API")
				}
				_ = r.ParseForm()
				if tc.personal && (r.Form.Get("scope") != azuredevops.ResourceScope || r.Form.Get("client_id") != azureTestIdentityApp) {
					t.Error("personal app requested organization permissions")
				}
				azureOAuthReply(w, map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": tc.scope})
			})
			result, err := s.VerifyAzureCallback(context.Background(), AzureRuntime{}, "fixture", azureTestCallback(tc.personal), "fixture-code")
			if !errors.Is(err, ErrPersonalCapability) || calls.Load() != 1 || result.token.AccessToken != "" || result.identity.ID != "" {
				t.Fatalf("scope result %v calls=%d", err, calls.Load())
			}
		})
	}
}
func TestAzureOAuthCompleteRequiresFreshExactActiveAccount(t *testing.T) {
	cases := []struct {
		name                               string
		accountID, accountName, identityID string
		active, container                  bool
		projectsStatus                     int
		wantCalls                          int
	}{
		{"account_absent", azureTestApp, "fixture-org", azureTestSubject, true, false, 200, 1},
		{"account_renamed", azureTestAccount, "another-org", azureTestSubject, true, false, 200, 1},
		{"different_identity", azureTestAccount, "fixture-org", azureTestApp, true, false, 200, 2},
		{"inactive", azureTestAccount, "fixture-org", azureTestSubject, false, false, 200, 2},
		{"container", azureTestAccount, "fixture-org", azureTestSubject, true, true, 200, 2},
		{"project_forbidden", azureTestAccount, "fixture-org", azureTestSubject, true, false, 403, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			s := azureOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch r.URL.Path {
				case "/_apis/accounts":
					azureOAuthReply(w, map[string]any{"value": []any{map[string]any{"accountId": tc.accountID, "accountName": tc.accountName}}})
				case "/fixture-org/_apis/identities":
					azureOAuthReply(w, map[string]any{"value": []any{map[string]any{"id": tc.identityID, "descriptor": "fixture;self", "isActive": tc.active, "isContainer": tc.container}}})
				case "/fixture-org/_apis/projects":
					w.WriteHeader(tc.projectsStatus)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(500)
				}
			})
			result, err := s.VerifyAzureComplete(context.Background(), azureTestComplete())
			if err == nil || result.verified || calls.Load() != int32(tc.wantCalls) {
				t.Fatalf("not failed closed: %v calls %d", err, calls.Load())
			}
		})
	}
}
func TestAzureOAuthPersonalAccountMismatchDiscardsTemporaryToken(t *testing.T) {
	s := azureOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + azureTestTenant + "/oauth2/v2.0/token":
			azureOAuthReply(w, map[string]any{"access_token": "personal-temporary-secret", "token_type": "Bearer", "expires_in": 3600, "scope": "vso.profile"})
		case "/_apis/profile/profiles/me":
			azureOAuthReply(w, map[string]any{"id": azureTestSubject, "displayName": "self"})
		case "/_apis/accounts":
			azureOAuthReply(w, map[string]any{"value": []any{map[string]any{"accountId": azureTestApp, "accountName": "other-org"}}})
		default:
			t.Error("personal temporary token reached organization APIs")
			w.WriteHeader(500)
		}
	})
	result, err := s.VerifyAzureCallback(context.Background(), AzureRuntime{}, "fixture", azureTestCallback(true), "fixture-code")
	if !errors.Is(err, ErrPersonalIdentity) || result.token.AccessToken != "" || result.token.RefreshToken != "" || result.identity.ID != "" {
		t.Fatalf("personal mismatch leaked result: %v", err)
	}
}
func TestAzureOAuthSuccessRetryAfterStopsSequenceWithinBudget(t *testing.T) {
	var calls atomic.Int32
	s := azureOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		azureOAuthReply(w, map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh", "token_type": "Bearer", "expires_in": 3600, "scope": azureTestScope})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	_, err := s.VerifyAzureCallback(ctx, AzureRuntime{}, "fixture", azureTestCallback(false), "fixture-code")
	var upstream *azuredevops.Error
	if !errors.As(err, &upstream) || upstream.Code != "rate_limited" || upstream.RetryAfter < 119*time.Second || calls.Load() != 1 || time.Since(start) > time.Second {
		t.Fatalf("failed budget/rate boundary: %v calls=%d", err, calls.Load())
	}
}
func TestAzureOAuthCompleteLocalFencesAndReplay(t *testing.T) {
	var calls atomic.Int32
	s := azureOAuthFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })
	for _, tc := range []struct {
		name   string
		mutate func(*AzureCompleteTarget)
		want   error
	}{
		{"expired", func(v *AzureCompleteTarget) { v.token.ExpiresAt = time.Now().Add(-time.Second) }, ErrNeedsReauth},
		{"app_changed", func(v *AzureCompleteTarget) { v.proof.AppID = azureTestIdentityApp }, ErrAzureAppChanged},
		{"invalid_tenant", func(v *AzureCompleteTarget) { v.proof.TenantID = "common" }, ErrInvalidRequest},
		{"wrong_scope", func(v *AzureCompleteTarget) { v.token.Scope = "vso.profile" }, ErrPersonalCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := azureTestComplete()
			tc.mutate(&target)
			_, err := s.VerifyAzureComplete(context.Background(), target)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
	target := azureTestComplete()
	target.proof.BindingID = azureTestAccount
	target.token = azuredevops.Token{}
	if _, err := s.VerifyAzureComplete(context.Background(), target); err != nil {
		t.Fatalf("completed proof must not request new authorization: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("local fence made external calls")
	}
}
func TestAzureOAuthProofBoundsAndStableFailureCodes(t *testing.T) {
	p := azureTestCallback(true).proof
	if !azureSameSession(p, p) {
		t.Fatal("same proof differs")
	}
	for _, change := range []func(*azureProof){func(v *azureProof) { v.BindingVersion++ }, func(v *azureProof) { v.ParentVersion++ }, func(v *azureProof) { v.GrantGeneration++ }, func(v *azureProof) { v.AccountID = azureTestApp }, func(v *azureProof) { v.OrganizationName = "another" }} {
		other := p
		change(&other)
		if azureSameSession(p, other) {
			t.Fatal("changed proof accepted")
		}
	}
	p.Identity = &AzureIdentity{ID: azureTestSubject, DisplayName: strings.Repeat("x", 61<<10)}
	if _, err := azureProofJSON(p); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("oversize proof %v", err)
	}
	for _, tc := range []struct {
		err  error
		code string
	}{
		{membership.ErrAuthorizationStale, "permission_denied"}, {ErrVersionConflict, "connection_changed"}, {ErrPersonalIdentity, "account_mismatch"}, {ErrPersonalCapability, "scope_not_allowed"},
		{&azuredevops.Error{Code: "rate_limited", HTTPStatus: 200}, "rate_limited"}, {&azuredevops.Error{Code: "invalid_grant", HTTPStatus: 400}, "authentication_failed"},
		{errors.New("opaque-provider-secret"), "exchange_failed"},
	} {
		if got := AzureFailure(tc.err); got != tc.code {
			t.Errorf("failure %s wanted %s", got, tc.code)
		}
	}
}
