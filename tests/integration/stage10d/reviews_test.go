package stage10d

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/artifactreview"
	"openfox/internal/conversations"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

func TestFileCommentsBindToMessages(t *testing.T) {
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
	const conv = "00000000-0000-0000-0000-00000000c160"
	const sourceRun = "00000000-0000-0000-0000-00000000d160"
	const reviseRun = "00000000-0000-0000-0000-00000000d161"
	const source = "00000000-0000-0000-0000-00000000a160"
	const candidate = "00000000-0000-0000-0000-00000000a161"
	const thread = "00000000-0000-0000-0000-00000000e160"
	const request = "00000000-0000-0000-0000-00000000e161"
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
	svc := artifactreview.Service{}
	const message = "00000000-0000-0000-0000-00000000f160"
	exec(`INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_kind,author_user_id,body) VALUES($1::uuid,$2::uuid,$3::uuid,1,'USER',$4::uuid,'please address the comments')`, orgA, message, conv, ownerA)
	exec(`INSERT INTO openfox.conversation_inputs(organization_id,id,conversation_id,message_id,body,status,run_id,assigned_at) VALUES($1::uuid,gen_random_uuid(),$2::uuid,$3::uuid,'please address the comments','ASSIGNED',$4::uuid,clock_timestamp())`, orgA, conv, message, reviseRun)
	input := artifactreview.Input{ID: request, ArtifactID: source, SHA256: artifactreview.Digest(text), Comments: []artifactreview.Snapshot{{ThreadID: thread, Anchor: artifactreview.Anchor{Kind: "text", Start: 7, End: 13, Quote: "原文"}, Body: "改成新文"}}}
	input, err = artifactreview.ValidateFileInput(input, artifactreview.File{SHA256: artifactreview.Digest(text), MIMEType: "text/markdown", Version: 1}, text)
	must(err)
	bind := func(in []artifactreview.Input) error {
		return user(func(ctx context.Context, tx pgx.Tx) error { return svc.Bind(ctx, tx, orgA, conv, message, ownerA, in) })
	}
	bad := input
	bad.Comments = append([]artifactreview.Snapshot(nil), input.Comments...)
	bad.Comments[0].Anchor.Quote = "错误"
	accepted := errors.New("无效锚点被接受")
	err = user(func(ctx context.Context, tx pgx.Tx) error {
		if err := svc.Bind(ctx, tx, orgA, conv, message, ownerA, []artifactreview.Input{bad}); err != nil {
			return err
		}
		return accepted
	})
	if !errors.Is(err, artifactreview.ErrInvalid) {
		t.Errorf("入队未校验锚点：%v", err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- bind([]artifactreview.Input{input}) }()
	}
	must(<-results)
	must(<-results)
	changedInput := input
	changedInput.SHA256 = artifactreview.Digest("different")
	if e := bind([]artifactreview.Input{changedInput}); !errors.Is(e, conversations.ErrDuplicateMessage) {
		t.Fatalf("changed retry %v", e)
	}
	if e := bind(nil); !errors.Is(e, conversations.ErrDuplicateMessage) {
		t.Fatalf("empty retry %v", e)
	}
	const collisionMessage = "00000000-0000-0000-0000-00000000f164"
	exec(`INSERT INTO openfox.messages(organization_id,id,conversation_id,seq,author_kind,author_user_id,body) VALUES($1::uuid,$2::uuid,$3::uuid,2,'USER',$4::uuid,'重复批注 ID')`, orgA, collisionMessage, conv, ownerA)
	if err := user(func(ctx context.Context, tx pgx.Tx) error {
		return svc.Bind(ctx, tx, orgA, conv, collisionMessage, ownerA, []artifactreview.Input{input})
	}); !errors.Is(err, artifactreview.ErrConflict) {
		t.Errorf("批注 ID 冲突没有映射：%v", err)
	}
	must(user(func(ctx context.Context, tx pgx.Tx) error {
		contexts, e := svc.ForRunAll(ctx, tx, orgA, reviseRun)
		if e != nil {
			return e
		}
		if len(contexts) != 1 || contexts[0].File.ID != source || contexts[0].Comments[0].Body != "改成新文" {
			t.Fatal("lost feedback")
		}
		return nil
	}))
	if e := transaction(orgB, ownerB, func(ctx context.Context, tx pgx.Tx) error {
		_, e := svc.ForRun(ctx, tx, orgB, reviseRun, request, false)
		return e
	}); !errors.Is(e, conversations.ErrNotFound) {
		t.Fatalf("tenant isolation %v", e)
	}
	if e := user(func(ctx context.Context, tx pgx.Tx) error {
		_, e := svc.ForRun(ctx, tx, orgA, sourceRun, request, false)
		return e
	}); !errors.Is(e, conversations.ErrNotFound) {
		t.Fatalf("unrelated run %v", e)
	}
	must(user(func(ctx context.Context, tx pgx.Tx) error {
		q, e := svc.ForRun(ctx, tx, orgA, reviseRun, request, true)
		if e != nil {
			return e
		}
		return svc.Applied(ctx, tx, q, artifactreview.Digest("submission"), candidate, artifactreview.Digest(changed))
	}))
	must(user(func(ctx context.Context, tx pgx.Tx) error {
		q, e := svc.ForRun(ctx, tx, orgA, reviseRun, request, false)
		if e != nil {
			return e
		}
		if q.ResultArtifactID != candidate {
			t.Fatal("missing direct result")
		}
		return nil
	}))
}
