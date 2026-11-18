package stage10d

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/artifactreview"
	"openfox/internal/docs"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

func TestNativeFileCommentsApplyDirectly(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	seed(t, ctx, url)
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, e := conn.Exec(ctx, query, args...); e != nil {
			t.Fatalf("%s: %v", query, e)
		}
	}
	const conv = "00000000-0000-0000-0000-00000000c162"
	const sourceRun = "00000000-0000-0000-0000-00000000d162"
	const reviseRun = "00000000-0000-0000-0000-00000000d163"
	const source = "00000000-0000-0000-0000-00000000a162"
	const candidate = "00000000-0000-0000-0000-00000000a163"
	const thread = "00000000-0000-0000-0000-00000000e262"
	const request = "00000000-0000-0000-0000-00000000e263"
	text := "前🙂原文\r\n后文"
	changed := "前🙂新文\r\n后文"
	exec(`INSERT INTO openfox.agent_config_versions(organization_id,agent_id,config_version,computer_id,grant_id,runtime,model,created_by) SELECT $1::uuid,$2::uuid,1,$3::uuid,id,'codex','default',$4::uuid FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$3::uuid ON CONFLICT DO NOTHING`, orgA, agentA, deviceA, ownerA)
	exec(`INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id,visibility) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,'PRIVATE')`, orgA, conv, spaceA, agentA, ownerA)
	for _, id := range []string{sourceRun, reviseRun} {
		state := "SUCCEEDED"
		if id == reviseRun {
			state = "RUNNING"
		}
		exec(`INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state,generation,finished_at,runtime,model,approval_mode,reasoning_effort) SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,id,1,1,$7::uuid,$8,1,CASE WHEN $8='SUCCEEDED' THEN clock_timestamp() ELSE NULL END,'codex','default','READ_ONLY','DEFAULT' FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$6::uuid`, orgA, id, spaceA, conv, agentA, deviceA, ownerA, state)
	}
	for _, v := range []struct{ id, run, content string }{{source, sourceRun, text}, {candidate, reviseRun, changed}} {
		exec(`INSERT INTO openfox.artifacts(organization_id,id,run_id,name,storage_key,size_bytes,sha256,mime_type,retention_until) VALUES($1::uuid,$2::uuid,$3::uuid,'review.md',$2,$4,$5,'text/markdown',clock_timestamp()+interval '1 day')`, orgA, v.id, v.run, len(v.content), artifactreview.Digest(v.content))
	}
	p, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 5 * time.Second, LockTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	transaction := func(org, user string, fn func(context.Context, pgx.Tx) error) error {
		return p.RunForOrganization(ctx, org, tenancy.SourceUserRequest, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return tenancy.OrganizationSelection{OrganizationID: org, ActorID: user, AuthorizationVersion: 1}, nil
		}), fn)
	}
	user := func(fn func(context.Context, pgx.Tx) error) error { return transaction(orgA, ownerA, fn) }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	const node = "00000000-0000-0000-0000-00000000f162"
	exec(`INSERT INTO openfox.doc_nodes(organization_id,id,space_id,owner_user_id,kind,title,body,created_by,updated_by) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'doc','Private draft',$5,$4::uuid,$4::uuid)`, orgA, node, spaceA, ownerA, text)
	svc := artifactreview.Service{}
	const message = "00000000-0000-0000-0000-00000000f162"
	exec(`INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_kind,author_user_id,body) VALUES($1::uuid,$2::uuid,$3::uuid,1,'USER',$4::uuid,'update this document')`, orgA, message, conv, ownerA)
	exec(`INSERT INTO openfox.conversation_inputs(organization_id,id,conversation_id,message_id,body,status,run_id,assigned_at) VALUES($1::uuid,gen_random_uuid(),$2::uuid,$3::uuid,'update this document','ASSIGNED',$4::uuid,clock_timestamp())`, orgA, conv, message, reviseRun)
	input := artifactreview.Input{ID: request, NodeID: node, SpaceID: spaceA, NodeVersion: 1, SHA256: artifactreview.Digest(text), Comments: []artifactreview.Snapshot{{ThreadID: thread, Anchor: artifactreview.Anchor{Kind: "text", Start: 7, End: 13, Quote: "原文"}, Body: "改成新文"}}}
	input, err = artifactreview.ValidateFileInput(input, artifactreview.File{SHA256: artifactreview.Digest(text), MIMEType: "text/markdown", Version: 1}, text)
	must(err)
	must(user(func(ctx context.Context, tx pgx.Tx) error {
		return svc.Bind(ctx, tx, orgA, conv, message, ownerA, []artifactreview.Input{input})
	}))
	const pendingMessage = "00000000-0000-0000-0000-00000000f165"
	exec(`INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_kind,author_user_id,body) VALUES($1::uuid,$2::uuid,$3::uuid,3,'USER',$4::uuid,'第二次批注')`, orgA, pendingMessage, conv, ownerA)
	exec(`INSERT INTO openfox.conversation_inputs(organization_id,id,conversation_id,message_id,body,status,run_id,assigned_at) VALUES($1::uuid,gen_random_uuid(),$2::uuid,$3::uuid,'第二次批注','ASSIGNED',$4::uuid,clock_timestamp())`, orgA, conv, pendingMessage, reviseRun)
	pending := input
	pending.ID = "00000000-0000-0000-0000-00000000e265"
	pending, err = artifactreview.ValidateFileInput(pending, artifactreview.File{SHA256: artifactreview.Digest(text), MIMEType: "text/markdown", Version: 1}, text)
	must(err)
	must(user(func(ctx context.Context, tx pgx.Tx) error {
		return svc.Bind(ctx, tx, orgA, conv, pendingMessage, ownerA, []artifactreview.Input{pending})
	}))
	must(user(func(ctx context.Context, tx pgx.Tx) error {
		q, e := svc.ForRun(ctx, tx, orgA, reviseRun, request, true)
		if e != nil {
			return e
		}
		if e = svc.ApplyNative(ctx, tx, orgA, q, changed); e != nil {
			return e
		}
		return svc.Applied(ctx, tx, q, artifactreview.Digest("submission"), "", artifactreview.Digest(changed))
	}))
	must(user(func(ctx context.Context, tx pgx.Tx) error {
		detail, e := docs.GetNodeDetail(ctx, tx, spaceA, node)
		if e != nil {
			return e
		}
		if detail.Body == nil || *detail.Body != changed || detail.Version != 2 {
			t.Fatal("document not directly updated")
		}
		return nil
	}))
	if err := user(func(ctx context.Context, tx pgx.Tx) error {
		contexts, err := svc.ForRunAll(ctx, tx, orgA, reviseRun)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(contexts)
		if len(contexts) != 2 || !strings.Contains(string(raw), "无法应用修改") {
			t.Errorf("未单独标记失效批注：%s", raw)
		}
		return nil
	}); err != nil {
		t.Errorf("单条失效批注阻断运行上下文：%v", err)
	}
	// A stale selection cannot overwrite a subsequent edit.
	stale := input
	stale.ID = "00000000-0000-0000-0000-00000000e264"
	const second = "00000000-0000-0000-0000-00000000f163"
	exec(`INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_kind,author_user_id,body) VALUES($1::uuid,$2::uuid,$3::uuid,2,'USER',$4::uuid,'stale')`, orgA, second, conv, ownerA)
	if e := user(func(ctx context.Context, tx pgx.Tx) error {
		return svc.Bind(ctx, tx, orgA, conv, second, ownerA, []artifactreview.Input{stale})
	}); !errors.Is(e, artifactreview.ErrConflict) {
		t.Fatalf("stale overwrite %v", e)
	}
	if e := transaction(orgA, memberA, func(ctx context.Context, tx pgx.Tx) error {
		return svc.Bind(ctx, tx, orgA, conv, second, memberA, []artifactreview.Input{stale})
	}); e == nil {
		t.Fatal("private document disclosed")
	}
}
