package imports

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"openfox/internal/imports"
	"openfox/internal/imports/jiraapi"
	"openfox/internal/integrations"
)

func TestSiteLegacyGrantAndSourceIdentity(t *testing.T) {
	e, hasher := newSiteEnv(t)
	opts := e.jira.safe()
	opts.LookupIP = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	server := e.newServer(e.box, opts, hasher)
	client := httpLogin(t, server.URL, "site-owner@example.test")
	e.worker.Safe = opts
	oldSite := strings.Replace(e.jira.srv.URL, "127.0.0.1", "EXAMPLE.COM", 1)
	canonical := strings.ToLower(oldSite)
	var id string
	if err := e.super.QueryRow(e.ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	oldGrant := map[string]any{"Provider": "jira", "Site": oldSite, "Identity": "admin", "Jira": jiraapi.Auth{Kind: "api_token", Email: "admin@jira.test", Secret: apiToken}}
	raw, _ := json.Marshal(oldGrant)
	sealed, err := e.box.Seal(raw, "integration:"+e.org+":"+id)
	if err != nil {
		t.Fatal(err)
	}
	blob := append([]byte(sealed.KeyID), sealed.Nonce...)
	blob = append(blob, sealed.Ciphertext...)
	hash := sha256.Sum256([]byte(oldSite + "\x00admin"))
	external := hex.EncodeToString(hash[:])
	e.exec(`INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,credentials_sealed,credentials_key_id,settings,created_by) VALUES($1::uuid,$2::uuid,'jira','ORGANIZATION',$3,'Legacy Jira',$4,$5,jsonb_build_object('auth_method','import_site','instance_url',$6::text),$7::uuid)`, e.org, id, external, blob, sealed.KeyID, oldSite, siteOwner)
	// A preexisting personal child must not be migrated, invalidated or replaced by
	// organization credential updates. This fixture deliberately needs no network.
	e.exec(`INSERT INTO openfox.integration_connections(organization_id,provider,scope,parent_connection_id,owner_user_id,external_id,display_name,created_by) VALUES($1::uuid,'jira','PERSONAL',$2::uuid,$3::uuid,'legacy-personal','Personal',$3::uuid)`, e.org, id, siteOwner)
	var found integrations.Connection
	client.json(200, "POST", e.prefix+"/integrations/jira/site", siteJiraRequest(canonical, "admin@jira.test"), &found)
	if found.ID != id || found.InstanceURL != oldSite || found.Version != 1 {
		t.Fatalf("legacy connection changed=%+v", found)
	}
	update := siteJiraRequest(canonical, "admin@jira.test")
	update["connection_id"], update["version"] = strings.ToUpper(id), found.Version
	client.json(200, "POST", e.prefix+"/integrations/jira/site", update, &found)
	if found.ID != id || found.InstanceURL != oldSite || found.Version != 2 {
		t.Fatalf("legacy update changed identity=%+v", found)
	}
	if e.str(`SELECT external_id FROM openfox.integration_connections WHERE id=$1::uuid`, id) != external {
		t.Fatal("legacy hash rewritten")
	}
	if e.num(`SELECT count(*) FROM openfox.integration_connections WHERE parent_connection_id=$1::uuid AND status='ACTIVE' AND version=1`, id) != 1 {
		t.Fatal("personal identity changed")
	}
	var j imports.Job
	client.json(201, "POST", e.prefix+"/imports/jira-api", map[string]any{"deployment": "cloud", "site_url": canonical, "auth": map[string]string{"kind": "api_token", "email": "admin@jira.test", "secret": apiToken}, "project_key": "PRJ", "new_space": map[string]string{"name": "Legacy source", "prefix": "LEG", "visibility": "SHARED"}}, &j)
	if j.SyncTicket != "" {
		t.Fatal("legacy raw import generated duplicate binding ticket")
	}
	if got := e.str(`SELECT preview->'api'->>'site_url' FROM openfox.import_jobs WHERE id=$1::uuid`, j.ID); got != oldSite {
		t.Fatalf("source namespace=%q", got)
	}
	for n := 0; n < 10 && j.Phase == "fetching"; n++ {
		if err := e.worker.RunOrganization(e.ctx, e.org); err != nil {
			t.Fatal(err)
		}
		client.json(200, "GET", e.prefix+"/imports/"+j.ID, nil, &j)
	}
	cfg := *j.Config
	cfg.Options.ContinuousSync = true
	client.json(200, "PUT", e.prefix+"/imports/"+j.ID+"/config", map[string]any{"config_version": j.ConfigVersion, "config": cfg}, &j)
	client.json(200, "POST", e.prefix+"/imports/"+j.ID+"/start", map[string]any{"config_version": j.ConfigVersion}, &j)
	if done := e.drain(j.ID); done.Status != "SUCCEEDED" {
		t.Fatalf("legacy source failed=%+v", done)
	}
	if e.str(`SELECT connection_id::text FROM openfox.import_sync_links WHERE job_id=$1::uuid`, j.ID) != id {
		t.Fatal("legacy sync parent changed")
	}
}
