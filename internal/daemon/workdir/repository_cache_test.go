package workdir

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openfox/internal/reposource"
)

func TestRepositoryCacheDoesNotRemoveCheckouts(t *testing.T) {
	if os.Getenv("OPENFOX_TEST_PUBLIC_GIT") != "1" {
		t.Skip("requires access to the public Git fixture")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(GuardPolicy{ManagedRoot: filepath.Join(root, "work")})
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(root, "cache")
	org := "qa-org"
	repos := []reposource.Repository{{URL: "https://github.com/octocat/Hello-World.git"}}
	key := BindingKey{OrganizationID: org, SpaceID: "qa-space", AgentID: "qa-agent", ConversationID: "first"}
	first, err := manager.PrepareRepositories(context.Background(), key, repos, cache)
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	files, err := filepath.Glob(filepath.Join(cache, hashedName(org), "*.bundle"))
	if err != nil || len(files) != 1 {
		t.Fatalf("cache %v %v", files, err)
	}
	info, _ := os.Stat(files[0])
	key.ConversationID = "second"
	repos[0].Branch = "master"
	second, err := manager.PrepareRepositories(context.Background(), key, repos, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	after, _ := os.Stat(files[0])
	if !os.SameFile(info, after) || first.Path == second.Path {
		t.Fatal("expected a shared bundle and separate checkouts")
	}
	if err := manager.ExpireRepositoryCache(context.Background(), cache, "other", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatal("another organization evicted the bundle")
	}
	if err := manager.ExpireRepositoryCache(context.Background(), cache, org, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatal("expired bundle remained")
	}
	for _, w := range []*PreparedWorkspace{first, second} {
		if name, err := repositoryGit(context.Background(), w.Path, "rev-parse", "--abbrev-ref", "@{upstream}"); err != nil || name != "origin/master" {
			t.Fatalf("upstream = %s, err = %v", name, err)
		}
		if _, err := repositoryGit(context.Background(), w.Path, "fsck", "--connectivity-only"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrivateRepositoryCredentialIsProcessOnly(t *testing.T) {
	dir := t.TempDir()
	if _, e := repositoryGit(context.Background(), dir, "init", "--bare", "."); e != nil {
		t.Fatal(e)
	}
	r := reposource.Repository{URL: "https://github.com/acme/private.git", Authorization: &reposource.Authorization{Scope: "grant-a", Username: "oauth2", Token: "test-only-token"}}
	got, e := repositoryGitAuth(context.Background(), dir, r, "config", "--get", "http."+r.URL+".extraHeader")
	if e != nil || !strings.HasPrefix(got, "Authorization: Basic ") {
		t.Fatal("process credential not applied")
	}
	data, e := os.ReadFile(filepath.Join(dir, "config"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "extraHeader") || strings.Contains(string(data), "test-only-token") {
		t.Fatal("credential persisted in config")
	}
	if _, e := repositoryGit(context.Background(), dir, "config", "--get", "http."+r.URL+".extraHeader"); e == nil {
		t.Fatal("credential leaked to subsequent command")
	}
}

func TestPrivateRepositoryCacheRevalidatesAccess(t *testing.T) {
	raw := os.Getenv("OPENFOX_TEST_PRIVATE_GIT_URL")
	if raw == "" {
		t.Skip("requires isolated authenticated Git HTTPS fixture")
	}
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	m, e := NewManager(GuardPolicy{ManagedRoot: filepath.Join(root, "work")})
	if e != nil {
		t.Fatal(e)
	}
	r := reposource.Repository{URL: raw, Authorization: &reposource.Authorization{Scope: "grant-103", Username: "oauth2", Token: "qa-private-repo-103", PrivateNetwork: true}}
	key := BindingKey{OrganizationID: "qa-private", SpaceID: "space", AgentID: "agent", ConversationID: "first"}
	cache := filepath.Join(root, "cache")
	a, e := m.PrepareRepositories(context.Background(), key, []reposource.Repository{r}, cache)
	if e != nil {
		t.Fatal(e)
	}
	a.Release()
	bundles, _ := filepath.Glob(filepath.Join(cache, hashedName(key.OrganizationID), "*.bundle"))
	if len(bundles) != 1 {
		t.Fatal("expected one authenticated bundle")
	}
	before, _ := os.Stat(bundles[0])
	key.ConversationID = "second"
	b, e := m.PrepareRepositories(context.Background(), key, []reposource.Repository{r}, cache)
	if e != nil {
		t.Fatal(e)
	}
	b.Release()
	after, _ := os.Stat(bundles[0])
	if a.Path == b.Path || !os.SameFile(before, after) {
		t.Fatal("cache not reused across isolated checkouts")
	}
	config, e := os.ReadFile(filepath.Join(b.Path, ".git", "config"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(config), r.Authorization.Token) || strings.Contains(string(config), "extraHeader") {
		t.Fatal("credential persisted in checkout")
	}
	r.Authorization.Token = "revoked-token"
	if _, e = m.PrepareRepositories(context.Background(), key, []reposource.Repository{r}, cache); e == nil {
		t.Fatal("cached workspace bypassed revoked credentials")
	}
}

func TestRepositoryIntranetAddressBoundary(t *testing.T) {
	for _, tc := range []struct {
		ip             string
		intranet, want bool
	}{
		{"10.0.0.2", true, true}, {"192.168.1.2", true, true}, {"fd00::1234", true, true},
		{"10.0.0.2", false, false}, {"127.0.0.1", true, false}, {"::1", true, false},
		{"169.254.169.254", true, false}, {"fd00:ec2::254", true, false}, {"224.0.0.1", true, false},
		{"8.8.8.8", false, true},
	} {
		if got := repositoryIPAllowed(net.ParseIP(tc.ip), tc.intranet); got != tc.want {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
}
