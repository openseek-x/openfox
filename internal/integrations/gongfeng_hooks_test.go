package integrations

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/gongfeng"
	"openfox/internal/platform/postgres"
)

func gongfengTLSClient(t *testing.T, handler http.HandlerFunc) *gongfeng.Client {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "git.tencent.com"}, DNSNames: []string{"git.tencent.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	client := gongfeng.New()
	transport := client.HTTP.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "git.tencent.com:443" {
			t.Errorf("unexpected origin %q", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	client.HTTP.Transport = transport
	t.Cleanup(transport.CloseIdleConnections)
	return client
}

type gongfengHookFixture struct {
	mu                                 sync.Mutex
	hook                               map[string]any
	posts, puts, activations, requests int
	unknownCreate                      bool
	hideHooks                          bool
	active                             bool
	path                               string
	visibility                         *int
	postEntered, postRelease           chan struct{}
}

func (f *gongfengHookFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/v3/user":
		json.NewEncoder(w).Encode(map[string]any{"id": 71, "username": "fixture", "state": "active"})
	case r.Method == "GET" && r.URL.Path == "/api/v3/projects/81":
		f.mu.Lock()
		path := f.path
		visibility := 0
		if f.visibility != nil {
			visibility = *f.visibility
		}
		f.mu.Unlock()
		if path == "" {
			path = "owner/project"
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 81, "path_with_namespace": path, "archived": false, "issues_enabled": true, "visibility_level": visibility, "public": visibility == 20})
	case r.Method == "GET" && r.URL.Path == "/api/v3/projects/81/members/all":
		json.NewEncoder(w).Encode([]any{map[string]any{"id": 71, "username": "fixture", "state": "active", "access_level": 40, "expires_at": nil}})
	case r.Method == "GET" && r.URL.Path == "/api/v3/projects/81/members/all/71":
		json.NewEncoder(w).Encode(map[string]any{"id": 71, "username": "fixture", "state": "active", "access_level": 40, "expires_at": nil})
	case r.Method == "GET" && r.URL.Path == "/api/v3/projects/81/hooks":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.hook == nil || f.hideHooks {
			json.NewEncoder(w).Encode([]any{})
		} else {
			json.NewEncoder(w).Encode([]any{f.hook})
		}
	case r.Method == "POST" && r.URL.Path == "/api/v3/projects/81/hooks":
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		f.posts++
		body["id"] = 41
		body["project_id"] = 81
		body["active"] = false
		delete(body, "token")
		f.hook = body
		unknown, entered, release := f.unknownCreate, f.postEntered, f.postRelease
		f.mu.Unlock()
		if entered != nil {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if unknown {
			w.WriteHeader(502)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(body)
	case r.Method == "PUT" && r.URL.Path == "/api/v3/projects/81/hooks/41":
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.puts++
		body["id"] = 41
		body["project_id"] = 81
		body["active"] = false
		delete(body, "token")
		f.hook = body
		json.NewEncoder(w).Encode(body)
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/activate"):
		f.mu.Lock()
		f.activations++
		if f.hook != nil {
			f.hook["active"] = f.active
		}
		f.mu.Unlock()
		json.NewEncoder(w).Encode(false)
	case r.Method == "GET" && r.URL.Path == "/api/v3/projects/81/hooks/41":
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.hook == nil {
			w.WriteHeader(404)
		} else {
			json.NewEncoder(w).Encode(f.hook)
		}
	case r.Method == "DELETE" && r.URL.Path == "/api/v3/projects/81/hooks/41":
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hook = nil
		json.NewEncoder(w).Encode(true)
	default:
		w.WriteHeader(404)
	}
}

func TestGongfengResumePreservesUncertainCreate(t *testing.T) {
	for _, inFlight := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "in_flight"}[inFlight], func(t *testing.T) {
			f := newGongfengDBFixture(t)
			remote := &gongfengHookFixture{unknownCreate: true}
			if inFlight {
				remote.postEntered = make(chan struct{})
				remote.postRelease = make(chan struct{})
			}
			f.service.Gongfeng = gongfengTLSClient(t, remote.serve)
			c := f.connection(71)
			r, e := f.repo(c, 81)
			if e != nil {
				t.Fatal(e)
			}
			w := f.worker()
			if e = w.RunGongfengHooks(context.Background(), f.org); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { done <- w.RunGongfengHooks(context.Background(), f.org) }()
			if inFlight {
				select {
				case <-remote.postEntered:
				case <-time.After(5 * time.Second):
					t.Fatal("POST did not start")
				}
			} else {
				if e = <-done; e != nil {
					t.Fatal(e)
				}
			}
			r = f.readRepo(c.ID, r.ID)
			before := r.HookState
			paused := false
			e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
				var e error
				r, e = f.service.UpdateGongfengRepository(ctx, tx, f.org, f.actor, c.ID, r.ID, GongfengRepositoryUpdate{ConnectionVersion: c.Version, Generation: r.Generation, Paused: &paused})
				return e
			})
			if e != nil {
				t.Fatal(e)
			}
			if r.HookState != before {
				t.Fatalf("resume replaced uncertain %s with %s", before, r.HookState)
			}
			if inFlight {
				close(remote.postRelease)
				if e = <-done; e != nil {
					t.Fatal(e)
				}
			}
			remote.mu.Lock()
			remote.hideHooks = true
			remote.mu.Unlock()
			for i := 0; i < 3; i++ {
				f.advanceHook(r.ID)
				if e = w.RunGongfengHooks(context.Background(), f.org); e != nil {
					t.Fatal(e)
				}
			}
			remote.mu.Lock()
			posts := remote.posts
			remote.mu.Unlock()
			if posts != 1 {
				t.Fatalf("resume caused %d POSTs", posts)
			}
			r = f.readRepo(c.ID, r.ID)
			if r.HookState != "UNKNOWN" {
				t.Fatal("uncertainty was lost", r.HookState)
			}
		})
	}
}
func (f *gongfengDBFixture) worker() *SyncWorker {
	f.t.Helper()
	pool, err := postgres.New(context.Background(), postgres.Config{URL: f.dsn, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 10 * time.Second, LockTimeout: 3 * time.Second})
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(pool.Close)
	return &SyncWorker{Pool: pool, Service: f.service, Instance: "i10-controlled"}
}
func (f *gongfengDBFixture) advanceHook(id string) {
	f.t.Helper()
	if err := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.integration_gongfeng_repositories SET hook_next_attempt_at=clock_timestamp(),lease_expires_at=CASE WHEN lease_token IS NOT NULL THEN clock_timestamp()-interval '1 second' ELSE NULL END WHERE id=$1::uuid`, id)
		return e
	}); err != nil {
		f.t.Fatal(err)
	}
}
func (f *gongfengDBFixture) readRepo(connection, id string) GongfengRepository {
	f.t.Helper()
	var r GongfengRepository
	if err := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		r, e = loadGongfengRepository(ctx, tx, connection, id, false)
		return e
	}); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func TestGongfengHookUnknownActivationAndDisconnect(t *testing.T) {
	f := newGongfengDBFixture(t)
	remote := &gongfengHookFixture{unknownCreate: true}
	f.service.Gongfeng = gongfengTLSClient(t, remote.serve)
	c := f.connection(71)
	r, err := f.repo(c, 81)
	if err != nil {
		t.Fatal(err)
	}
	worker := f.worker()
	run := func(want string) {
		t.Helper()
		remote.mu.Lock()
		before := remote.requests
		remote.mu.Unlock()
		if err := worker.RunGongfengHooks(context.Background(), f.org); err != nil {
			t.Fatal(err)
		}
		r = f.readRepo(c.ID, r.ID)
		if r.HookState != want {
			t.Fatalf("hook state %s want %s, reason %s", r.HookState, want, r.StatusReason)
		}
		remote.mu.Lock()
		delta := remote.requests - before
		remote.mu.Unlock()
		if delta > 5 {
			t.Fatal("request budget exceeded", delta)
		}
	}
	run("CREATE_PENDING")
	run("UNKNOWN")
	f.advanceHook(r.ID)
	run("UPDATE_PENDING")
	run("ACTIVATE_PENDING")
	run("VERIFY_PENDING")
	run("ACTIVATE_PENDING")
	if r.Status == "ACTIVE" {
		t.Fatal("false activation accepted")
	}
	remote.mu.Lock()
	remote.active = true
	remote.mu.Unlock()
	f.advanceHook(r.ID)
	run("VERIFY_PENDING")
	run("ACTIVE")
	remote.mu.Lock()
	posts, puts := remote.posts, remote.puts
	remote.mu.Unlock()
	if posts != 1 || puts != 1 {
		t.Fatal("unknown create replayed", posts, puts)
	}
	var cfg GongfengConfiguration
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		cfg, e = f.service.GetGongfengConfiguration(ctx, tx, f.org, f.actor, c.ID, "")
		if e != nil {
			return e
		}
		raw, _ := json.Marshal(cfg)
		if strings.Contains(string(raw), "hooks/git/") || strings.Contains(string(raw), "hook_url_hash") || strings.Contains(string(raw), "fixture-pat") {
			t.Fatal("configuration leaked callback/PAT")
		}
		_, e = f.service.Disconnect(ctx, tx, f.org, f.actor, c.ID, c.Version, ConfirmPhrase("腾讯工蜂"))
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		cfg, e = f.service.GetGongfengConfiguration(ctx, tx, f.org, f.actor, c.ID, "")
		if e != nil {
			return e
		}
		if len(cfg.Repositories) != 1 || cfg.Repositories[0].HookID == nil || cfg.Repositories[0].RepositoryURL != "https://git.tencent.com/owner/project" {
			t.Fatal("cleanup information missing")
		}
		var sealed, hash []byte
		e = tx.QueryRow(ctx, `SELECT credentials_sealed,webhook_token_hash FROM openfox.integration_connections WHERE id=$1::uuid`, c.ID).Scan(&sealed, &hash)
		if sealed != nil || hash != nil {
			t.Fatal("disconnect retained credential")
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	before := remote.requests
	remote.mu.Unlock()
	if err = worker.RunGongfengHooks(context.Background(), f.org); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.requests != before {
		t.Fatal("disconnected worker called platform")
	}
}

func TestGongfengHookLateCreateAndRotationFence(t *testing.T) {
	f := newGongfengDBFixture(t)
	remote := &gongfengHookFixture{active: true, postEntered: make(chan struct{}), postRelease: make(chan struct{})}
	f.service.Gongfeng = gongfengTLSClient(t, remote.serve)
	c := f.connection(71)
	r, err := f.repo(c, 81)
	if err != nil {
		t.Fatal(err)
	}
	worker := f.worker()
	if err = worker.RunGongfengHooks(context.Background(), f.org); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.RunGongfengHooks(context.Background(), f.org) }()
	select {
	case <-remote.postEntered:
	case <-time.After(3 * time.Second):
		close(remote.postRelease)
		t.Fatal("POST did not start")
	}
	err = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.RotateGongfengWebhook(ctx, tx, f.org, f.actor, c.ID, c.Version)
		return e
	})
	close(remote.postRelease)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("late worker did not finish")
	}
	r = f.readRepo(c.ID, r.ID)
	if r.Status == "ACTIVE" || r.HookState != "UNKNOWN" || r.HookID == nil || *r.HookID != 41 {
		t.Fatal("stale create activated", r.Status, r.HookState)
	}
	f.advanceHook(r.ID)
	if err = worker.RunGongfengHooks(context.Background(), f.org); err != nil {
		t.Fatal(err)
	}
	r = f.readRepo(c.ID, r.ID)
	if r.HookState != "ACTIVATE_PENDING" || r.HookID == nil || *r.HookID != 41 {
		t.Fatal("old URL hash did not recover created hook", r.HookState)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.posts != 1 {
		t.Fatal("late create duplicated", remote.posts)
	}
}

func TestGongfengChangedVisibilityPreservesOwnedHookCleanup(t *testing.T) {
	f := newGongfengDBFixture(t)
	remote := &gongfengHookFixture{active: true}
	f.service.Gongfeng = gongfengTLSClient(t, remote.serve)
	c := f.connection(71)
	r, e := f.repo(c, 81)
	if e != nil {
		t.Fatal(e)
	}
	w := f.worker()
	for i := 0; i < 4; i++ {
		f.advanceHook(r.ID)
		if e = w.RunGongfengHooks(context.Background(), f.org); e != nil {
			t.Fatal(e)
		}
	}
	r = f.readRepo(c.ID, r.ID)
	if r.HookState != "ACTIVE" || r.HookID == nil {
		t.Fatal(r)
	}
	remote.mu.Lock()
	visibility := 20
	remote.visibility = &visibility
	remote.mu.Unlock()
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.RetryGongfengRepository(ctx, tx, f.org, f.actor, c.ID, r.ID, GongfengRepositoryUpdate{ConnectionVersion: c.Version, Generation: r.Generation})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.RunGongfengHooks(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	r = f.readRepo(c.ID, r.ID)
	if r.StatusReason != "repository_changed" || r.Status != "NEEDS_ATTENTION" {
		t.Fatal(r)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.RemoveGongfengRepository(ctx, tx, f.org, f.actor, c.ID, r.ID, GongfengRepositoryUpdate{ConnectionVersion: c.Version, Generation: r.Generation})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.RunGongfengHooks(context.Background(), f.org); e != nil {
		t.Fatal(e)
	}
	r = f.readRepo(c.ID, r.ID)
	if r.HookState != "DELETED" || r.Status != "REMOVED" {
		t.Fatal(r)
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if remote.hook != nil {
		t.Fatal("owned remote hook not deleted")
	}
}
