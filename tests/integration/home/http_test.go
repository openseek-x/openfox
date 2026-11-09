package home

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/api"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
)

const httpSharedSpace = "00000000-0000-0000-0000-000000000e90"

// 走真实 HTTP 处理器：空间列表在成员未加入 SHARED 空间时不应 500；/my-tasks 的鉴权与参数校验。
func TestPostgresHomeHTTP(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{
		MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash("home http password")
	if err != nil {
		t.Fatal(err)
	}
	seed, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
		  ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
		      credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, []any{homeOwnerA, hash}},
		// 负责人不是这个 SHARED 空间的显式成员：列表左连接拿到 NULL 成员列。
		{`INSERT INTO openfox.spaces (organization_id, id, name, visibility, prefix) VALUES ($1::uuid, $2::uuid, 'HTTP shared', 'SHARED', 'httpshared')`, []any{homeOrgA, httpSharedSpace}},
	} {
		if _, err := seed.Exec(ctx, statement.query, statement.args...); err != nil {
			_ = seed.Close(ctx)
			t.Fatal(err)
		}
	}
	if err := seed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	database, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server := httptest.NewServer(api.New(identity.Service{
		Store: identity.PostgresStore{Pool: database}, PasswordHasher: hasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}, database, false))
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	get := func(client *http.Client, path string) (int, map[string]json.RawMessage) {
		t.Helper()
		response, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body map[string]json.RawMessage
		_ = json.NewDecoder(response.Body).Decode(&body)
		return response.StatusCode, body
	}
	login, err := client.Post(server.URL+"/v1/auth/login", "application/json", strings.NewReader(
		`{"email":"alice@example.test","password":"home http password","client_kind":"web"}`))
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.StatusCode != http.StatusCreated {
		t.Fatalf("login status = %d", login.StatusCode)
	}

	status, body := get(client, "/v1/organizations/"+homeOrgA+"/spaces")
	if status != http.StatusOK || !strings.Contains(string(body["spaces"]), httpSharedSpace) {
		t.Fatalf("spaces = %d %s", status, body["spaces"])
	}
	status, body = get(client, "/v1/organizations/"+homeOrgA+"/my-tasks?scope=assigned&limit=10")
	if status != http.StatusOK || !strings.HasPrefix(string(body["tasks"]), "[") {
		t.Fatalf("my-tasks = %d %v", status, body)
	}
	for _, query := range []string{"scope=everything", "limit=0", "limit=abc", "cursor=%21%21", "space_id=nope"} {
		if status, _ := get(client, "/v1/organizations/"+homeOrgA+"/my-tasks?"+query); status != http.StatusBadRequest {
			t.Fatalf("my-tasks?%s status = %d, want 400", query, status)
		}
	}
	if status, _ := get(&http.Client{}, "/v1/organizations/"+homeOrgA+"/my-tasks"); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated my-tasks = %d", status)
	}
	if status, _ := get(client, "/v1/organizations/00000000-0000-0000-0000-0000000000b1/my-tasks"); status != http.StatusNotFound {
		t.Fatalf("foreign organization my-tasks = %d", status)
	}

	// 「全部 Agent」目录与置顶：写操作需要 CSRF，请求体校验。
	status, body = get(client, "/v1/organizations/"+homeOrgA+"/agent-directory")
	if status != http.StatusOK || !strings.HasPrefix(string(body["entries"]), "[") || string(body["archived_count"]) == "" {
		t.Fatalf("agent-directory = %d %v", status, body)
	}
	if status, _ := get(client, "/v1/organizations/"+homeOrgA+"/agent-directory?archived=maybe"); status != http.StatusBadRequest {
		t.Fatalf("agent-directory invalid archived = %d", status)
	}
	var csrf string
	for _, cookie := range jar.Cookies(mustURL(t, server.URL)) {
		if cookie.Name == "openfox_csrf" {
			csrf = cookie.Value
		}
	}
	put := func(body, token string) int {
		t.Helper()
		request, err := http.NewRequest(http.MethodPut, server.URL+"/v1/organizations/"+homeOrgA+"/agent-directory/pins", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("X-CSRF-Token", token)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	missing := `{"agent_id":"00000000-0000-0000-0000-00000000ffff","pinned":true}`
	if status := put(missing, ""); status != http.StatusForbidden {
		t.Fatalf("pin without CSRF = %d", status)
	}
	if status := put(missing, csrf); status != http.StatusNotFound {
		t.Fatalf("pin unknown Agent = %d", status)
	}
	for _, invalid := range []string{`{"agent_id":"00000000-0000-0000-0000-00000000ffff"}`, `{"pinned":true}`, `{"pinned":true,"extra":1}`} {
		if status := put(invalid, csrf); status != http.StatusBadRequest {
			t.Fatalf("pin %s = %d, want 400", invalid, status)
		}
	}
}

func mustURL(t *testing.T, raw string) *neturl.URL {
	t.Helper()
	parsed, err := neturl.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
