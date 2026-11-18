package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/api"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

const (
	aliceID = "00000000-0000-0000-0000-000000000001"
	bobID   = "00000000-0000-0000-0000-000000000002"
	orgA    = "00000000-0000-0000-0000-0000000000a1"
	orgB    = "00000000-0000-0000-0000-0000000000b1"
)

func TestPostgresAuthAndTenantAPI(t *testing.T) {
	connectionString := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if connectionString == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{
		MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	seed, err := pgx.Connect(ctx, connectionString)
	if err != nil {
		t.Fatal(err)
	}
	_, err = seed.Exec(ctx, `
		INSERT INTO openfox.password_credentials (user_id, password_hash)
		VALUES ($1::uuid, $2)
		ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
		    credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()
	`, aliceID, hash)
	if err != nil {
		_ = seed.Close(ctx)
		t.Fatal(err)
	}
	if err := seed.Close(ctx); err != nil {
		t.Fatal(err)
	}

	database, err := postgres.New(ctx, postgres.Config{
		URL: connectionString, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server := httptest.NewServer(api.New(identity.Service{
		Store:           identity.PostgresStore{Pool: database},
		PasswordHasher:  hasher,
		SessionIdle:     time.Hour,
		SessionAbsolute: 24 * time.Hour,
		Now:             time.Now,
	}, database, false))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	loginResponse := doJSON(t, client, http.MethodPost, server.URL+"/v1/auth/login", map[string]any{
		"email": "alice@example.test", "password": "correct horse battery staple", "client_kind": "web",
	}, "")
	if loginResponse.StatusCode != http.StatusCreated {
		t.Fatalf("login status = %d, body = %s", loginResponse.StatusCode, readBody(loginResponse))
	}
	loginResponse.Body.Close()

	spacesResponse := doJSON(t, client, http.MethodGet, server.URL+"/v1/organizations/"+orgA+"/spaces", nil, "")
	if spacesResponse.StatusCode != http.StatusOK {
		t.Fatalf("list spaces status = %d, body = %s", spacesResponse.StatusCode, readBody(spacesResponse))
	}
	var spaces struct {
		Spaces []map[string]any `json:"spaces"`
	}
	if err := json.NewDecoder(spacesResponse.Body).Decode(&spaces); err != nil {
		t.Fatal(err)
	}
	spacesResponse.Body.Close()
	if len(spaces.Spaces) != 1 {
		t.Fatalf("organization A spaces = %d, want 1", len(spaces.Spaces))
	}

	hiddenResponse := doJSON(t, client, http.MethodGet, server.URL+"/v1/organizations/"+orgB+"/spaces", nil, "")
	if hiddenResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("organization B status = %d, body = %s", hiddenResponse.StatusCode, readBody(hiddenResponse))
	}
	hiddenResponse.Body.Close()

	csrf := cookieValue(jar, server.URL, "openfox_csrf")
	createResponse := doJSON(t, client, http.MethodPost, server.URL+"/v1/organizations/"+orgA+"/spaces", map[string]any{
		"name": "Created by integration", "visibility": "PRIVATE",
	}, csrf)
	if createResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create space status = %d, body = %s", createResponse.StatusCode, readBody(createResponse))
	}
	createResponse.Body.Close()

	unauthenticated := doJSON(t, &http.Client{}, http.MethodGet, server.URL+"/v1/organizations/"+orgA+"/spaces", nil, "")
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, body = %s", unauthenticated.StatusCode, readBody(unauthenticated))
	}
	unauthenticated.Body.Close()

	store := identity.PostgresStore{Pool: database}
	selectionA, _, err := store.SelectOrganization(ctx, aliceID, orgA)
	if err != nil {
		t.Fatal(err)
	}
	var tenantACount int
	if err := database.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selectionA, nil
		}), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT count(*) FROM openfox.spaces").Scan(&tenantACount)
		}); err != nil {
		t.Fatal(err)
	}
	if tenantACount != 2 {
		t.Fatalf("organization A adapter count = %d, want 2", tenantACount)
	}

	var contextFreeCount int
	if err := database.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT count(*) FROM openfox.spaces").Scan(&contextFreeCount)
	}); err != nil {
		t.Fatal(err)
	}
	if contextFreeCount != 0 {
		t.Fatalf("context-free adapter count = %d, want 0", contextFreeCount)
	}

	selectionB, _, err := store.SelectOrganization(ctx, bobID, orgB)
	if err != nil {
		t.Fatal(err)
	}
	var tenantBCount int
	if err := database.RunForOrganization(ctx, orgB, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selectionB, nil
		}), func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SELECT count(*) FROM openfox.spaces").Scan(&tenantBCount)
		}); err != nil {
		t.Fatal(err)
	}
	if tenantBCount != 1 {
		t.Fatalf("organization B adapter count = %d, want 1", tenantBCount)
	}
}

func doJSON(t *testing.T, client *http.Client, method, url string, body map[string]any, csrf string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func cookieValue(jar http.CookieJar, rawURL, name string) string {
	for _, cookie := range jar.Cookies(mustURL(rawURL)) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func mustURL(value string) *url.URL {
	parsed, err := url.Parse(value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func readBody(response *http.Response) string {
	if response == nil || response.Body == nil {
		return ""
	}
	value, err := io.ReadAll(response.Body)
	if err != nil {
		return err.Error()
	}
	return string(value)
}
