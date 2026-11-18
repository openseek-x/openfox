package integrations

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"openfox/internal/imports/jiraapi"
	"openfox/internal/imports/zentao"
	"openfox/internal/platform/safehttp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCanonicalSitePreservesInstanceBoundaries(t *testing.T) {
	for input, want := range map[string]string{
		" https://Jira.Example.com:443/jira/ ": "https://jira.example.com/jira",
		"https://Jira.Example.com:8443/Jira/":  "https://jira.example.com:8443/Jira",
		"https://example.com/team%252Fjira/":   "https://example.com/team%252Fjira",
		"https://[2001:db8::1]:443/":           "https://[2001:db8::1]",
	} {
		got, err := CanonicalSite(input)
		if err != nil || got != want {
			t.Fatalf("%q => %q %v", input, got, err)
		}
	}
	for _, input := range []string{"https://example.com/team%2Fjira", "https://example.com/team%2f", "https://example.com/%61", "http://example.com", "https://user:pass@example.com", "https://example.com/?", "https://example.com/?a=1", "https://example.com/#x"} {
		if _, err := CanonicalSite(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestVerifyOrganizationSiteProtocols(t *testing.T) {
	for _, kind := range []string{"api_token", "pat", "password"} {
		t.Run(kind, func(t *testing.T) {
			deployment := "server"
			if kind == "api_token" {
				deployment = "cloud"
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/team%2Fjira/rest/api/2/myself" {
					t.Errorf("path=%q escaped=%q", r.URL.Path, r.URL.EscapedPath())
				}
				if kind == "pat" {
					if r.Header.Get("Authorization") != "Bearer fake-secret" {
						t.Error("missing bearer")
					}
				} else {
					user, password, ok := r.BasicAuth()
					if !ok || user != "actor@example.test" || password != "fake-secret" {
						t.Error("incorrect basic")
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"accountId": "verified-id", "name": "verified-id"})
			}))
			defer server.Close()
			ca := x509.NewCertPool()
			ca.AddCert(server.Certificate())
			grant, err := VerifySiteConnection(context.Background(), "jira", SiteConnectionInput{Site: server.URL + "/team%252Fjira/", Deployment: deployment, Jira: &jiraapi.Auth{Kind: kind, Email: "actor@example.test", Username: "actor@example.test", Secret: "fake-secret"}}, safehttp.Options{RootCAs: ca, AllowAddr: func(net.IP) bool { return true }})
			if err != nil || grant.Identity != "verified-id" || grant.Site != server.URL+"/team%252Fjira" {
				t.Fatalf("grant identity/site=%q %q err=%v", grant.Identity, grant.Site, err)
			}
		})
	}
	t.Run("Zen token profile and independent gateway", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, password, ok := r.BasicAuth()
			if !ok || user != "gateway" || password != "gateway-secret" {
				t.Error("gateway auth missing")
			}
			if r.URL.Path == "/api.php/v1/tokens" {
				var body map[string]string
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body["account"] != "LoginName" || body["password"] != "zen-secret" {
					t.Error("login body")
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"token": "zen-token"})
				return
			}
			if r.URL.Path != "/api.php/v1/user" || r.Header.Get("Token") != "zen-token" {
				t.Error("profile request")
			}
			_, _ = w.Write([]byte(`{"profile":{"account":"canonical-user","phone":"not-retained"}}`))
		}))
		defer server.Close()
		ca := x509.NewCertPool()
		ca.AddCert(server.Certificate())
		grant, err := VerifySiteConnection(context.Background(), "zentao", SiteConnectionInput{Site: server.URL, Zen: &zentao.Credentials{Account: "LoginName", Password: "zen-secret", BasicUser: "gateway", BasicPassword: "gateway-secret"}}, safehttp.Options{RootCAs: ca, AllowAddr: func(net.IP) bool { return true }})
		if err != nil || grant.Identity != "canonical-user" || grant.LoginIdentity != "LoginName" || grant.Zen == nil || grant.Zen.Token != "zen-token" {
			t.Fatalf("identity=%q err=%v", grant.Identity, err)
		}
	})
}

func TestVerifyOrganizationSiteFailures(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			ca := x509.NewCertPool()
			ca.AddCert(server.Certificate())
			_, err := VerifySiteConnection(context.Background(), "jira", SiteConnectionInput{Site: server.URL, Deployment: "cloud", Jira: &jiraapi.Auth{Kind: "api_token", Email: "a@example.test", Secret: "redacted"}}, safehttp.Options{RootCAs: ca, AllowAddr: func(net.IP) bool { return true }})
			if err == nil || strings.Contains(err.Error(), "redacted") {
				t.Fatalf("error=%v", err)
			}
		})
	}
	t.Run("redirect is not followed", func(t *testing.T) {
		var followed atomic.Bool
		dest := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Store(true) }))
		defer dest.Close()
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, http.StatusFound) }))
		defer server.Close()
		ca := x509.NewCertPool()
		ca.AddCert(server.Certificate())
		_, err := VerifySiteConnection(context.Background(), "jira", SiteConnectionInput{Site: server.URL, Deployment: "cloud", Jira: &jiraapi.Auth{Kind: "api_token", Email: "a@example.test", Secret: "fake"}}, safehttp.Options{RootCAs: ca, AllowAddr: func(net.IP) bool { return true }})
		if err == nil || followed.Load() {
			t.Fatalf("redirect followed=%v err=%v", followed.Load(), err)
		}
	})
}
