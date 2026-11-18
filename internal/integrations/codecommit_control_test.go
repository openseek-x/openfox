package integrations

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/codecommit"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

type codeCommitDBFixture struct {
	t                     *testing.T
	dsn, org, user, space string
	service               Service
	actor                 collaboration.Actor
}

func newCodeCommitDBFixture(t *testing.T) *codeCommitDBFixture {
	t.Helper()
	dsn := os.Getenv("OPENFOX_I13_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires exclusive I13 database")
	}
	f := &codeCommitDBFixture{t: t, dsn: dsn, org: refsTestID(t), user: refsTestID(t), space: refsTestID(t)}
	c, e := pgx.Connect(context.Background(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	var name string
	if e = c.QueryRow(context.Background(), `SELECT current_database()`).Scan(&name); e != nil || !strings.HasPrefix(name, "i13_") {
		t.Fatal("requires explicit exclusive I13 database")
	}
	queries := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, []any{f.user, f.user + "@example.test"}},
		{`INSERT INTO openfox.organizations(id,slug,name) VALUES($1::uuid,$2,'I13 fixture')`, []any{f.org, f.org}},
		{`INSERT INTO openfox.organization_directory(organization_id,slug,name,created_at) SELECT id,slug,name,created_at FROM openfox.organizations WHERE id=$1::uuid`, []any{f.org}},
		{`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'OWNER')`, []any{f.org, f.user}},
		{`INSERT INTO openfox.spaces(organization_id,id,prefix,name,visibility) VALUES($1::uuid,$2::uuid,'CCTEST','I13','PRIVATE')`, []any{f.org, f.space}},
		{`INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES($1::uuid,$2::uuid,$3::uuid,'OWNER')`, []any{f.org, f.space, f.user}},
		{`INSERT INTO openfox.task_statuses(organization_id,name,category,position) VALUES($1::uuid,'Todo','TODO',0),($1::uuid,'Doing','IN_PROGRESS',1),($1::uuid,'Done','DONE',2)`, []any{f.org}},
		{`INSERT INTO openfox.task_types(organization_id,space_id,name,is_default) VALUES($1::uuid,$2::uuid,'Task',true)`, []any{f.org, f.space}},
		{`INSERT INTO openfox.workflow_statuses(organization_id,space_id,task_type_id,status_id,position,is_default) SELECT t.organization_id,t.space_id,t.id,s.id,s.position,s.category='TODO' FROM openfox.task_types t JOIN openfox.task_statuses s ON s.organization_id=t.organization_id WHERE t.organization_id=$1::uuid`, []any{f.org}},
	}
	for _, q := range queries {
		if _, e = c.Exec(context.Background(), q.sql, q.args...); e != nil {
			t.Fatal(e)
		}
	}
	key := make([]byte, 32)
	if _, e = rand.Read(key); e != nil {
		t.Fatal(e)
	}
	box, e := secrets.New(key)
	if e != nil {
		t.Fatal(e)
	}
	f.service = Service{Box: box, PublicURL: "https://openfox.example.test"}
	f.actor = collaboration.Actor{UserID: f.user, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	return f
}
func (f *codeCommitDBFixture) run(org string, fn func(context.Context, pgx.Tx) error) error {
	tenant, e := tenancy.NewTenantContext(tenancy.OrganizationSelection{OrganizationID: org, ActorID: f.user, AuthorizationVersion: 1}, tenancy.SourceUserRequest)
	if e != nil {
		return e
	}
	ctx := tenancy.WithTenantContext(context.Background(), tenant)
	c, e := pgx.Connect(ctx, f.dsn)
	if e != nil {
		return e
	}
	defer c.Close(ctx)
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, org, f.user); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `SET LOCAL ROLE openfox_app`); e != nil {
		return e
	}
	if e = fn(ctx, tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (f *codeCommitDBFixture) exec(sql string, args ...any) {
	f.t.Helper()
	ctx := context.Background()
	c, e := pgx.Connect(ctx, f.dsn)
	if e != nil {
		f.t.Fatal(e)
	}
	defer c.Close(ctx)
	tx, e := c.Begin(ctx)
	if e != nil {
		f.t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, f.org, f.user); e != nil {
		f.t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, sql, args...); e != nil {
		f.t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		f.t.Fatal(e)
	}
}

func ccFixtureInput() CodeCommitCredentialInput {
	return CodeCommitCredentialInput{Region: "us-east-1", AccessKeyID: "AKIA0123456789ABCDEF", SecretAccessKey: "synthetic-secret-key"}
}
func ccFixtureIdentity() CodeCommitVerifiedCredentials {
	return CodeCommitVerifiedCredentials{Identity: codecommit.Identity{Account: "123456789012", ARN: "arn:aws:iam::123456789012:user/test", UserID: "synthetic-user"}}
}
func (f *codeCommitDBFixture) connection() Connection {
	f.t.Helper()
	var c Connection
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		p, e := f.service.PrepareCodeCommitCredentials(ctx, tx, f.org, f.actor, "", ccFixtureInput())
		if e != nil {
			return e
		}
		c, e = f.service.SaveCodeCommitCredentials(ctx, tx, f.org, f.actor, p, ccFixtureIdentity())
		return e
	})
	if e != nil {
		f.t.Fatal(e)
	}
	return c
}
func (f *codeCommitDBFixture) access(c Connection) CodeCommitAccess {
	f.t.Helper()
	var a CodeCommitAccess
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		a, e = f.service.PrepareCodeCommit(ctx, tx, f.org, f.actor, c.ID)
		return e
	})
	if e != nil {
		f.t.Fatal(e)
	}
	return a
}
func (f *codeCommitDBFixture) repository(a CodeCommitAccess, id, name string) (CodeCommitBridgeResult, error) {
	var r CodeCommitBridgeResult
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		r, e = f.service.SaveCodeCommitRepository(ctx, tx, f.org, f.actor, a, CodeCommitRepositoryInput{ConnectionVersion: a.Version, Name: name, ConfirmInitialization: true}, codecommit.Repository{ID: id, Name: name, AccountID: a.settings.Account, ARN: "arn:aws:codecommit:" + a.settings.Region + ":" + a.settings.Account + ":" + name})
		return e
	})
	return r, e
}
func ccEvent(a CodeCommitAccess, r CodeCommitRepository, id string, pr bool) []byte {
	var d map[string]any
	typ := "CodeCommit Repository State Change"
	if pr {
		typ = "CodeCommit Pull Request State Change"
		d = map[string]any{"event": "pullRequestCreated", "repositoryNames": []string{r.Name}, "pullRequestId": "7"}
	} else {
		d = map[string]any{"event": "referenceUpdated", "repositoryName": r.Name, "repositoryId": r.RepositoryID, "referenceType": "branch", "referenceName": "main", "referenceFullName": "refs/heads/main", "oldCommitId": strings.Repeat("a", 40), "commitId": strings.Repeat("b", 40)}
	}
	b, _ := json.Marshal(map[string]any{"version": "0", "id": id, "detail-type": typ, "source": "aws.codecommit", "account": a.settings.Account, "region": a.settings.Region, "time": time.Now().UTC().Format(time.RFC3339), "resources": []string{r.ARN}, "detail": d})
	return b
}
func (f *codeCommitDBFixture) receive(a CodeCommitAccess, r CodeCommitBridgeResult, b []byte) (CodeCommitReceipt, error) {
	var out CodeCommitReceipt
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = f.service.ReceiveCodeCommit(ctx, tx, CodeCommitBridgeLookup{f.org, a.ID, r.Repository.ID}, r.Repository.BridgeID, r.Token, b)
		return e
	})
	return out, e
}

func TestCodeCommitCursorAndOpaqueMarkers(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, _ := secrets.New(key)
	s := Service{Box: box}
	a := CodeCommitAccess{ID: refsTestID(t), Version: 2, settings: codeCommitSettings{Account: "123456789012", Region: "us-east-1"}}
	org := refsTestID(t)
	raw, e := s.EncodeCodeCommitCursor(org, a, "next/+opaque")
	if e != nil {
		t.Fatal(e)
	}
	next, e := s.DecodeCodeCommitCursor(org, a, raw)
	if e != nil || next != "next/+opaque" {
		t.Fatal("cursor round trip", e)
	}
	for _, v := range []CodeCommitAccess{{ID: a.ID, Version: 3}, {ID: refsTestID(t), Version: a.Version}} {
		if _, e = s.DecodeCodeCommitCursor(org, v, raw); e == nil {
			t.Fatal("cursor fence ignored")
		}
	}
	if _, e = s.DecodeCodeCommitCursor(refsTestID(t), a, raw); e == nil {
		t.Fatal("cross tenant cursor")
	}
	plain, _ := json.Marshal(codeCommitCursor{a.ID, a.Version, "next", time.Now().Add(-time.Minute).Unix()})
	sealed, _, _ := s.seal(plain, "codecommit-repositories:"+org+":"+a.ID)
	if _, e = s.DecodeCodeCommitCursor(org, a, base64.RawURLEncoding.EncodeToString(sealed)); e == nil {
		t.Fatal("expired cursor")
	}
	upper := codeCommitRepositoryKey(a, strings.Repeat("X", 256))
	lower := codeCommitRepositoryKey(a, strings.Repeat("x", 256))
	if len(upper) > 255 || strings.EqualFold(upper, lower) {
		t.Fatal("opaque ID alias or overlong key")
	}
	if len(codeCommitDigest(strings.Repeat("x", 1024))) != 64 {
		t.Fatal("comment marker bound")
	}
	if strings.Contains(fmt.Sprintf("%+v", ccFixtureInput()), "synthetic") {
		t.Fatal("credential input formatting leaked secret")
	}
}
func TestCodeCommitEnvelopeValidation(t *testing.T) {
	a := CodeCommitAccess{settings: codeCommitSettings{Account: "123456789012", Region: "us-east-1"}}
	r := CodeCommitRepository{RepositoryID: "opaque-repo", Name: "Repo", ARN: "arn:aws:codecommit:us-east-1:123456789012:Repo"}
	for _, pr := range []bool{true, false} {
		body := ccEvent(a, r, "event-1", pr)
		v, e := parseCodeCommitEnvelope(body)
		if e != nil {
			t.Fatal(e)
		}
		if _, _, _, _, _, _, e = validateCodeCommitEnvelope(v, a, r); e != nil {
			t.Fatal(e)
		}
	}
	for _, body := range [][]byte{[]byte(`{"id":"a","id":"b"}`), []byte(`{"x":{ "a":1,"a":2}}`), append(ccEvent(a, r, "x", true), []byte(`{}`)...), []byte(strings.Repeat(" ", CodeCommitEnvelopeLimit+1))} {
		if _, e := parseCodeCommitEnvelope(body); e == nil {
			t.Fatal("ambiguous envelope accepted")
		}
	}
	v, _ := parseCodeCommitEnvelope(ccEvent(a, r, "x", true))
	v.Account = "999999999999"
	if _, _, _, _, _, _, e := validateCodeCommitEnvelope(v, a, r); e == nil {
		t.Fatal("cross-account event accepted")
	}
}
func TestCodeCommitCredentialsCASAndTenantPG(t *testing.T) {
	f := newCodeCommitDBFixture(t)
	c := f.connection()
	a := f.access(c)
	if a.credentials.AccessKeyID != ccFixtureInput().AccessKeyID {
		t.Fatal("sealed credential round trip")
	}
	var p CodeCommitCredentialPlan
	in := ccFixtureInput()
	in.Version = c.Version
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		p, e = f.service.PrepareCodeCommitCredentials(ctx, tx, f.org, f.actor, c.ID, in)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
				_, e := f.service.SaveCodeCommitCredentials(ctx, tx, f.org, f.actor, p, ccFixtureIdentity())
				return e
			})
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrVersionConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	e = f.run(refsTestID(t), func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.readCodeCommit(ctx, tx, "other", c.ID, false, false)
		return e
	})
	if !errors.Is(e, ErrNotFound) {
		t.Fatal("tenant isolation", e)
	}
}
func TestCodeCommitBridgeConcurrentReplayAndRotationPG(t *testing.T) {
	f := newCodeCommitDBFixture(t)
	a := f.access(f.connection())
	r, e := f.repository(a, "repo-id", "Repo")
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Token) != 43 || r.URL == "" {
		t.Fatal("one-time bridge token missing")
	}
	body := ccEvent(a, r.Repository, "event-1", true)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := f.receive(a, r, body); results <- e }()
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var deliveries, works int
		e := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM openfox.integration_webhook_deliveries WHERE connection_id=$1::uuid),(SELECT count(*) FROM openfox.integration_codecommit_work WHERE connection_id=$1::uuid)`, a.ID).Scan(&deliveries, &works)
		if e == nil && (deliveries != 1 || works != 1) {
			t.Fatal("duplicate writes", deliveries, works)
		}
		cfg, e := f.service.CodeCommitConfiguration(ctx, tx, f.org, f.actor, a.ID, "")
		if e != nil {
			return e
		}
		raw, _ := json.Marshal(cfg)
		if strings.Contains(string(raw), r.Token) || strings.Contains(string(raw), "synthetic-secret") {
			t.Fatal("configuration leaked secret")
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	changed := append(append([]byte{}, body...), byte(' '))
	if _, e = f.receive(a, r, changed); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("same event different body", e)
	}
	var rotated CodeCommitBridgeResult
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		rotated, e = f.service.ManageCodeCommitRepository(ctx, tx, f.org, f.actor, a.ID, r.Repository.ID, "rotate", CodeCommitVersionInput{a.Version, r.Repository.Version})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(a, r, body); !errors.Is(e, ErrCodeCommitBridgeUnauthorized) {
		t.Fatal("old token accepted", e)
	}
	if out, e := f.receive(a, rotated, body); e != nil || out.Duplicate {
		t.Fatal("new generation did not accept event", e)
	}
	abort := errors.New("caller transaction aborted")
	next := ccEvent(a, r.Repository, "event-2", false)
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := f.service.ReceiveCodeCommit(ctx, tx, CodeCommitBridgeLookup{f.org, a.ID, r.Repository.ID}, rotated.Repository.BridgeID, rotated.Token, next); e != nil {
			return e
		}
		return abort
	})
	if !errors.Is(e, abort) {
		t.Fatal(e)
	}
	if out, e := f.receive(a, rotated, next); e != nil || out.Duplicate {
		t.Fatal("rolled back event suppressed", e)
	}
}
func TestCodeCommitDisableDisconnectRetainsUnknownPG(t *testing.T) {
	f := newCodeCommitDBFixture(t)
	c := f.connection()
	a := f.access(c)
	r, e := f.repository(a, "repo-id", "Repo")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(a, r, ccEvent(a, r.Repository, "unknown", true)); e != nil {
		t.Fatal(e)
	}
	intent, _ := codecommit.NewCommentIntent("Repo", "7", strings.Repeat("a", 40), strings.Repeat("b", 40), "original-idempotency-token", "controlled task link")
	raw, _ := json.Marshal(intent)
	f.exec(`UPDATE openfox.integration_codecommit_work SET backlink_state='SENDING',backlink_intent=$2::jsonb,backlink_link_ids=ARRAY[$3::uuid],backlink_sender=$4 WHERE connection_id=$1::uuid`, c.ID, raw, refsTestID(t), ccFixtureIdentity().Identity.ARN)
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.Disconnect(ctx, tx, f.org, f.actor, c.ID, c.Version, ConfirmPhrase("AWS CodeCommit"))
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var state string
		var saved []byte
		var active, secret int
		e := tx.QueryRow(ctx, `SELECT backlink_state,backlink_intent FROM openfox.integration_codecommit_work WHERE connection_id=$1::uuid`, c.ID).Scan(&state, &saved)
		if e != nil {
			return e
		}
		restored, e := codecommit.ParseCommentIntent(saved)
		if e != nil || state != "UNKNOWN" || restored.Fingerprint() != intent.Fingerprint() {
			t.Fatal("lost unknown intent", e, state)
		}
		e = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM openfox.integration_codecommit_repositories WHERE connection_id=$1::uuid AND enabled),(SELECT count(*) FROM openfox.integration_connections WHERE id=$1::uuid AND credentials_sealed IS NOT NULL)`, c.ID).Scan(&active, &secret)
		if active != 0 || secret != 0 {
			t.Fatal("disconnect left effects enabled")
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(a, r, ccEvent(a, r.Repository, "after", true)); !errors.Is(e, ErrNotFound) {
		t.Fatal("disconnected accepted", e)
	}
}

func TestCodeCommitRepositoryConcurrentLimitAndSlotReusePG(t *testing.T) {
	f := newCodeCommitDBFixture(t)
	a := f.access(f.connection())
	var first CodeCommitBridgeResult
	for i := 0; i < 19; i++ {
		r, e := f.repository(a, fmt.Sprint("repo-", i), fmt.Sprint("Repo", i))
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 {
			first = r
		}
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 19; i < 21; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := f.repository(a, fmt.Sprint("repo-", i), fmt.Sprint("Repo", i))
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	success, rejected := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrInvalidRequest) {
			rejected++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatal("active repository cap raced", success, rejected)
	}
	e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ManageCodeCommitRepository(ctx, tx, f.org, f.actor, a.ID, first.Repository.ID, "disable", CodeCommitVersionInput{a.Version, first.Repository.Version})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.repository(a, "replacement-id", "Replacement"); e != nil {
		t.Fatal("disabled retained row consumed active slot", e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		cfg, e := f.service.CodeCommitConfiguration(ctx, tx, f.org, f.actor, a.ID, "")
		if e == nil && len(cfg.Repositories) != 21 {
			t.Fatal("retained configuration missing", len(cfg.Repositories))
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestCodeCommitCurrentManagerAndScopeFencesPG(t *testing.T) {
	f := newCodeCommitDBFixture(t)
	c := f.connection()
	a := f.access(c)
	r, e := f.repository(a, "repo", "Repo")
	if e != nil {
		t.Fatal(e)
	}
	member := refsTestID(t)
	f.exec(`INSERT INTO openfox.users(id,email) VALUES($1::uuid,$2)`, member, member+"@example.test")
	f.exec(`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES($1::uuid,$2::uuid,'MEMBER')`, f.org, member)
	forbidden := collaboration.Actor{UserID: member, Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ManageCodeCommitRepository(ctx, tx, f.org, forbidden, c.ID, r.Repository.ID, "disable", CodeCommitVersionInput{c.Version, r.Repository.Version})
		return e
	})
	if !errors.Is(e, ErrForbidden) {
		t.Fatal("member managed bridge", e)
	}
	body := ccEvent(a, r.Repository, "cross-repo", false)
	var event map[string]any
	_ = json.Unmarshal(body, &event)
	event["resources"] = []string{"arn:aws:codecommit:us-east-1:123456789012:Other"}
	body, _ = json.Marshal(event)
	if _, e = f.receive(a, r, body); !errors.Is(e, ErrInvalidRequest) {
		t.Fatal("bridge crossed repository", e)
	}
	wrong := r
	wrong.Token = strings.Repeat("a", 43)
	if _, e = f.receive(a, wrong, ccEvent(a, r.Repository, "wrong-token", true)); !errors.Is(e, ErrCodeCommitBridgeUnauthorized) {
		t.Fatal("wrong bearer accepted", e)
	}
	// Current membership must be rechecked after network preparation.
	f.exec(`UPDATE openfox.organization_members SET role='OWNER',authorization_version=authorization_version+1 WHERE organization_id=$1::uuid AND user_id=$2::uuid`, f.org, member)
	f.exec(`UPDATE openfox.organization_members SET role='MEMBER',authorization_version=authorization_version+1 WHERE organization_id=$1::uuid AND user_id=$2::uuid`, f.org, f.user)
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.SaveCodeCommitRepository(ctx, tx, f.org, f.actor, a, CodeCommitRepositoryInput{ConnectionVersion: a.Version, Name: "Other", ConfirmInitialization: true}, codecommit.Repository{ID: "other", Name: "Other", AccountID: a.settings.Account, ARN: "arn:aws:codecommit:us-east-1:123456789012:Other"})
		return e
	})
	if e == nil {
		t.Fatal("stale owner admitted repository")
	}
}

func TestCodeCommitControlPlaneScannerPG(t *testing.T) {
	f := newCodeCommitDBFixture(t)
	a := f.access(f.connection())
	r, e := f.repository(a, "repo", "Repo")
	if e != nil {
		t.Fatal(e)
	}
	c, e := pgx.Connect(context.Background(), f.dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(context.Background())
	check := func(want bool) {
		t.Helper()
		tx, e := c.Begin(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(context.Background())
		if _, e = tx.Exec(context.Background(), `SET LOCAL ROLE openfox_context`); e != nil {
			t.Fatal(e)
		}
		var found bool
		e = tx.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM openfox_private.list_integration_sync_organizations(NULL,500) WHERE organization_id=$1::uuid)`, f.org).Scan(&found)
		if e != nil || found != want {
			t.Fatal("control scanner", e, found, want)
		}
		var lookupOrg string
		e = tx.QueryRow(context.Background(), `SELECT organization_id::text FROM openfox_private.resolve_codecommit_bridge($1::uuid)`, r.Repository.BridgeID).Scan(&lookupOrg)
		if want && (e != nil || lookupOrg != f.org) {
			t.Fatal("bridge locator", e)
		}
		if !want && !errors.Is(e, pgx.ErrNoRows) {
			t.Fatal("disabled bridge locator", e)
		}
	}
	check(true)
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := f.service.ManageCodeCommitRepository(ctx, tx, f.org, f.actor, a.ID, r.Repository.ID, "disable", CodeCommitVersionInput{a.Version, r.Repository.Version})
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	check(false)
}

func TestCodeCommitScanRetryPreservesCheckpointPG(t *testing.T) {
	f := newCodeCommitDBFixture(t)
	c := f.connection()
	a := f.access(c)
	r, e := f.repository(a, "repo", "Repo")
	if e != nil {
		t.Fatal(e)
	}
	f.exec(`UPDATE openfox.integration_codecommit_repositories SET next_token='opaque-checkpoint',scan_pending='["7"]',scan_seen_tokens='["prior-page"]',failure_count=3,last_error='upstream_unavailable',next_attempt_at=NULL,lease_token=$2::uuid,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1::uuid`, r.Repository.ID, refsTestID(t))
	retry := func(version uint64) (CodeCommitBridgeResult, error) {
		var out CodeCommitBridgeResult
		e := f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			out, e = f.service.ManageCodeCommitRepository(ctx, tx, f.org, f.actor, c.ID, r.Repository.ID, "reconcile", CodeCommitVersionInput{c.Version, version})
			return e
		})
		return out, e
	}
	if _, e = retry(r.Repository.Version); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("live lease ignored", e)
	}
	f.exec(`UPDATE openfox.integration_codecommit_repositories SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, r.Repository.ID)
	next, e := retry(r.Repository.Version)
	if e != nil {
		t.Fatal(e)
	}
	if next.Token != "" || next.URL != "" || next.Repository.Generation != r.Repository.Generation || next.Repository.Version != r.Repository.Version+1 || next.Repository.LastError != "" || next.Repository.NextAttemptAt == nil {
		t.Fatal("retry reset bridge or failed to schedule")
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var token, pending, seen string
		var hash []byte
		var count int
		e := tx.QueryRow(ctx, `SELECT next_token,scan_pending::text,scan_seen_tokens::text,bridge_token_hash,failure_count FROM openfox.integration_codecommit_repositories WHERE id=$1::uuid`, r.Repository.ID).Scan(&token, &pending, &seen, &hash, &count)
		if e != nil {
			return e
		}
		if token != "opaque-checkpoint" || pending != `["7"]` || seen != `["prior-page"]` || !bytes.Equal(hash, TokenHash(r.Token)) || count != 0 {
			t.Fatal("retry lost checkpoint/token")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = retry(r.Repository.Version); !errors.Is(e, ErrVersionConflict) {
		t.Fatal("stale version accepted", e)
	}
	if _, e = retry(next.Repository.Version); !errors.Is(e, ErrInvalidRequest) {
		t.Fatal("healthy scan incorrectly retried", e)
	}
}

func TestCodeCommitCatalogScopeAndConfiguration(t *testing.T) {
	p, ok := Lookup(ProviderCodeCommit)
	if !ok || !p.Bindable || p.Name != "AWS CodeCommit" || len(p.Capabilities) != 1 || p.Capabilities[0] != capPRLink || IsGitProvider(ProviderCodeCommit) {
		t.Fatal("CodeCommit scope advertised incorrectly")
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	box, _ := secrets.New(key)
	for _, svc := range []Service{{}, {Box: box}, {PublicURL: "https://openfox.example.test"}, {Box: box, PublicURL: "https://openfox.example.test?invalid=1"}} {
		if v := svc.providerView(p, ProviderSummary{}); v.Configured || v.Bindable {
			t.Fatal("incomplete server configuration advertised")
		}
	}
	v := (Service{Box: box, PublicURL: "https://openfox.example.test"}).providerView(p, ProviderSummary{})
	if !v.Configured || !v.Bindable || v.OAuthRedirectURI != "" {
		t.Fatal("fixed-origin IAM configuration")
	}
}

func TestCodeCommitBridgeActivationSecondBoundaryPG(t *testing.T) {
	f := newCodeCommitDBFixture(t)
	c := f.connection()
	a := f.access(c)
	r, e := f.repository(a, "repo", "Repo")
	if e != nil {
		t.Fatal(e)
	}
	event := func(id string, at time.Time) []byte {
		var body map[string]any
		_ = json.Unmarshal(ccEvent(a, r.Repository, id, false), &body)
		body["time"] = at.Format(time.RFC3339)
		raw, _ := json.Marshal(body)
		return raw
	}
	if out, e := f.receive(a, r, event("before-activation", r.Repository.ActivatedAt.Truncate(time.Second).Add(-time.Second))); e != nil || !out.Accepted {
		t.Fatal("old event receipt", e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var jobs int
		var state, detail string
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_codecommit_work WHERE connection_id=$1::uuid`, c.ID).Scan(&jobs); e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT status,detail FROM openfox.integration_webhook_deliveries WHERE connection_id=$1::uuid`, c.ID).Scan(&state, &detail); e != nil {
			return e
		}
		if jobs != 0 || state != "IGNORED" || detail != "before_activation" {
			t.Fatal("preactivation event queued", jobs, state, detail)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.receive(a, r, event("activation-second", r.Repository.ActivatedAt.Truncate(time.Second))); e != nil {
		t.Fatal(e)
	}
	e = f.run(f.org, func(ctx context.Context, tx pgx.Tx) error {
		var count int
		e := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_codecommit_work WHERE connection_id=$1::uuid`, c.ID).Scan(&count)
		if count != 1 {
			t.Fatal("activation second lost", count)
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
}
