package yunxiao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/integrations/gitapi"
)

const testToken = "synthetic-yunxiao-credential"

func fixture(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-yunxiao-token") != testToken {
			t.Error("missing expected credential")
			w.WriteHeader(401)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return &Client{APIURL: server.URL, HTTP: server.Client()}
}

func TestIdentityUsesAuthenticatedUserAndSelectedOrganization(t *testing.T) {
	for _, status := range []string{"ENABLED", "NORMAL_USING", "DISABLED", "UNVISITED", "unknown"} {
		t.Run(status, func(t *testing.T) {
			calls := 0
			client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" {
					t.Errorf("unexpected method %s", r.Method)
				}
				switch r.URL.Path {
				case "/oapi/v1/platform/user":
					fmt.Fprint(w, `{"id":"user-a","name":"Alice","email":"alice@example.test","lastOrganization":"other-org"}`)
				case "/oapi/v1/platform/organizations/org-a/members:readByUser":
					if r.URL.Query().Get("userId") != "user-a" {
						t.Fatal("identity must come from authenticated response")
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"userId": "user-a", "organizationId": "org-a", "status": status})
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			})
			user, err := client.Identity(context.Background(), testToken, "org-a")
			if status == "ENABLED" || status == "NORMAL_USING" {
				if err != nil || user.ID != "user-a" {
					t.Fatalf("identity: %v %+v", err, user)
				}
			} else if !errors.Is(err, ErrForbidden) {
				t.Fatalf("unusable member accepted: %v", err)
			}
			if calls != 2 {
				t.Fatalf("got %d calls", calls)
			}
		})
	}
	t.Run("cross organization", func(t *testing.T) {
		client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/oapi/v1/platform/user" {
				fmt.Fprint(w, `{"id":"user-a"}`)
			} else {
				fmt.Fprint(w, `{"userId":"user-a","organizationId":"org-b","status":"ENABLED"}`)
			}
		})
		if _, err := client.Identity(context.Background(), testToken, "org-a"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("cross-org accepted: %v", err)
		}
	})
}

func TestProjectsRequireCompleteStablePagination(t *testing.T) {
	for _, problem := range []string{"", "duplicate", "changed_total", "missing_headers", "missing_row"} {
		t.Run(problem, func(t *testing.T) {
			client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/oapi/v1/projex/organizations/org-a/projects:search" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var input struct {
					Page    int    `json:"page"`
					PerPage int    `json:"perPage"`
					OrderBy string `json:"orderBy"`
					Sort    string `json:"sort"`
				}
				if json.NewDecoder(r.Body).Decode(&input) != nil || input.PerPage != 200 || input.OrderBy != "gmtCreate" || input.Sort != "asc" {
					t.Fatal("unexpected page body")
				}
				total := 201
				if problem == "changed_total" && input.Page == 2 {
					total = 202
				}
				if problem != "missing_headers" {
					w.Header().Set("x-total", strconv.Itoa(total))
					w.Header().Set("x-page", strconv.Itoa(input.Page))
					w.Header().Set("x-per-page", "200")
					w.Header().Set("x-total-pages", "2")
					if input.Page == 1 {
						w.Header().Set("x-next-page", "2")
					}
				}
				rows := make([]Project, 0)
				for i := (input.Page - 1) * 200; i < min(input.Page*200, total); i++ {
					id := fmt.Sprint(i + 1)
					if problem == "duplicate" && input.Page == 2 {
						id = "1"
					}
					rows = append(rows, Project{Named: Named{ID: id, Name: "Project " + id}, LogicalStatus: "normal"})
				}
				if problem == "missing_row" && input.Page == 2 {
					rows = nil
				}
				_ = json.NewEncoder(w).Encode(rows)
			})
			rows, err := client.Projects(context.Background(), testToken, "org-a")
			if problem == "" {
				if err != nil || len(rows) != 201 {
					t.Fatalf("complete source: rows=%d error=%v", len(rows), err)
				}
			} else if err == nil || rows != nil {
				t.Fatalf("partial source accepted: rows=%d error=%v", len(rows), err)
			}
		})
	}
}

func TestClientFailureBoundaries(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "999999999")
				w.WriteHeader(status)
				fmt.Fprint(w, testToken)
			})
			_, err := client.Identity(context.Background(), testToken, "org-a")
			if err == nil || strings.Contains(err.Error(), testToken) {
				t.Fatalf("unsafe error: %v", err)
			}
			expected := map[int]error{401: gitapi.ErrTokenRevoked, 403: ErrForbidden, 404: gitapi.ErrNotFound, 500: gitapi.ErrRequestFailed}
			if status != 429 && !errors.Is(err, expected[status]) {
				t.Fatalf("wrong classification: %v", err)
			}
			if status == 429 {
				var limited *gitapi.ErrRateLimited
				if !errors.As(err, &limited) || time.Until(limited.ResetAt) > 24*time.Hour || time.Until(limited.ResetAt) < 23*time.Hour {
					t.Fatalf("unbounded retry: %v", err)
				}
			}
		})
	}
	t.Run("credential redirect blocked", func(t *testing.T) {
		var hits atomic.Int32
		sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
		defer sink.Close()
		client := fixture(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, sink.URL, 302) })
		if _, err := client.Identity(context.Background(), testToken, "org-a"); err == nil || hits.Load() != 0 {
			t.Fatalf("redirect leaked request: %v, hits=%d", err, hits.Load())
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		client := fixture(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := client.Identity(ctx, testToken, "org-a")
		if err == nil || time.Since(start) > time.Second {
			t.Fatalf("cancellation failed: %v", err)
		}
	})
	t.Run("invalid input before network", func(t *testing.T) {
		client := fixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("invalid input reached upstream") })
		if _, err := client.Identity(context.Background(), testToken, "../org"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("path input accepted: %v", err)
		}
		if _, err := client.Identity(context.Background(), "bad\nkey", "org-a"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("credential header accepted: %v", err)
		}
	})
}
