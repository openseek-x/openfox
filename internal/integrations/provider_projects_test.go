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

type projectTransport func(r *http.Request) (*http.Response, error)

func (f projectTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderProjectRows(t *testing.T) {
	for _, tc := range []struct {
		provider, project, want string
		responses               map[string]string
	}{
		{"teambition", "p", "w", map[string]string{"/api/v3/project/p/taskflowstatus/search": `{"result":[{"id":"done","name":"Done"}]}`, "/api/v3/project/p/task/query": `{"result":[{"id":"w","projectId":"p","content":"Task","tfsId":"done"}]}`}},
		{"tapd", "100", "101000000000000017", map[string]string{"/stories": `{"status":1,"data":[{"Story":{"id":"101000000000000017","name":"Task","workspace_id":"100"}}]}`, "/bugs": `{"status":1,"data":[]}`, "/tasks": `{"status":1,"data":[]}`}},
		{"pingcode", "pc", "w", map[string]string{"/v1/project/work_items": `{"total":1,"values":[{"id":"w","title":"Task","project":{"id":"pc"},"type":"story","identifier":"P-7","assignee":{"id":"u7","display_name":"Alex"},"state":{"name":"Shipped","type":"completed"},"priority":{"name":"High"}}]}`}},
		{"shortcut", "42", "workspace/32", map[string]string{"/api/v3/workflows": `[{"states":[{"id":7,"name":"Done"}]}]`, "/api/v3/projects/42/stories": `[{"name":"Task","id":32,"workflow_state_id":7,"project_id":42}]`}},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			src := ProjectSource{Provider: tc.provider, ExternalID: "workspace", auth: ProviderTokenInput{Method: "token", Secret: "test"}}
			src.transport = projectTransport(func(r *http.Request) (*http.Response, error) {
				body, ok := tc.responses[r.URL.Path]
				if !ok || r.Method != "GET" || !strings.HasPrefix(r.URL.String(), providerRoots[tc.provider]+"/") {
					t.Fatalf("unexpected request %s", r.URL.Path)
				}
				if tc.provider == "pingcode" && (r.URL.Query().Get("project_id") != tc.project || r.URL.Query().Get("page_index") != "0") {
					t.Fatal("wrong project filter or first page")
				}
				if tc.provider == "tapd" && r.URL.Query().Get("workspace_id") != tc.project {
					t.Fatal("wrong workspace")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
			})
			rows, err := src.ProjectRows(context.Background(), tc.project)
			if err != nil || len(rows) != 2 || rows[1][0] != tc.want || rows[1][1] != "Task" {
				t.Fatalf("rows=%v, err=%v", rows, err)
			}
			if tc.provider == "pingcode" && (rows[1][4] != "Alex" || strings.Join(rows[1][7:], "/") != "u7/done/High/P-7") {
				t.Fatal(rows[1])
			}
		})
	}
}
func TestProviderProjectRejectsWrongProjectAndRedirect(t *testing.T) {
	src := ProjectSource{Provider: "pingcode"}
	src.transport = projectTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"values":[{"id":"w","project":{"id":"other"}}],"total":1}`)), Header: http.Header{}}, nil
	})
	if _, err := src.ProjectRows(context.Background(), "expected"); !errors.Is(err, ErrUpstreamUnavailable) {
		t.Fatal(err)
	}
	src.transport = projectTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "open.pingcode.com" {
			t.Fatal("credential redirected")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://other.example"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	if _, err := src.Projects(context.Background()); !errors.Is(err, ErrUpstreamUnavailable) {
		t.Fatal(err)
	}
}

func TestPingCodeRejectsAnotherCompanyBeforeProjectList(t *testing.T) {
	src := ProjectSource{Provider: "pingcode", auth: ProviderTokenInput{CompanyID: "expected"}}
	src.transport = projectTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/directory/team" {
			t.Fatalf("requested projects before checking company: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"different"}`))}, nil
	})
	if _, err := src.Projects(context.Background()); !errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("mismatched company: %v", err)
	}
}

func TestShortcutRejectsStoryFromDifferentProject(t *testing.T) {
	src := ProjectSource{Provider: "shortcut", ExternalID: "ws"}
	src.transport = projectTransport(func(r *http.Request) (*http.Response, error) {
		body := "[]"
		if r.URL.Path == "/api/v3/projects/12/stories" {
			body = `[{"id":3,"name":"stray","project_id":9}]`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if _, err := src.ProjectRows(context.Background(), "12"); !errors.Is(err, ErrUpstreamUnavailable) {
		t.Fatal(err)
	}
}

func TestWriteProjectTextUsesOnlyChangedFields(t *testing.T) {
	title := "renamed"
	for _, tc := range []struct{ provider, project, id, path, method, field, response string }{
		{"shortcut", "7", "workspace/2", "/api/v3/stories/2", "PUT", "name", `{"id":2,"project_id":7}`},
		{"pingcode", "p", "w", "/v1/project/work_items/w", "PATCH", "title", `{"id":"w","project":{"id":"p"}}`},
		{"teambition", "p", "w", "/api/v3/task/w/content", "PUT", "content", `{"result":{"content":"renamed"}}`},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			s := ProjectSource{Provider: tc.provider, ExternalID: "workspace"}
			s.transport = projectTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != tc.method || r.URL.Path != tc.path {
					t.Fatalf("%s %s", r.Method, r.URL.Path)
				}
				var fields map[string]string
				if err := json.NewDecoder(r.Body).Decode(&fields); err != nil || len(fields) != 1 || fields[tc.field] != title {
					t.Fatalf("fields = %v: %v", fields, err)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			})
			if err := s.UpdateProjectText(context.Background(), tc.project, tc.id, "Task", &title, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}
