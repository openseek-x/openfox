package stage3

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/integrations"
	"openfox/internal/integrations/azuredevops"
	"openfox/internal/secrets"
)

const azureTestApp = "11111111-1111-4111-8111-111111111111"
const azureTestTenant = "22222222-2222-4222-8222-222222222222"
const azureTestSubject = "33333333-3333-4333-8333-333333333333"
const azureTestAccount = "44444444-4444-4444-8444-444444444444"
const azureTestScope = "vso.profile vso.code vso.threads_full vso.work vso.identity"

type azureGrantEnv struct {
	*s3Env
	runtime                  integrations.AzureRuntime
	svc                      integrations.Service
	mu                       sync.Mutex
	refreshes, profiles      int
	expires                  int
	refreshError             string
	refreshHook, profileHook func()
}

func newAzureGrantEnv(t *testing.T) *azureGrantEnv {
	e := &azureGrantEnv{s3Env: newS3Env(t), expires: 3600}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		e.mu.Lock()
		expires, code := e.expires, e.refreshError
		rh, ph := e.refreshHook, e.profileHook
		e.mu.Unlock()
		switch r.Host {
		case "login.microsoftonline.com":
			if r.Method != "POST" || r.URL.Path != "/"+azureTestTenant+"/oauth2/v2.0/token" {
				t.Error("unverified OAuth origin/path")
			}
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") == "" {
				t.Error("refresh form")
			}
			e.mu.Lock()
			e.refreshes++
			e.mu.Unlock()
			if rh != nil {
				rh()
			}
			if code != "" {
				w.WriteHeader(400)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": code})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh-token", "expires_in": expires, "token_type": "Bearer", "scope": azureTestScope})
		case "app.vssps.visualstudio.com":
			e.mu.Lock()
			e.profiles++
			e.mu.Unlock()
			if ph != nil {
				ph()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": azureTestSubject, "displayName": "Fixture"})
		default:
			t.Errorf("unexpected fixed host %s", r.Host)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	tr.TLSClientConfig.ServerName = srv.Certificate().DNSNames[0]
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	t.Cleanup(tr.CloseIdleConnections)
	key := make([]byte, secrets.KeySize)
	_, _ = rand.Read(key)
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	e.svc = integrations.Service{Azure: &azuredevops.Client{App: azuredevops.App{ClientID: azureTestApp, ClientSecret: "fixture-secret"}, Transport: tr}, Box: box, PublicURL: "http://localhost:18149"}
	e.runtime = integrations.AzureRuntime{Pool: e.database, Service: e.svc}
	return e
}
func (e *azureGrantEnv) seed(expired bool) (string, string) {
	grant, parent := s3NewID(e.t), s3NewID(e.t)
	plain, _ := json.Marshal(map[string]string{"access_token": strings.Repeat("a", 9000), "refresh_token": "fixture-refresh"})
	sealed, err := e.svc.Box.SealOAuth(plain, "azure-devops-grant:"+e.org+":"+grant)
	if err != nil {
		e.t.Fatal(err)
	}
	blob := append([]byte(sealed.KeyID), sealed.Nonce...)
	blob = append(blob, sealed.Ciphertext...)
	expires := time.Now().Add(time.Hour)
	if expired {
		expires = time.Now().Add(-time.Second)
	}
	e.exec(`INSERT INTO openfox.integration_azure_grants(organization_id,id,app_client_id,resource_tenant_id,subject_id,subject_name,scope,credentials_sealed,credentials_key_id,expires_at,status,created_by) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'Fixture',$6,$7,$8,$9,'ACTIVE',$10::uuid)`, e.org, grant, azureTestApp, azureTestTenant, s3NewID(e.t), azureTestScope, blob, sealed.KeyID, expires, s3OwnerID)
	e.exec(`INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,account_kind,external_id,display_name,status,settings,created_by,azure_grant_id) VALUES($1::uuid,$2::uuid,'azure_devops','ORGANIZATION','organization',$3,'Fixture','ACTIVE',jsonb_build_object('auth_method','azure_devops_oauth','organization_name','fixture-org'),$4::uuid,$5::uuid)`, e.org, parent, s3NewID(e.t), s3OwnerID, grant)
	return grant, parent
}
func (e *azureGrantEnv) read(parent string) error {
	_, err := e.runtime.Do(e.ctx, e.org, parent, 1, 1, func(ctx context.Context, c *azuredevops.Client, token string) error {
		_, err := c.Profile(ctx, token)
		return err
	})
	return err
}

func TestAzureGrantRefreshLeaseShortTokenAndAttentionCommit(t *testing.T) {
	e := newAzureGrantEnv(t)
	grant, parent := e.seed(true)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	e.mu.Lock()
	e.expires = 60
	e.refreshHook = func() { close(entered); <-release }
	e.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- e.read(parent) }()
	<-entered
	var retry *integrations.AzureRetry
	if err := e.read(parent); !errors.As(err, &retry) {
		t.Errorf("concurrent refresh did not defer: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var cred, gen, ver int
	e.scalar(&cred, `SELECT credentials_version FROM openfox.integration_azure_grants WHERE id=$1`, grant)
	e.scalar(&gen, `SELECT generation FROM openfox.integration_azure_grants WHERE id=$1`, grant)
	e.scalar(&ver, `SELECT version FROM openfox.integration_connections WHERE id=$1`, parent)
	e.mu.Lock()
	refreshes, profiles := e.refreshes, e.profiles
	e.refreshHook = nil
	e.mu.Unlock()
	if refreshes != 1 || profiles != 1 || cred != 2 || gen != 1 || ver != 1 {
		t.Fatalf("refresh=%d profiles=%d credential=%d generation=%d parent=%d", refreshes, profiles, cred, gen, ver)
	}
	for _, code := range []string{"invalid_grant", "interaction_required", "consent_required"} {
		t.Run(code, func(t *testing.T) {
			g, p := e.seed(true)
			e.mu.Lock()
			e.refreshError = code
			e.mu.Unlock()
			if err := e.read(p); err == nil {
				t.Fatal("refresh error accepted")
			}
			var status, reason string
			e.scalar(&status, `SELECT status FROM openfox.integration_azure_grants WHERE id=$1`, g)
			e.scalar(&reason, `SELECT status_reason FROM openfox.integration_connections WHERE id=$1`, p)
			if status != "NEEDS_ATTENTION" || reason != "reauthorization_required" {
				t.Fatalf("failure rolled back: %s %s", status, reason)
			}
			if e.count(`SELECT count(*) FROM openfox.integration_azure_grants WHERE id=$1 AND refresh_lease IS NOT NULL`, g) != 0 {
				t.Fatal("failure retained refresh lease")
			}
		})
	}
}

func TestAzureGrantLateParentResponseReleasesLeaseAndIsRejected(t *testing.T) {
	e := newAzureGrantEnv(t)
	g, p := e.seed(false)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	e.mu.Lock()
	e.profileHook = func() { close(entered); <-release }
	e.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- e.read(p) }()
	<-entered
	e.exec(`UPDATE openfox.integration_connections SET status='DISCONNECTED',disconnected_at=clock_timestamp(),version=version+1 WHERE id=$1`, p)
	close(release)
	if err := <-done; !errors.Is(err, integrations.ErrVersionConflict) {
		t.Fatalf("late result escaped: %v", err)
	}
	if e.count(`SELECT count(*) FROM openfox.integration_azure_grants WHERE id=$1 AND request_lease IS NOT NULL`, g) != 0 {
		t.Fatal("late failure rolled back lease release")
	}
}

func TestAzureGrantMaintenanceIndependentGrantProgress(t *testing.T) {
	for _, mode := range []string{"busy", "app_changed"} {
		t.Run(mode, func(t *testing.T) {
			e := newAzureGrantEnv(t)
			g1, p1 := e.seed(true)
			g2, p2 := e.seed(true)
			if g1 > g2 {
				g1, g2 = g2, g1
				p1, p2 = p2, p1
			}
			if mode == "busy" {
				e.exec(`UPDATE openfox.integration_azure_grants SET request_lease=$2::uuid,request_lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, g1, s3NewID(t))
			} else {
				e.exec(`UPDATE openfox.integration_azure_grants SET app_client_id=$2::uuid WHERE id=$1`, g1, s3NewID(t))
			}
			err := e.runtime.MaintainOrganization(e.ctx, e.org)
			if mode == "busy" && err != nil || mode == "app_changed" && err == nil {
				t.Fatalf("unexpected aggregate %v", err)
			}
			var cred uint64
			e.scalar(&cred, `SELECT credentials_version FROM openfox.integration_azure_grants WHERE id=$1`, g2)
			if cred != 2 {
				t.Fatalf("second grant blocked: credentials_version %d", cred)
			}
			e.mu.Lock()
			n := e.refreshes
			e.mu.Unlock()
			if n != 1 {
				t.Fatalf("refresh count %d", n)
			}
		})
	}
}
