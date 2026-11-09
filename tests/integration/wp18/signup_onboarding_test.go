package wp18

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/api"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
)

// TestEmailSignupAndFirstOrganizationAgainstPostgreSQL drives the full
// HTTP flow against a migrated database: an unknown email signs up through
// the email code, creates its first organization, and lists it.
func TestEmailSignupAndFirstOrganizationAgainstPostgreSQL(t *testing.T) {
	connectionString := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if connectionString == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	database, err := postgres.New(ctx, postgres.Config{
		URL: connectionString, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := identity.PostgresStore{Pool: database}
	handler := api.NewWithOptions(identity.Service{
		Store: store, EmailStore: store,
		EmailOTP:    identity.EmailOTPConfig{ChallengeTTL: 10 * time.Minute, MaxAttempts: 5, UniversalCode: "666666"},
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}, database, false, nil, api.AuthRateLimits{EmailRequestPerWindow: 100, EmailVerifyPerWindow: 100, Window: time.Minute})
	server := httptest.NewServer(http.StripPrefix("/api", handler))
	defer server.Close()

	email := "signup-" + time.Now().UTC().Format("150405.000000000") + "@example.test"
	client := newSessionClient(t, server.URL)
	client.mustPost(t, "/api/v1/auth/email/request", map[string]string{"email": email}, nil, http.StatusAccepted, nil)
	var login struct {
		UserID  string `json:"user_id"`
		NewUser bool   `json:"new_user"`
	}
	client.mustPost(t, "/api/v1/auth/email/verify", map[string]string{"email": email, "code": "666666"}, nil, http.StatusCreated, &login)
	if login.UserID == "" || !login.NewUser {
		t.Fatalf("first login = %#v, want new user", login)
	}

	var listed struct {
		Organizations []struct{ ID, Slug, Role string } `json:"organizations"`
	}
	client.mustGet(t, "/api/v1/me/organizations", http.StatusOK, &listed)
	if len(listed.Organizations) != 0 {
		t.Fatalf("new user organizations = %#v, want none", listed.Organizations)
	}

	// 同一幂等键并发提交：只创建一个组织，其余请求返回同一结果。
	slug := "org-" + strings.ReplaceAll(login.UserID[:8], "-", "")
	body := map[string]string{"name": "我的团队", "slug": slug}
	headers := map[string]string{"Idempotency-Key": "onboarding-" + login.UserID[:8]}
	var wait sync.WaitGroup
	ids := make([]string, 6)
	statuses := make([]int, 6)
	for index := range ids {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			var created struct{ ID string }
			statuses[index] = client.post(t, "/api/v1/organizations", body, headers, &created)
			ids[index] = created.ID
		}(index)
	}
	wait.Wait()
	createdCount := 0
	for index, status := range statuses {
		if status == http.StatusCreated {
			createdCount++
		} else if status != http.StatusOK {
			t.Fatalf("concurrent create %d status = %d", index, status)
		}
		if ids[index] == "" || ids[index] != ids[0] {
			t.Fatalf("concurrent create ids = %#v, want one organization", ids)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created responses = %d, want exactly 1", createdCount)
	}

	client.mustPost(t, "/api/v1/organizations", map[string]string{"name": "改名", "slug": slug}, headers, http.StatusConflict, nil)
	client.mustPost(t, "/api/v1/organizations", map[string]string{"name": "缺少键", "slug": slug + "-x"}, nil, http.StatusBadRequest, nil)

	client.mustGet(t, "/api/v1/me/organizations", http.StatusOK, &listed)
	if len(listed.Organizations) != 1 || listed.Organizations[0].ID != ids[0] || listed.Organizations[0].Role != "OWNER" {
		t.Fatalf("organizations after create = %#v", listed.Organizations)
	}

	var space struct{ ID string }
	client.mustPost(t, "/api/v1/organizations/"+ids[0]+"/spaces",
		map[string]string{"name": "默认空间", "visibility": "PRIVATE"}, nil, http.StatusCreated, &space)
	if space.ID == "" {
		t.Fatal("first space was not created")
	}
	var typesResponse struct {
		TaskTypes []struct {
			Name      string                      `json:"name"`
			IsDefault bool                        `json:"is_default"`
			Statuses  []struct{ Category string } `json:"statuses"`
		} `json:"task_types"`
	}
	client.mustGet(t, "/api/v1/organizations/"+ids[0]+"/spaces/"+space.ID+"/task-types", http.StatusOK, &typesResponse)
	if len(typesResponse.TaskTypes) != 1 || !typesResponse.TaskTypes[0].IsDefault || len(typesResponse.TaskTypes[0].Statuses) != 7 {
		t.Fatalf("new space task types = %#v, want one default type with 7 statuses", typesResponse.TaskTypes)
	}
	var statusesResponse struct {
		Statuses []struct{ Category string } `json:"statuses"`
	}
	client.mustGet(t, "/api/v1/organizations/"+ids[0]+"/task-statuses", http.StatusOK, &statusesResponse)
	if len(statusesResponse.Statuses) != 7 {
		t.Fatalf("seeded statuses = %d, want 7", len(statusesResponse.Statuses))
	}

	// 已注册邮箱再次登录：不重复创建账号，new_user=false。
	second := newSessionClient(t, server.URL)
	second.mustPost(t, "/api/v1/auth/email/request", map[string]string{"email": strings.ToUpper(email)}, nil, http.StatusAccepted, nil)
	var again struct {
		UserID  string `json:"user_id"`
		NewUser bool   `json:"new_user"`
	}
	second.mustPost(t, "/api/v1/auth/email/verify", map[string]string{"email": email, "code": "666666"}, nil, http.StatusCreated, &again)
	if again.UserID != login.UserID || again.NewUser {
		t.Fatalf("returning login = %#v, want same user and new_user=false", again)
	}

	// 其他用户看不到该组织。
	outsider := newSessionClient(t, server.URL)
	otherEmail := "outsider-" + email
	outsider.mustPost(t, "/api/v1/auth/email/request", map[string]string{"email": otherEmail}, nil, http.StatusAccepted, nil)
	outsider.mustPost(t, "/api/v1/auth/email/verify", map[string]string{"email": otherEmail, "code": "666666"}, nil, http.StatusCreated, nil)
	outsider.mustGet(t, "/api/v1/organizations/"+ids[0]+"/spaces", http.StatusNotFound, nil)
}

type sessionClient struct {
	base string
	http *http.Client
	jar  *cookiejar.Jar
}

func newSessionClient(t *testing.T, base string) *sessionClient {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &sessionClient{base: base, jar: jar, http: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

func (c *sessionClient) csrf() string {
	parsed, _ := url.Parse(c.base)
	for _, cookie := range c.jar.Cookies(parsed) {
		if cookie.Name == "openfox_csrf" {
			return cookie.Value
		}
	}
	return ""
}

func (c *sessionClient) post(t *testing.T, path string, body any, headers map[string]string, target any) int {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, c.base+path, strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token := c.csrf(); token != "" {
		request.Header.Set("X-CSRF-Token", token)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return c.do(t, request, target)
}

func (c *sessionClient) mustPost(t *testing.T, path string, body any, headers map[string]string, want int, target any) {
	t.Helper()
	if got := c.post(t, path, body, headers, target); got != want {
		t.Fatalf("POST %s status = %d, want %d", path, got, want)
	}
}

func (c *sessionClient) mustGet(t *testing.T, path string, want int, target any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.do(t, request, target); got != want {
		t.Fatalf("GET %s status = %d, want %d", path, got, want)
	}
}

func (c *sessionClient) do(t *testing.T, request *http.Request, target any) int {
	t.Helper()
	response, err := c.http.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if target != nil && response.StatusCode < 300 {
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatalf("decode %s: %v", request.URL.Path, err)
		}
	}
	return response.StatusCode
}
