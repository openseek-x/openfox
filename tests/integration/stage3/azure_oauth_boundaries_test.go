package stage3

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/url"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"sync"
	"testing"
)

func TestAzureOAuthCallbackPurposeReplayExpiryAndFailureCommit(t *testing.T) {
	e := newAzureOAuthEnv(t)
	client := e.owner()
	client.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	start := func() (integrations.StartResult, string) {
		var out integrations.StartResult
		client.must(200, "POST", "integrations/azure_devops/oauth/start", integrations.AzureStartInput{Purpose: "organization", TenantID: azureTestTenant}, &out)
		u, _ := url.Parse(out.AuthorizeURL)
		return out, u.Query().Get("state")
	}
	s, state := start()
	client.must(302, "GET", "/v1/integrations/azure_devops/identity/callback?state="+state+"&code=fixture", nil, nil)
	var status string
	e.scalar(&status, `SELECT status FROM openfox.integration_oauth_states WHERE id=$1::uuid`, s.SessionID)
	if status != "PENDING" {
		t.Fatal("wrong purpose consumed state")
	}
	client.must(302, "GET", "/v1/integrations/azure_devops/callback?state="+state+"&code=fixture", nil, nil)
	client.must(302, "GET", "/v1/integrations/azure_devops/callback?state="+state+"&code=fixture", nil, nil)
	e.scalar(&status, `SELECT status FROM openfox.integration_oauth_states WHERE id=$1::uuid`, s.SessionID)
	if status != "SELECTING" {
		t.Fatal("replay altered successful proof")
	}
	s, _ = start()
	e.exec(`UPDATE openfox.integration_oauth_states SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, s.SessionID)
	var view integrations.AzureSession
	client.must(200, "GET", "integrations/azure_devops/oauth/sessions/"+s.SessionID, nil, &view)
	if view.Status != "EXPIRED" {
		t.Fatal("expiry not surfaced")
	}
	if n := e.count(`SELECT count(*) FROM openfox.integration_oauth_states WHERE id=$1::uuid AND status='EXPIRED' AND pending_sealed IS NULL AND code_verifier_sealed IS NULL`, s.SessionID); n != 1 {
		t.Fatal("expiry rolled back")
	}
	s, state = start()
	e.fake.mu.Lock()
	e.fake.scope = "vso.profile vso.code_write"
	e.fake.mu.Unlock()
	client.must(302, "GET", "/v1/integrations/azure_devops/callback?state="+state+"&code=fixture", nil, nil)
	if n := e.count(`SELECT count(*) FROM openfox.integration_oauth_states WHERE id=$1::uuid AND status='FAILED' AND pending_sealed IS NULL AND code_verifier_sealed IS NULL`, s.SessionID); n != 1 {
		t.Fatal("failed proof rolled back or kept tokens")
	}
}
func TestAzureOAuthSameSessionConcurrentReauthorization(t *testing.T) {
	e := newAzureOAuthEnv(t)
	parent := e.connect(azureTestAccount)
	session := e.authorize(e.owner(), integrations.AzureStartInput{Purpose: "reauthorize", ConnectionID: parent.ID, Version: parent.Version})
	var target integrations.AzureCompleteTarget
	if err := e.tx(s3OwnerID, func(ctx context.Context, tx pgx.Tx, a collaboration.Actor) error {
		var e2 error
		target, e2 = e.svc.PrepareAzureComplete(ctx, tx, e.org, a, session.ID, integrations.AzureCompleteInput{AccountID: azureTestAccount})
		return e2
	}); err != nil {
		t.Fatal(err)
	}
	target, err := e.svc.VerifyAzureComplete(e.ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	var before int
	e.scalar(&before, `SELECT generation FROM openfox.integration_azure_grants WHERE organization_id=$1::uuid`, e.org)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- e.tx(s3OwnerID, func(ctx context.Context, tx pgx.Tx, a collaboration.Actor) error {
				_, _, e2 := e.svc.CompleteAzureOrganization(ctx, tx, e.org, a, session.ID, target)
				return e2
			})
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var after int
	e.scalar(&after, `SELECT generation FROM openfox.integration_azure_grants WHERE organization_id=$1::uuid`, e.org)
	if after != before+1 {
		t.Fatalf("reauth repeated generation %d -> %d", before, after)
	}
}

func TestAzureLateHTTPResultsLoseAuthority(t *testing.T) {
	for _, scenario := range []string{"actor", "parent", "complete"} {
		t.Run(scenario, func(t *testing.T) {
			e := newAzureOAuthEnv(t)
			parent := e.connect(azureTestAccount)
			caller := e.admin()
			path := "integrations/azure_devops/connections/" + parent.ID + "/projects"
			method := "GET"
			var body any
			if scenario == "complete" {
				session := e.authorize(caller, integrations.AzureStartInput{Purpose: "organization", TenantID: azureTestTenant})
				path = "integrations/azure_devops/oauth/sessions/" + session.ID + "/complete"
				method = "POST"
				body = integrations.AzureCompleteInput{AccountID: azureTestAccountB}
			}
			entered, released := make(chan struct{}), make(chan struct{})
			var once sync.Once
			e.fake.mu.Lock()
			e.fake.projectHook = func() { once.Do(func() { close(entered) }); <-released }
			e.fake.mu.Unlock()
			defer func() {
				select {
				case <-released:
				default:
					close(released)
				}
			}()
			result := make(chan s3Response, 1)
			go func() { result <- caller.do(method, path, body) }()
			select {
			case <-entered:
			case <-e.ctx.Done():
				t.Fatal("upstream never entered")
			}
			if scenario == "parent" {
				e.owner().must(200, "POST", "integrations/connections/"+parent.ID+"/disconnect", map[string]any{"version": parent.Version, "confirm_phrase": "断开 Azure DevOps"}, nil)
			} else {
				e.exec(`UPDATE openfox.organization_members SET role='MEMBER' WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, s3AdminID)
				var version int
				e.scalar(&version, `SELECT authorization_version FROM openfox.organization_members WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, s3AdminID)
				if version <= 1 {
					t.Fatal("fixture did not revoke actor version")
				}
			}
			close(released)
			response := <-result
			if response.Status != 401 && response.Status != 403 && response.Status != 409 {
				t.Fatalf("late authority result %d: %s", response.Status, response.Body)
			}
			if scenario == "complete" && e.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND external_id=$2`, e.org, azureTestAccountB) != 0 {
				t.Fatal("late completion created connection")
			}
		})
	}
}
