package stage1

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"openfox/internal/dispatch"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

func (e *tkEnv) materializeTransferInputs(wantCreated, wantRejected int) {
	e.t.Helper()
	pool, err := postgres.New(e.ctx, postgres.Config{URL: os.Getenv("OPENFOX_TEST_DATABASE_URL"), MaxConns: 2, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 10 * time.Second, LockTimeout: 5 * time.Second})
	if err != nil {
		e.t.Fatal(err)
	}
	defer pool.Close()
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second})
	if err != nil {
		e.t.Fatal(err)
	}
	err = pool.RunForOrganization(e.ctx, e.org, tenancy.SourceOrganizationWorker, tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: cfgOwnerID, AuthorizationVersion: 1}, nil
	}), func(ctx context.Context, tx pgx.Tx) error {
		created, rejected, err := store.MaterializeInputs(ctx, tx, e.org, 10)
		if err == nil && (created != wantCreated || rejected != wantRejected) {
			e.t.Errorf("materialized %d rejected %d, want %d/%d", created, rejected, wantCreated, wantRejected)
		}
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *tkEnv) transferAssistant(space, owner string) string {
	e.t.Helper()
	id := e.insertAgent(space, owner, "PRIVATE")
	e.exec(`UPDATE openfox.agents SET builtin_kind='ASSISTANT' WHERE id=$1`, id)
	e.exec(`INSERT INTO openfox.agent_config_versions
 (organization_id,agent_id,config_version,computer_id,grant_id,runtime,model,max_concurrency,queue_timeout_seconds,run_timeout_seconds,created_by)
 SELECT $1,$2,1,$3,g.id,'codex','default',1,3600,600,$4 FROM openfox.device_grants g WHERE g.organization_id=$1 AND g.device_id=$3`, e.org, id, e.deviceID, owner)
	return id
}

func (e *tkEnv) assistantOffer(conv string, recipient *cfgClient, target string) string {
	e.t.Helper()
	e.owner.must(204, "POST", "conversations/"+conv+"/offers", map[string]any{"kind": "TRANSFER", "recipient_user_id": target}, nil, "Idempotency-Key", cfgNewID(e.t))
	var pending []struct {
		ID             string
		ConversationID string `json:"conversation_id"`
		Assistant      bool
	}
	recipient.must(200, "GET", "conversation-offers", nil, &pending)
	for _, p := range pending {
		if p.ConversationID == conv && p.Assistant {
			return "conversation-offers/" + p.ID
		}
	}
	e.t.Fatal("missing assistant transfer request")
	return ""
}

func TestAssistantConversationTransfer(t *testing.T) {
	for _, withTask := range []bool{false, true} {
		name := "standalone"
		if withTask {
			name = "task"
		}
		t.Run(name, func(t *testing.T) {
			e := newTkEnv(t)
			source, home := e.createSpace("shared"), e.createSpace("shared")
			from := e.transferAssistant(source.ID, cfgOwnerID)
			to := e.transferAssistant(home.ID, cfgMemberID)
			body := map[string]string{"space_id": source.ID, "agent_id": from, "visibility": "PRIVATE"}
			var c struct{ ID string }
			e.owner.must(201, "POST", "conversations", body, &c)
			base := "conversations/" + c.ID
			if withTask {
				body["task_id"] = e.newTask(e.owner, source, nil).ID
				e.owner.must(200, "POST", base+"/task", map[string]string{"task_id": body["task_id"]}, nil)
			}
			e.owner.must(201, "POST", base+"/messages", map[string]string{"body": "original history", "client_message_id": cfgNewID(t)}, nil)
			e.materializeTransferInputs(1, 0)
			var originalRun, originalSnapshot string
			e.scalar(&originalRun, `SELECT id::text FROM openfox.runs WHERE conversation_id=$1`, c.ID)
			e.exec(`UPDATE openfox.runs SET state='FAILED',finished_at=clock_timestamp(),failure_code='INTERNAL' WHERE id=$1`, originalRun)
			e.scalar(&originalSnapshot, `SELECT row_to_json(r)::text FROM openfox.runs r WHERE id=$1`, originalRun)
			e.exec(`UPDATE openfox.conversations SET runtime_override='codex',model_override='source-model',reasoning_effort_override='HIGH',approval_mode_override='READ_ONLY',workspace_selection=jsonb_build_object('mode','DIRECT','computer_id',$2::text,'directory','/source-only') WHERE id=$1`, c.ID, e.deviceID)
			p := e.assistantOffer(c.ID, e.member, cfgMemberID)
			var chat struct{ ID string }
			e.member.must(200, "POST", "direct-chats", map[string]string{"user_id": cfgOwnerID}, &chat)
			checkCard := func(status string, canOpen bool) {
				t.Helper()
				var page struct {
					Messages []struct {
						Offer struct {
							ID            string
							Status        string
							Assistant     bool
							CanOpen       bool   `json:"can_open"`
							TaskDisplayID string `json:"task_display_id"`
						}
					}
				}
				e.member.must(200, "GET", "direct-chats/"+chat.ID+"/messages", nil, &page)
				if len(page.Messages) != 1 {
					t.Fatalf("transfer card count=%d", len(page.Messages))
				}
				card := page.Messages[0].Offer
				if "conversation-offers/"+card.ID != p || card.Status != status || !card.Assistant || card.CanOpen != canOpen || (card.TaskDisplayID != "") != withTask {
					t.Fatalf("transfer card=%+v", card)
				}
			}
			checkCard("PENDING", false)
			e.member.expectError(404, "NOT_FOUND", "GET", base, nil)
			e.member.must(204, "PUT", p, map[string]bool{"accept": true}, nil)
			checkCard("ACCEPTED", true)
			e.member.must(204, "PUT", p, map[string]bool{"accept": true}, nil)
			e.owner.expectError(403, "ACTION_FORBIDDEN", "PATCH", base+"/overrides", map[string]any{"model": "default", "expected_version": 1})
			e.member.expectError(404, "NOT_FOUND", "POST", base+"/runs/"+originalRun+"/retry", nil)
			var valid bool
			e.scalar(&valid, `SELECT owner_user_id=$2 AND agent_id=$3 AND space_id=$4 AND COALESCE(task_id::text,'')=$5 AND visibility='SHARED'
 AND runtime_override IS NULL AND model_override IS NULL AND approval_mode_override IS NULL AND reasoning_effort_override IS NULL
 AND workspace_selection IS NULL AND binding_id IS NULL FROM openfox.conversations WHERE id=$1`, c.ID, cfgMemberID, to, source.ID, body["task_id"])
			if !valid {
				t.Fatal("handoff did not preserve scope or reset runtime state")
			}
			var afterSnapshot string
			e.scalar(&afterSnapshot, `SELECT row_to_json(r)::text FROM openfox.runs r WHERE id=$1`, originalRun)
			if afterSnapshot != originalSnapshot {
				t.Fatal("historical run mutated during transfer")
			}
			var directory struct{ Target any }
			e.member.must(200, "GET", base+"/work-directory", nil, &directory)
			if directory.Target != nil {
				t.Fatal("old owner's workspace exposed as recipient workspace")
			}
			var summary struct {
				Agent       struct{ ID, SpaceID, BuiltinKind string }
				OwnerUserID string
			}
			e.member.must(200, "GET", base, nil, &summary)
			e.owner.must(200, "GET", base, nil, nil)
			var count int
			e.scalar(&count, `SELECT count(*) FROM openfox.messages WHERE conversation_id=$1 AND body='original history'`, c.ID)
			if count != 1 {
				t.Fatal("history changed")
			}
			for _, entry := range []struct {
				client *cfgClient
				want   int
			}{{e.owner, 0}, {e.member, 1}} {
				var page struct{ Sessions []struct{ ID string } }
				entry.client.must(200, "GET", "assistant/sessions", nil, &page)
				if len(page.Sessions) != entry.want {
					t.Fatalf("assistant list %+v want %d", page, entry.want)
				}
			}
			message := func(agent string) map[string]any {
				m := map[string]any{"body": "continue", "client_message_id": cfgNewID(t)}
				if agent != "" {
					m["mentions"] = []map[string]string{{"agent_id": agent}}
				}
				return m
			}
			e.owner.must(201, "POST", base+"/messages", message(""), nil)
			e.scalar(&count, `SELECT count(*) FROM openfox.conversation_inputs WHERE conversation_id=$1 AND status='PENDING'`, c.ID)
			if count != 0 {
				t.Fatal("source participant invoked recipient assistant")
			}
			e.owner.expectError(400, "INVALID_REQUEST", "POST", base+"/messages", message(to))
			e.member.must(201, "POST", base+"/messages", message(""), nil)
			e.member.must(201, "POST", base+"/messages", message(to), nil)
			e.scalar(&count, `SELECT count(*) FROM openfox.conversation_inputs i JOIN openfox.conversations c ON c.organization_id=i.organization_id AND c.id=i.conversation_id WHERE i.conversation_id=$1 AND i.status='PENDING' AND COALESCE(i.target_agent_id,c.agent_id)=$2`, c.ID, to)
			if count != 2 {
				t.Fatalf("recipient inputs=%d", count)
			}
			e.materializeTransferInputs(1, 0)
			e.scalar(&count, `SELECT count(*) FROM openfox.runs WHERE conversation_id=$1 AND agent_id=$2 AND actor_id=$3 AND space_id=$4 AND runtime='codex' AND model='default' AND workspace_selection IS NULL`, c.ID, to, cfgMemberID, source.ID)
			if count != 1 {
				t.Fatal("next run did not use recipient defaults in original space")
			}
		})
	}
}

func TestAssistantTransferReadinessAndBusy(t *testing.T) {
	e := newTkEnv(t)
	sp := e.createSpace("shared")
	from := e.transferAssistant(sp.ID, cfgOwnerID)
	var c struct{ ID string }
	e.owner.must(201, "POST", "conversations", map[string]string{"space_id": sp.ID, "agent_id": from}, &c)
	base := "conversations/" + c.ID
	var peers []struct {
		ID     string
		Reason string `json:"unavailable_reason"`
	}
	e.owner.must(200, "GET", base+"/offer-candidates?kind=TRANSFER", nil, &peers)
	if len(peers) != 1 || peers[0].Reason != "ASSISTANT_TRANSFER_NOT_READY" {
		t.Fatalf("unready peers=%+v", peers)
	}
	e.owner.expectError(409, "ASSISTANT_TRANSFER_NOT_READY", "POST", base+"/offers", map[string]string{"kind": "TRANSFER", "recipient_user_id": cfgMemberID}, "Idempotency-Key", cfgNewID(t))
	to := e.transferAssistant(sp.ID, cfgMemberID)
	p := e.assistantOffer(c.ID, e.member, cfgMemberID)
	e.exec(`UPDATE openfox.agents SET status='OFFLINE' WHERE id=$1`, to)
	e.member.expectError(409, "ASSISTANT_TRANSFER_NOT_READY", "PUT", p, map[string]bool{"accept": true})
	e.exec(`UPDATE openfox.agents SET status='ACTIVE' WHERE id=$1`, to)
	e.owner.must(201, "POST", base+"/messages", map[string]string{"body": "pending", "client_message_id": cfgNewID(t)}, nil)
	e.member.expectError(409, "ASSISTANT_TRANSFER_BUSY", "PUT", p, map[string]bool{"accept": true})
	e.exec(`UPDATE openfox.conversation_inputs SET status='REJECTED',assigned_at=clock_timestamp() WHERE conversation_id=$1`, c.ID)
	e.addSpaceMember(sp.ID, cfgMemberID, "MEMBER", "REVOKED")
	e.member.expectError(404, "NOT_FOUND", "PUT", p, map[string]bool{"accept": true})
	e.addSpaceMember(sp.ID, cfgMemberID, "MEMBER", "ACTIVE")
	e.member.must(204, "PUT", p, map[string]bool{"accept": true}, nil)
	e.addSpaceMember(sp.ID, cfgMemberID, "MEMBER", "REVOKED")
	var page struct{ Sessions []struct{ ID string } }
	e.member.must(200, "GET", "assistant/sessions", nil, &page)
	if len(page.Sessions) != 0 {
		t.Fatal("revoked source space still appears in assistant sessions")
	}
}

func TestAssistantTransferMessageWaitsForOwner(t *testing.T) {
	e := newTkEnv(t)
	sp := e.createSpace("shared")
	from := e.transferAssistant(sp.ID, cfgOwnerID)
	to := e.transferAssistant(sp.ID, cfgMemberID)
	var c struct{ ID string }
	e.owner.must(201, "POST", "conversations", map[string]string{"space_id": sp.ID, "agent_id": from}, &c)
	tx, err := e.superuser.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	if _, err = tx.Exec(e.ctx, `UPDATE openfox.conversations SET agent_id=$2,owner_user_id=$3,visibility='SHARED' WHERE id=$1`, c.ID, to, cfgMemberID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(e.ctx, `INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id,access_level) VALUES($1,$2,$3,'COMMENTER')`, e.org, c.ID, cfgOwnerID); err != nil {
		t.Fatal(err)
	}
	done := make(chan cfgResponse, 1)
	go func() {
		done <- e.owner.do("POST", "conversations/"+c.ID+"/messages", map[string]string{"body": "late", "client_message_id": cfgNewID(t)})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err = tx.QueryRow(e.ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pg_backend_pid()=ANY(pg_blocking_pids(pid)))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("message did not wait on conversation lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = tx.Commit(e.ctx); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.Status != 201 {
		t.Fatalf("late send: %d %s", r.Status, r.Body)
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.conversation_inputs WHERE conversation_id=$1`, c.ID)
	if count != 0 {
		t.Fatal("stale sender queued a run during handoff")
	}
}

func TestAssistantTransferGrantsAndScope(t *testing.T) {
	env := newTkEnv(t)
	source, home := env.createSpace("shared"), env.createSpace("private")
	env.addSpaceMember(home.ID, cfgMemberID, "MEMBER", "ACTIVE")
	from := env.transferAssistant(source.ID, cfgOwnerID)
	target := env.transferAssistant(home.ID, cfgMemberID)
	var conversation struct{ ID string }
	env.owner.must(201, "POST", "conversations", map[string]string{"space_id": source.ID, "agent_id": from}, &conversation)
	base := "conversations/" + conversation.ID
	request := env.assistantOffer(conversation.ID, env.member, cfgMemberID)
	env.outsider.expectError(404, "NOT_FOUND", "PUT", request, map[string]bool{"accept": true})
	env.owner.expectError(403, "ACTION_FORBIDDEN", "POST", base+"/offers", map[string]string{"kind": "TRANSFER", "recipient_user_id": cfgOutsiderID}, "Idempotency-Key", cfgNewID(t))
	env.member.expectError(404, "NOT_FOUND", "POST", base+"/offers", map[string]string{"kind": "TRANSFER", "recipient_user_id": cfgOwnerID}, "Idempotency-Key", cfgNewID(t))
	env.exec(`UPDATE openfox.device_grants SET scope='SPACE',scope_space_id=$2 WHERE organization_id=$1 AND device_id=$3`, env.org, home.ID, env.deviceID)
	env.member.expectError(409, "ASSISTANT_TRANSFER_NOT_READY", "PUT", request, map[string]bool{"accept": true})
	env.exec(`UPDATE openfox.device_grants SET scope='PERSONAL',scope_space_id=NULL,scope_user_id=$2 WHERE organization_id=$1 AND device_id=$3`, env.org, cfgOwnerID, env.deviceID)
	env.member.expectError(409, "ASSISTANT_TRANSFER_NOT_READY", "PUT", request, map[string]bool{"accept": true})
	env.exec(`UPDATE openfox.device_grants SET scope_user_id=$2 WHERE organization_id=$1 AND device_id=$3`, env.org, cfgMemberID, env.deviceID)
	env.addSpaceMember(home.ID, cfgMemberID, "MEMBER", "REVOKED")
	env.member.expectError(409, "ASSISTANT_TRANSFER_NOT_READY", "PUT", request, map[string]bool{"accept": true})
	env.addSpaceMember(home.ID, cfgMemberID, "MEMBER", "ACTIVE")
	env.addSpaceMember(source.ID, cfgMemberID, "VIEWER", "ACTIVE")
	env.member.expectError(403, "ACTION_FORBIDDEN", "PUT", request, map[string]bool{"accept": true})
	env.addSpaceMember(source.ID, cfgMemberID, "MEMBER", "ACTIVE")
	env.member.must(204, "PUT", request, map[string]bool{"accept": true}, nil)
	ordinary := env.insertAgent(home.ID, cfgMemberID, "SHARED")
	_, err := env.superuser.Exec(env.ctx, `UPDATE openfox.conversations SET agent_id=$2 WHERE id=$1`, conversation.ID, ordinary)
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != "23514" {
		t.Fatalf("ordinary cross-space agent allowed: %v", err)
	}
	_, err = env.superuser.Exec(env.ctx, `UPDATE openfox.conversations SET owner_user_id=$2 WHERE id=$1`, conversation.ID, cfgOwnerID)
	if !errors.As(err, &databaseError) || databaseError.Code != "23514" {
		t.Fatalf("cross-space assistant owner mismatch allowed: %v", err)
	}
	var currentAgent string
	env.scalar(&currentAgent, `SELECT agent_id::text FROM openfox.conversations WHERE id=$1`, conversation.ID)
	if currentAgent != target {
		t.Fatal("rejected scope update changed agent")
	}
}

func TestAssistantTransferActiveRunsAndReconciliation(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	from := env.transferAssistant(space.ID, cfgOwnerID)
	env.transferAssistant(space.ID, cfgMemberID)
	var conversation struct{ ID string }
	env.owner.must(201, "POST", "conversations", map[string]string{"space_id": space.ID, "agent_id": from}, &conversation)
	request := env.assistantOffer(conversation.ID, env.member, cfgMemberID)
	env.owner.must(201, "POST", "conversations/"+conversation.ID+"/messages", map[string]string{"body": "queued", "client_message_id": cfgNewID(t)}, nil)
	env.materializeTransferInputs(1, 0)
	var runID string
	env.scalar(&runID, `SELECT id::text FROM openfox.runs WHERE conversation_id=$1`, conversation.ID)
	for _, state := range []string{"QUEUED", "PREPARING", "RUNNING", "CANCEL_REQUESTED"} {
		env.exec(`UPDATE openfox.runs SET state=$2,cancel_requested_at=CASE WHEN $2='CANCEL_REQUESTED' THEN clock_timestamp() ELSE NULL END WHERE id=$1`, runID, state)
		env.member.expectError(409, "ASSISTANT_TRANSFER_BUSY", "PUT", request, map[string]bool{"accept": true})
	}
	env.exec(`UPDATE openfox.runs SET state='FAILED',finished_at=clock_timestamp(),cancel_requested_at=NULL,reconciliation_required=true WHERE id=$1`, runID)
	env.member.expectError(409, "ASSISTANT_TRANSFER_BUSY", "PUT", request, map[string]bool{"accept": true})
	env.exec(`UPDATE openfox.runs SET reconciliation_required=false WHERE id=$1`, runID)
	env.exec(`INSERT INTO openfox.run_reconciliation_cases(organization_id,run_id,generation,reason_code,failure_code) VALUES($1,$2,1,'RESULT_UNCONFIRMED','UNKNOWN_PROCESS')`, env.org, runID)
	env.member.expectError(409, "ASSISTANT_TRANSFER_BUSY", "PUT", request, map[string]bool{"accept": true})
	env.exec(`UPDATE openfox.run_reconciliation_cases SET status='RESOLVED',resolved_at=clock_timestamp(),resolution='PROCESS_EXITED_FAILED',resolved_by='test' WHERE run_id=$1`, runID)
	env.member.must(204, "PUT", request, map[string]bool{"accept": true}, nil)
}

func TestAssistantTransferCompetingRecipients(t *testing.T) {
	env := newTkEnv(t)
	space := env.createSpace("shared")
	from := env.transferAssistant(space.ID, cfgOwnerID)
	env.transferAssistant(space.ID, cfgMemberID)
	other := env.addMember(tkUserA, "MEMBER")
	env.transferAssistant(space.ID, tkUserA)
	var conversation struct{ ID string }
	env.owner.must(201, "POST", "conversations", map[string]string{"space_id": space.ID, "agent_id": from}, &conversation)
	first := env.assistantOffer(conversation.ID, env.member, cfgMemberID)
	second := env.assistantOffer(conversation.ID, other, tkUserA)
	results := make(chan cfgResponse, 2)
	go func() { results <- env.member.do("PUT", first, map[string]bool{"accept": true}) }()
	go func() { results <- other.do("PUT", second, map[string]bool{"accept": true}) }()
	winner, loser := <-results, <-results
	if winner.Status != 204 {
		winner, loser = loser, winner
	}
	if winner.Status != 204 || (loser.Status != 403 && loser.Status != 404) {
		t.Fatalf("competing transfers: %+v %+v", winner, loser)
	}
	var accepted int
	env.scalar(&accepted, `SELECT count(*) FROM openfox.conversation_offers WHERE conversation_id=$1 AND status='ACCEPTED'`, conversation.ID)
	if accepted != 1 {
		t.Fatal("more than one transfer accepted")
	}
}
