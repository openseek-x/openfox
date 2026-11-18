package mcpoauth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type metadataTransport func(*http.Request) (*http.Response, error)

func (f metadataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDiscoveryKeepsCandidateResponsesIndependent(t *testing.T) {
	const issuer = "https://auth.example/tenant"
	const resource = "https://mcp.example/r"
	for _, complete := range []bool{false, true} {
		c := Client{HTTP: &http.Client{Transport: metadataTransport(func(r *http.Request) (*http.Response, error) {
			body := "{}"
			switch r.URL.Path {
			case "/.well-known/oauth-protected-resource/r":
				body = `{"resource":"https://mcp.example/r","authorization_servers":["https://auth.example/tenant"]}`
			case "/.well-known/oauth-authorization-server/tenant":
				body = `{"issuer":"https://other.example","token_endpoint":"https://old.example/token"}`
			case "/.well-known/openid-configuration/tenant":
				body = `{"issuer":"https://auth.example/tenant","authorization_endpoint":"https://auth.example/login","code_challenge_methods_supported":["S256"]`
				if complete {
					body += `,"token_endpoint":"https://auth.example/token"`
				}
				body += "}"
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}}
		pending, _, err := c.Start(context.Background(), resource, "https://client.example/done", "state", "client", "")
		if complete {
			if err != nil || pending.Issuer != issuer || pending.TokenEndpoint != "https://auth.example/token" {
				t.Fatalf("complete fallback failed: %v", err)
			}
		} else if !errors.Is(err, ErrProtocol) {
			t.Fatal("accepted fields from a rejected discovery response")
		}
	}
}
