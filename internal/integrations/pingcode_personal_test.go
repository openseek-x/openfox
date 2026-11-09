package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func pingCodeResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestVerifyPingCodePersonal(t *testing.T) {
	grant := PingCodePersonalGrant{CompanyID: "team-1", token: "personal-token"}
	requests := 0
	grant.transport = projectTransport(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Method != http.MethodGet || request.URL.Scheme != "https" || request.URL.Host != "open.pingcode.com" || request.Header.Get("Authorization") != "Bearer personal-token" {
			t.Fatalf("unexpected identity request: %s %s", request.Method, request.URL)
		}
		switch request.URL.Path {
		case "/v2/myself":
			return pingCodeResponse(200, `{"id":"user-1","display_name":"A User","name":"Other Name"}`), nil
		case "/v2/directory/team":
			return pingCodeResponse(200, `{"id":"team-1"}`), nil
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
			return nil, nil
		}
	})
	verified, err := VerifyPingCodePersonal(context.Background(), grant)
	if err != nil || verified.UserID != "user-1" || verified.Name != "A User" || requests != 2 {
		t.Fatalf("verification = %+v, %v, requests=%d", verified, err, requests)
	}
}

func TestVerifyPingCodePersonalRejectsWrongIdentity(t *testing.T) {
	for _, scenario := range []struct {
		name, myself, team string
		status             int
	}{
		{name: "different company", myself: `{"id":"user-1"}`, team: `{"id":"team-2"}`, status: 200},
		{name: "missing user", myself: `{"name":"A User"}`, team: `{"id":"team-1"}`, status: 200},
		{name: "revoked token", myself: `{}`, status: 401},
		{name: "redirect", myself: `{}`, status: 302},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			grant := PingCodePersonalGrant{CompanyID: "team-1", token: "personal-token"}
			grant.transport = projectTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/v2/myself" {
					return pingCodeResponse(scenario.status, scenario.myself), nil
				}
				return pingCodeResponse(200, scenario.team), nil
			})
			_, err := VerifyPingCodePersonal(context.Background(), grant)
			if err == nil {
				t.Fatal("invalid personal identity accepted")
			}
			if scenario.status == 401 && !errors.Is(err, ErrNeedsReauth) {
				t.Fatalf("revoked token: %v", err)
			}
		})
	}
}

func TestPingCodePersonalCommentUsesAuthorToken(t *testing.T) {
	grant := PingCodePersonalGrant{UserID: "user-1", token: "personal-token"}
	grant.transport = projectTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/v2/comments" || request.Header.Get("Authorization") != "Bearer personal-token" || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected comment request: %s %s", request.Method, request.URL)
		}
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body) != 3 || body["content"] != "hello" || body["principal_type"] != "workitem" || body["principal_id"] != "work-1" {
			t.Fatalf("comment payload: %v, %v", body, err)
		}
		return pingCodeResponse(201, `{"id":"comment-1","created_by":{"id":"user-1"}}`), nil
	})
	id, err := grant.CreateComment(context.Background(), "work-1", "hello")
	if err != nil || id != "comment-1" {
		t.Fatalf("created comment = %q, %v", id, err)
	}
}

func TestPingCodePersonalCommentRejectsWrongAuthorAndFailure(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		response *http.Response
	}{
		{name: "wrong author", response: pingCodeResponse(200, `{"id":"comment-1","created_by":{"id":"other"}}`)},
		{name: "missing remote id", response: pingCodeResponse(200, `{"created_by":{"id":"user-1"}}`)},
		{name: "revoked", response: pingCodeResponse(401, `{}`)},
		{name: "redirect", response: pingCodeResponse(302, `{}`)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			grant := PingCodePersonalGrant{UserID: "user-1", token: "personal-token"}
			grant.transport = projectTransport(func(*http.Request) (*http.Response, error) { return scenario.response, nil })
			if _, err := grant.CreateComment(context.Background(), "work-1", "hello"); err == nil {
				t.Fatal("unverified comment accepted")
			}
		})
	}
}
