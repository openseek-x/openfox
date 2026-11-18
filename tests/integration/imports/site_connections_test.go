package imports

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/identity"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/platform/postgres"
	"openfox/internal/platform/safehttp"
	"openfox/internal/secrets"
)

const (
	siteOwner  = "00000000-0000-0000-0000-00000000f711"
	siteMember = "00000000-0000-0000-0000-00000000f712"
	siteGuest  = "00000000-0000-0000-0000-00000000f714"
)

func newSiteEnv(t *testing.T, lifetime ...time.Duration) (*apiEnv, identity.Argon2idHasher) {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	duration := 120 * time.Second
	if len(lifetime) > 0 {
		duration = lifetime[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	t.Cleanup(cancel)
	super, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = super.Close(context.Background()) })
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 12, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 15 * time.Second, LockTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash(httpPassword)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(bytes.Repeat([]byte{7}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e := &apiEnv{t: t, ctx: ctx, url: url, super: super, pool: pool, box: box}
	if err := super.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&e.org); err != nil {
		t.Fatal(err)
	}
	for id, email := range map[string]string{siteOwner: "site-owner@example.test", siteMember: "site-member@example.test", siteGuest: "site-guest@example.test"} {
		e.exec(`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2) ON CONFLICT(id) DO UPDATE SET email=excluded.email`, id, email)
		e.exec(`INSERT INTO openfox.password_credentials(user_id,password_hash) VALUES($1::uuid,$2) ON CONFLICT(user_id) DO UPDATE SET password_hash=excluded.password_hash,status='ACTIVE'`, id, hash)
	}
	e.exec(`INSERT INTO openfox.organizations(id,slug,name) VALUES($1::uuid,$2,'Site connections')`, e.org, "site-"+e.org[:8])
	e.exec(`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER'),($1::uuid,$3::uuid,'MEMBER'),($1::uuid,$4::uuid,'GUEST')`, e.org, siteOwner, siteMember, siteGuest)
	e.exec(`INSERT INTO openfox.task_statuses(organization_id,name,description,category,position,is_default) VALUES($1::uuid,'To Do','','TODO',0,true),($1::uuid,'Done','','DONE',1,false)`, e.org)
	e.jira = newFakeJira(t, "cloud")
	e.jira.actualCount, e.jira.countTotal = 1, 1
	e.prefix = "/v1/organizations/" + e.org
	e.server = e.newServer(box, e.jira.safe(), hasher)
	e.owner = httpLogin(t, e.server.URL, "site-owner@example.test")
	e.member = httpLogin(t, e.server.URL, "site-member@example.test")
	e.guest = httpLogin(t, e.server.URL, "site-guest@example.test")
	e.worker = &imports.Worker{Pool: pool, Instance: "sites", Secrets: box, Safe: e.jira.safe()}
	return e, hasher
}

func TestOrganizationSiteConnectionsHTTP(t *testing.T) {
	e, hasher := newSiteEnv(t)
	endpoint := e.prefix + "/integrations/jira/site"
	request := func(site, secret string) map[string]any {
		return map[string]any{"site_url": site, "deployment": "cloud", "jira": map[string]any{"Kind": "api_token", "Email": "admin@jira.test", "Secret": secret}}
	}
	var conn integrations.Connection
	t.Run("standalone connection persists without imports and keeps secrets encrypted", func(t *testing.T) {
		e.owner.json(200, "POST", endpoint, request(e.jira.srv.URL, apiToken), &conn)
		if conn.ID == "" || conn.InstanceURL != e.jira.srv.URL || conn.SiteIdentity != "admin" {
			t.Fatalf("connection=%+v", conn)
		}
		if e.num(`SELECT count(*) FROM openfox.import_jobs WHERE organization_id=$1::uuid`, e.org) != 0 {
			t.Fatal("binding created import")
		}
		var detail integrations.Detail
		e.owner.json(200, "GET", e.prefix+"/integrations/jira", nil, &detail)
		if !detail.Provider.Bindable || len(detail.Connections) != 1 {
			t.Fatalf("detail=%+v", detail)
		}
		raw := e.str(`SELECT settings::text||encode(credentials_sealed,'escape') FROM openfox.integration_connections WHERE id=$1::uuid`, conn.ID)
		if strings.Contains(raw, apiToken) {
			t.Fatal("plaintext persisted")
		}
	})
	t.Run("same identity reuses id and failed or stale reauthorization preserves credentials", func(t *testing.T) {
		var duplicate integrations.Connection
		e.owner.json(200, "POST", endpoint, request(e.jira.srv.URL+"/", apiToken), &duplicate)
		if duplicate.ID != conn.ID || duplicate.Version != conn.Version {
			t.Fatalf("duplicate=%+v", duplicate)
		}
		bad := request(e.jira.srv.URL, "wrong-secret")
		bad["connection_id"], bad["version"] = conn.ID, conn.Version
		e.owner.expect(400, "JIRA_CONNECTION_FAILED", "POST", endpoint, bad)
		update := request(e.jira.srv.URL, apiToken)
		update["connection_id"], update["version"] = conn.ID, conn.Version
		e.owner.json(200, "POST", endpoint, update, &duplicate)
		e.owner.expect(409, "VERSION_CONFLICT", "POST", endpoint, update)
		if duplicate.ID != conn.ID || duplicate.Version != conn.Version+1 {
			t.Fatal("update did not retain id and increment version")
		}
		conn = duplicate
	})
	t.Run("member guest cross organization and unconfigured encryption denied", func(t *testing.T) {
		e.member.expect(403, "FORBIDDEN", "POST", endpoint, request(e.jira.srv.URL, apiToken))
		e.guest.expect(403, "FORBIDDEN", "POST", endpoint, request(e.jira.srv.URL, apiToken))
		var other string
		if err := e.super.QueryRow(e.ctx, `SELECT gen_random_uuid()::text`).Scan(&other); err != nil {
			t.Fatal(err)
		}
		e.exec(`INSERT INTO openfox.organizations(id,slug,name) VALUES($1::uuid,$2,'Other site org')`, other, "other-"+other[:8])
		e.exec(`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER')`, other, siteOwner)
		cross := request(e.jira.srv.URL, apiToken)
		cross["connection_id"], cross["version"] = conn.ID, conn.Version
		e.owner.expect(404, "NOT_FOUND", "POST", "/v1/organizations/"+other+"/integrations/jira/site", cross)
		srv := e.newServer(nil, e.jira.safe(), hasher)
		client := httpLogin(t, srv.URL, "site-owner@example.test")
		client.expect(503, "SECRETS_UNAVAILABLE", "POST", endpoint, request(e.jira.srv.URL, apiToken))
		var detail integrations.Detail
		client.json(200, "GET", e.prefix+"/integrations/jira", nil, &detail)
		if detail.Provider.Bindable || detail.Provider.Configured {
			t.Fatal("no-key binding advertised")
		}
	})
	t.Run("selected and raw credentials share origin then real fetching import and sync", func(t *testing.T) {
		var j imports.Job
		e.owner.json(201, "POST", e.prefix+"/imports/jira-api", map[string]any{"connection_id": conn.ID, "project_key": "PRJ", "new_space": map[string]any{"name": "Connected import", "prefix": "SIT", "visibility": "SHARED"}}, &j)
		if j.SyncTicket != "" {
			t.Fatal("existing connection emitted new binding ticket")
		}
		if got := e.str(`SELECT preview->'project_origin'->>'connection' FROM openfox.import_jobs WHERE id=$1::uuid`, j.ID); got != conn.ID {
			t.Fatalf("origin=%s", got)
		}
		for n := 0; n < 10 && j.Phase == "fetching"; n++ {
			if err := e.worker.RunOrganization(e.ctx, e.org); err != nil {
				t.Fatal(err)
			}
			e.owner.json(200, "GET", e.prefix+"/imports/"+j.ID, nil, &j)
		}
		cfg := *j.Config
		cfg.Options.ContinuousSync = true
		e.owner.json(200, "PUT", e.prefix+"/imports/"+j.ID+"/config", map[string]any{"config_version": j.ConfigVersion, "config": cfg}, &j)
		e.owner.json(200, "POST", e.prefix+"/imports/"+j.ID+"/start", map[string]any{"config_version": j.ConfigVersion}, &j)
		done := e.drain(j.ID)
		if done.Status != "SUCCEEDED" || done.CreatedCount != 1 {
			t.Fatalf("done=%+v", done)
		}
		if got := e.str(`SELECT connection_id::text FROM openfox.import_sync_links WHERE job_id=$1::uuid`, j.ID); got != conn.ID {
			t.Fatal("sync uses different connection")
		}
		var second imports.Job
		e.owner.json(201, "POST", e.prefix+"/imports/jira-api", map[string]any{"deployment": "cloud", "site_url": e.jira.srv.URL + "/", "auth": map[string]any{"kind": "api_token", "email": "admin@jira.test", "secret": apiToken}, "project_key": "PRJ", "space_id": *done.SpaceID}, &second)
		if second.SyncTicket != "" || e.str(`SELECT preview->'project_origin'->>'connection' FROM openfox.import_jobs WHERE id=$1::uuid`, second.ID) != conn.ID {
			t.Fatal("raw entry did not reuse saved connection")
		}
		e.owner.json(200, "POST", e.prefix+"/imports/"+second.ID+"/cancel", map[string]any{"version": second.Version}, nil)
		e.exec(`UPDATE openfox.import_sync_links SET state='PAUSED' WHERE job_id=$1::uuid`, j.ID)
	})
	t.Run("concurrent creates remain unique", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make(chan int, 8)
		for n := 0; n < 8; n++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				status, _ := e.owner.do("POST", endpoint, request(e.jira.srv.URL, apiToken))
				results <- status
			}()
		}
		wg.Wait()
		close(results)
		for status := range results {
			if status != 200 && status != 409 {
				t.Fatalf("status=%d", status)
			}
		}
		if e.num(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='jira' AND status<>'DISCONNECTED'`, e.org) != 1 {
			t.Fatal("duplicate identity")
		}
	})
	t.Run("production transport rejects private addresses", func(t *testing.T) {
		srv := e.newServer(e.box, safehttp.Options{}, hasher)
		client := httpLogin(t, srv.URL, "site-owner@example.test")
		client.expect(400, "blocked_address", "POST", endpoint, request(e.jira.srv.URL, apiToken))
	})
	t.Run("ZenTao standalone identity and saved token import", func(t *testing.T) {
		zen := newSiteZen(t)
		var z integrations.Connection
		body := map[string]any{"site_url": zen.URL, "zen": map[string]string{"account": "zen-owner", "password": "zen-secret"}}
		e.owner.json(200, "POST", e.prefix+"/integrations/zentao/site", body, &z)
		var projects []map[string]string
		e.owner.json(200, "POST", e.prefix+"/imports/zentao-sources", map[string]string{"connection_id": z.ID}, &projects)
		if len(projects) != 1 {
			t.Fatalf("projects=%v", projects)
		}
		var j imports.Job
		e.owner.json(201, "POST", e.prefix+"/imports/zentao-import", map[string]any{"connection_id": z.ID, "project_id": "products/1", "new_space": map[string]any{"name": "Zen import", "prefix": "ZEN", "visibility": "SHARED"}}, &j)
		if j.SyncTicket != "" {
			t.Fatal("saved Zen connection emitted a ticket")
		}
		cfg := *j.Config
		cfg.Statuses = []imports.StatusMapping{{Source: "active", NewName: "Active", NewCategory: "TODO"}}
		e.owner.json(200, "PUT", e.prefix+"/imports/"+j.ID+"/config", map[string]any{"config_version": j.ConfigVersion, "config": cfg}, &j)
		e.owner.json(200, "POST", e.prefix+"/imports/"+j.ID+"/start", map[string]any{"config_version": j.ConfigVersion}, &j)
		if done := e.drain(j.ID); done.Status != "SUCCEEDED" || done.CreatedCount != 1 {
			t.Fatalf("done=%+v", done)
		}
		if e.num(`SELECT count(*) FROM openfox.import_sync_links WHERE job_id=$1::uuid`, j.ID) != 0 {
			t.Fatal("one-off import enabled sync")
		}
	})
}

func newSiteZen(t *testing.T) *httptest.Server {
	zen := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api.php/v1/tokens" {
			var in struct{ Account, Password string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Password != "zen-secret" {
				w.WriteHeader(401)
				return
			}
			writeJSONResponse(w, 200, map[string]string{"token": "token-" + in.Account})
			return
		}
		if r.Header.Get("Token") != "token-zen-owner" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api.php/v1/user":
			writeJSONResponse(w, 200, map[string]any{"profile": map[string]string{"account": "zen-owner"}})
		case "/api.php/v1/products":
			writeJSONResponse(w, 200, map[string]any{"total": 1, "products": []any{map[string]any{"id": 1, "name": "Product"}}})
		case "/api.php/v1/projects":
			writeJSONResponse(w, 200, map[string]any{"total": 0, "projects": []any{}})
		case "/api.php/v1/products/1/stories":
			writeJSONResponse(w, 200, map[string]any{"total": 1, "stories": []any{map[string]any{"id": 1, "title": "Zen imported story", "status": "active"}}})
		case "/api.php/v1/products/1/bugs":
			writeJSONResponse(w, 200, map[string]any{"total": 0, "bugs": []any{}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(zen.Close)
	return zen
}

func TestSiteNeedsAttentionKeepsLegacyOneOffImport(t *testing.T) {
	for _, provider := range []string{"jira", "zentao"} {
		t.Run(provider, func(t *testing.T) {
			e, _ := newSiteEnv(t)
			var conn integrations.Connection
			var raw map[string]any
			path := e.prefix + "/imports/jira-api"
			if provider == "jira" {
				e.owner.json(200, "POST", e.prefix+"/integrations/jira/site", siteJiraRequest(e.jira.srv.URL, "admin@jira.test"), &conn)
				raw = map[string]any{"site_url": e.jira.srv.URL, "deployment": "cloud", "auth": map[string]string{"kind": "api_token", "email": "admin@jira.test", "secret": apiToken}, "project_key": "PRJ"}
			} else {
				zen := newSiteZen(t)
				e.owner.json(200, "POST", e.prefix+"/integrations/zentao/site", map[string]any{"site_url": zen.URL, "zen": map[string]string{"account": "zen-owner", "password": "zen-secret"}}, &conn)
				path = e.prefix + "/imports/zentao-import"
				raw = map[string]any{"credentials": map[string]string{"site": zen.URL, "account": "zen-owner", "password": "zen-secret"}, "project_id": "products/1"}
			}
			e.exec(`UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='fixture credential expired' WHERE id=$1::uuid`, conn.ID)
			oldCipher := e.str(`SELECT encode(credentials_sealed,'hex') FROM openfox.integration_connections WHERE id=$1::uuid`, conn.ID)
			selected := map[string]any{"connection_id": conn.ID, "new_space": map[string]string{"name": "Unavailable", "prefix": "BAD", "visibility": "SHARED"}}
			if provider == "jira" {
				selected["project_key"] = "PRJ"
			} else {
				selected["project_id"] = "products/1"
			}
			e.owner.expect(409, "INTEGRATION_NEEDS_REAUTH", "POST", path, selected)
			for i := 0; i < 2; i++ {
				raw["new_space"] = map[string]string{"name": "One off", "prefix": fmt.Sprintf("ON%d", i), "visibility": "SHARED"}
				var j imports.Job
				e.owner.json(201, "POST", path, raw, &j)
				ticket := j.SyncTicket
				if ticket == "" {
					t.Fatal("raw legacy ticket lost")
				}
				for n := 0; n < 10 && j.Phase == "fetching"; n++ {
					if err := e.worker.RunOrganization(e.ctx, e.org); err != nil {
						t.Fatal(err)
					}
					e.owner.json(200, "GET", e.prefix+"/imports/"+j.ID, nil, &j)
				}
				cfg := *j.Config
				cfg.Options.ContinuousSync = i == 1
				if provider == "zentao" {
					cfg.Statuses = []imports.StatusMapping{{Source: "active", NewName: "Active", NewCategory: "TODO"}}
				}
				e.owner.json(200, "PUT", e.prefix+"/imports/"+j.ID+"/config", map[string]any{"config_version": j.ConfigVersion, "config": cfg}, &j)
				if i == 0 {
					e.owner.json(200, "POST", e.prefix+"/imports/"+j.ID+"/start", map[string]any{"config_version": j.ConfigVersion}, &j)
					if done := e.drain(j.ID); done.Status != "SUCCEEDED" {
						t.Fatalf("oneoff=%+v", done)
					}
				} else {
					e.owner.expect(409, "INTEGRATION_NEEDS_REAUTH", "POST", e.prefix+"/imports/"+j.ID+"/start", map[string]any{"config_version": j.ConfigVersion, "sync_ticket": ticket})
				}
			}
			if e.num(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid`, e.org) != 1 {
				t.Fatal("implicit replacement")
			}
			if e.num(`SELECT version FROM openfox.integration_connections WHERE id=$1::uuid`, conn.ID) != int(conn.Version) || e.str(`SELECT encode(credentials_sealed,'hex') FROM openfox.integration_connections WHERE id=$1::uuid`, conn.ID) != oldCipher {
				t.Fatal("raw import mutated saved credentials")
			}
			if e.str(`SELECT status FROM openfox.integration_connections WHERE id=$1::uuid`, conn.ID) != "NEEDS_ATTENTION" {
				t.Fatal("implicit reactivation")
			}
		})
	}
}
