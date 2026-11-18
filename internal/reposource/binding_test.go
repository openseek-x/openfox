package reposource

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConnectionCannotAuthorizeAnotherOrigin(t *testing.T) {
	for _, tc := range []struct {
		provider, base, url string
		valid               bool
	}{
		{"github", "", "https://github.com/acme/private.git", true},
		{"github", "", "https://github.com.evil.test/acme/private.git", false},
		{"github", "", "https://github.com@evil.test/acme/private.git", false},
		{"github", "", "https://github.com/acme/private.git?token=x", false},
		{"gitlab", "https://git.example.test/gitlab", "https://git.example.test/gitlab/group/repo.git", true},
		{"gitlab", "https://git.example.test/gitlab", "https://git.example.test/gitlab-other/group/repo.git", false},
		{"gitlab", "https://git.example.test/gitlab", "https://git.example.test/gitlab/group/../repo.git", false},
		{"gitlab", "https://git.example.test/gitlab", "https://git.example.test:8443/gitlab/group/repo.git", false},
	} {
		if MatchesInstance(tc.provider, tc.base, tc.url) != tc.valid {
			t.Fatalf("unexpected binding validity: %s", tc.url)
		}
	}
}
func TestCacheIdentityExcludesCredentials(t *testing.T) {
	r := Repository{URL: "https://github.com/acme/repo.git", Authorization: &Authorization{Scope: "connection-a", Token: "test-secret", Username: "test-user"}}
	b, e := json.Marshal(r.Identity())
	if e != nil || strings.Contains(string(b), "test-secret") || strings.Contains(string(b), "test-user") {
		t.Fatal("credential in cache identity")
	}
	r.Authorization.Token = "rotated"
	next, _ := json.Marshal(r.Identity())
	if string(b) != string(next) {
		t.Fatal("rotation changed workspace")
	}
	r.Authorization.Scope = "connection-b"
	next, _ = json.Marshal(r.Identity())
	if string(b) == string(next) {
		t.Fatal("connections shared an identity")
	}
	r.Authorization = nil
	r.ConnectionID = ""
	legacy, _ := json.Marshal(r)
	next, _ = json.Marshal(r.Identity())
	if string(legacy) != string(next) {
		t.Fatal("anonymous workspace identity changed")
	}
}
