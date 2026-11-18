package stage3

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/agents"
)

func TestWorkingHoursHTTPConfigAndTrigger(t *testing.T) {
	env := newS3Env(t)
	id := env.insertAgent(env.space.ID)
	env.configureAgent(id)
	base := spacePath(env.space.ID) + "/agents/" + id
	var agent agents.Agent
	env.owner().must(200, "GET", base, nil, &agent)
	cfg := map[string]any{"computer_id": agent.Config.ComputerID, "grant_id": agent.Config.GrantID, "runtime": "codex", "model": "default", "expected_agent_version": agent.Version}
	closed := map[string]any{"enabled": true, "timezone": "UTC", "days": [][]any{{}, {}, {}, {}, {}, {}, {}}, "triggers": map[string]bool{"assignment": true, "comment_mention": true, "direct_chat": false}}
	cfg["working_hours"] = closed
	env.member().expectError(403, "", "POST", base+"/config-versions", cfg)
	env.outsider().expectError(404, "", "POST", base+"/config-versions", cfg)
	env.owner().must(201, "POST", base+"/config-versions", cfg, &agent)
	env.owner().expectError(409, "", "POST", base+"/config-versions", cfg)
	cfg["expected_agent_version"] = agent.Version
	delete(cfg, "working_hours")
	env.owner().must(201, "POST", base+"/config-versions", cfg, &agent)
	if !strings.Contains(string(agent.Config.WorkingHours), `"assignment": true`) && !strings.Contains(string(agent.Config.WorkingHours), `"assignment":true`) {
		t.Fatalf("omitted field lost %s", agent.Config.WorkingHours)
	}
	for _, bad := range []any{map[string]any{"enabled": true}, map[string]any{"enabled": true, "timezone": "Local", "days": [][]any{{}, {}, {}, {}, {}, {}, {}}, "triggers": closed["triggers"]}, map[string]any{"enabled": true, "timezone": "UTC", "days": [][]any{{}, {}, {}, {}, {}, {}, {}}, "triggers": map[string]any{"assignment": nil, "comment_mention": true, "direct_chat": false}}} {
		cfg["expected_agent_version"] = agent.Version
		cfg["working_hours"] = bad
		env.owner().expectError(400, "", "POST", base+"/config-versions", cfg)
	}
	var copied agents.Agent
	env.owner().must(201, "POST", base+"/copy", map[string]any{"name": "A04 independent copy"}, &copied, "Idempotency-Key", "a04-copy-schedule")
	if copied.ID == id || string(copied.Config.WorkingHours) != string(agent.Config.WorkingHours) {
		t.Fatalf("copy lost schedule: %s", copied.Config.WorkingHours)
	}
	// The public create task path carries a once-only future assignment timestamp.
	future := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	task := env.createTask(env.owner(), env.space.ID, map[string]any{"title": "HTTP deferred task", "status_id": env.statusID(env.org, "TODO"), "assignees": []map[string]string{{"kind": "AGENT", "id": id}}, "assignment_not_before": future.Format(time.RFC3339)})
	env.materialize()
	var run string
	env.scalar(&run, `SELECT r.id::text FROM openfox.runs r JOIN openfox.conversations c ON c.id=r.conversation_id WHERE c.task_id=$1`, task.ID)
	path := "runs/" + run
	var detail struct {
		Pending   bool       `json:"schedule_pending"`
		Can       bool       `json:"can_trigger_now"`
		Requested *time.Time `json:"requested_start_at"`
	}
	env.owner().must(200, "GET", path, nil, &detail)
	var conversationID string
	env.scalar(&conversationID, `SELECT conversation_id::text FROM openfox.runs WHERE id=$1`, run)
	var page struct {
		Runs []struct {
			Pending   bool       `json:"schedule_pending"`
			Requested *time.Time `json:"requested_start_at"`
		} `json:"runs"`
	}
	env.owner().must(200, "GET", "conversations/"+conversationID+"/runs", nil, &page)
	if len(page.Runs) != 1 || !page.Runs[0].Pending || page.Runs[0].Requested == nil {
		t.Fatalf("list lost schedule %+v", page)
	}

	if !detail.Pending || !detail.Can || detail.Requested == nil || !detail.Requested.Equal(future) {
		t.Fatalf("detail %+v", detail)
	}
	env.member().expectError(404, "", "POST", path+"/trigger-now", nil, "Idempotency-Key", "a04-http-forbidden")
	env.outsider().expectError(404, "", "POST", path+"/trigger-now", nil, "Idempotency-Key", "a04-http-hidden")
	env.owner().expectError(400, "IDEMPOTENCY_KEY_REQUIRED", "POST", path+"/trigger-now", nil)
	var wg sync.WaitGroup
	responses := make(chan s3Response, 8)
	client := env.owner()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- client.do(http.MethodPost, path+"/trigger-now", nil, "Idempotency-Key", "a04-http-concurrent")
		}()
	}
	wg.Wait()
	close(responses)
	replayed := 0
	for r := range responses {
		if r.Status != 200 {
			t.Fatalf("trigger %d %s", r.Status, r.Body)
		}
		var value struct {
			Replayed bool `json:"replayed"`
		}
		if err := json.Unmarshal(r.Body, &value); err != nil {
			t.Fatal(err)
		}
		if value.Replayed {
			replayed++
		}
	}
	if replayed != 7 {
		t.Fatalf("replays=%d", replayed)
	}
	env.owner().must(200, "GET", path, nil, &detail)
	if detail.Pending || detail.Can {
		t.Fatalf("after %+v", detail)
	}
	if got := env.count(`SELECT count(*) FROM openfox.runs r JOIN openfox.conversations c ON c.id=r.conversation_id WHERE c.task_id=$1`, task.ID); got != 1 {
		t.Fatalf("runs=%d", got)
	}
	// An authorized manager is a different idempotency principal.
	env.addSpaceMember(env.space.ID, s3AdminID, "MEMBER")
	var conversation string
	env.scalar(&conversation, `SELECT conversation_id::text FROM openfox.runs WHERE id=$1`, run)
	env.exec(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id,access_level) VALUES($1,$2,$3,'COMMENTER')`, env.org, conversation, s3AdminID)
	env.admin().expectError(409, "IDEMPOTENCY_CONFLICT", "POST", path+"/trigger-now", nil, "Idempotency-Key", "a04-http-concurrent")
	// Wait on the existing Run lock, revoke its device grant, then release the
	// lock. The permission decision made before waiting must not authorize admission.
	next := env.createTask(env.owner(), env.space.ID, map[string]any{"title": "revoke while waiting", "status_id": env.statusID(env.org, "TODO"), "assignees": []map[string]string{{"kind": "AGENT", "id": id}}})
	env.materialize()
	var blocked string
	env.scalar(&blocked, `SELECT r.id::text FROM openfox.runs r JOIN openfox.conversations c ON c.id=r.conversation_id WHERE c.task_id=$1`, next.ID)
	lock, err := env.superuser.Begin(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(env.ctx)
	if _, err = lock.Exec(env.ctx, `SELECT id FROM openfox.runs WHERE id=$1 FOR UPDATE`, blocked); err != nil {
		t.Fatal(err)
	}
	response := make(chan s3Response, 1)
	go func() {
		response <- client.do("POST", "runs/"+blocked+"/trigger-now", nil, "Idempotency-Key", "a04-http-lock-revoke")
	}()
	waiting := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		env.exec(`SELECT pg_stat_clear_snapshot()`)
		env.scalar(&waiting, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock')`)
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("request did not reach Run lock")
	}
	if _, err = lock.Exec(env.ctx, `UPDATE openfox.device_grants SET status='REVOKED',permission_version=permission_version+1 WHERE organization_id=$1 AND id=$2`, env.org, agent.Config.GrantID); err != nil {
		t.Fatal(err)
	}
	if err = lock.Commit(env.ctx); err != nil {
		t.Fatal(err)
	}
	denied := <-response
	if denied.Status != 403 {
		t.Fatalf("revoked while waiting=%d %s", denied.Status, denied.Body)
	}
	var pending bool
	env.scalar(&pending, `SELECT schedule_pending FROM openfox.runs WHERE id=$1`, blocked)
	if !pending {
		t.Fatal("revoked request admitted run")
	}
	if n := env.count(`SELECT count(*) FROM openfox.run_idempotency WHERE organization_id=$1 AND idempotency_key='a04-http-lock-revoke'`, env.org); n != 0 {
		t.Fatal("failed admission committed idempotency claim")
	}
}

func TestWorkingHoursCommentPermissionAndBounds(t *testing.T) {
	env := newS3Env(t)
	id := env.insertAgent(env.space.ID)
	env.configureAgent(id)
	env.exec(`INSERT INTO openfox.agent_config_versions(organization_id,agent_id,config_version,computer_id,grant_id,runtime,model,created_by,working_hours)
 SELECT organization_id,agent_id,2,computer_id,grant_id,runtime,model,created_by,'{"enabled":true,"timezone":"UTC","days":[[],[],[],[],[],[],[]],"triggers":{"assignment":true,"comment_mention":true,"direct_chat":false}}'::jsonb FROM openfox.agent_config_versions WHERE agent_id=$1`, id)
	env.exec(`UPDATE openfox.agents SET current_config_version=2 WHERE id=$1`, id)
	task := env.createTask(env.owner(), env.space.ID, map[string]any{"title": "original author", "status_id": env.statusID(env.org, "TODO"), "assignees": []map[string]string{{"kind": "AGENT", "id": id}}})
	env.materialize()
	post := func(taskID, agentID, key string, client *s3Client) string {
		var out struct {
			ID string `json:"id"`
		}
		client.must(201, "POST", spacePath(env.space.ID)+"/tasks/"+taskID+"/comments", map[string]any{"body": "please inspect", "client_comment_id": key, "mentions": []map[string]string{{"agent_id": agentID}}}, &out)
		return out.ID
	}
	comment := post(task.ID, id, "a04-original-author", env.member())
	env.materialize()
	var actor, origin, owner string
	env.scalar(&actor, `SELECT r.actor_id::text FROM openfox.runs r JOIN openfox.conversation_inputs i ON i.run_id=r.id JOIN openfox.messages m ON m.id=i.message_id WHERE m.client_message_id=$1`, "comment-mention:"+comment+":"+id)
	if actor != s3MemberID {
		t.Fatalf("comment impersonated assignment owner: %s", actor)
	}
	env.scalar(&origin, `SELECT c.origin FROM openfox.conversations c JOIN openfox.messages m ON m.conversation_id=c.id WHERE m.client_message_id=$1`, "comment-mention:"+comment+":"+id)
	env.scalar(&owner, `SELECT c.owner_user_id::text FROM openfox.conversations c JOIN openfox.messages m ON m.conversation_id=c.id WHERE m.client_message_id=$1`, "comment-mention:"+comment+":"+id)
	if origin != "USER" || owner != s3MemberID {
		t.Fatalf("conversation %s %s", origin, owner)
	}
	// A private Agent owned by someone else may be mentioned but cannot be run.
	env.exec(`UPDATE openfox.agents SET visibility='PRIVATE' WHERE id=$1`, id)
	private := post(task.ID, id, "a04-private", env.member())
	if n := env.count(`SELECT count(*) FROM openfox.task_agent_events WHERE comment_id=$1`, private); n != 0 {
		t.Fatal("private mention enqueued")
	}
	env.exec(`UPDATE openfox.agents SET visibility='SHARED' WHERE id=$1`, id)
	revoked := post(task.ID, id, "a04-author-revoked", env.member())
	env.exec(`DELETE FROM openfox.space_members WHERE organization_id=$1 AND space_id=$2 AND user_id=$3`, env.org, env.space.ID, s3MemberID)
	env.materialize()
	if n := env.count(`SELECT count(*) FROM openfox.messages WHERE client_message_id=$1`, "comment-mention:"+revoked+":"+id); n != 0 {
		t.Fatal("revoked author executed")
	}
	var outcome string
	env.scalar(&outcome, `SELECT outcome FROM openfox.task_agent_events WHERE comment_id=$1`, revoked)
	if outcome == "started" || outcome == "" {
		t.Fatalf("revocation outcome %q", outcome)
	}
	env.addSpaceMember(env.space.ID, s3MemberID, "MEMBER")
	deleted := post(task.ID, id, "a04-deleted", env.member())
	env.member().must(204, "DELETE", spacePath(env.space.ID)+"/tasks/"+task.ID+"/comments/"+deleted, nil, nil, "Idempotency-Key", "a04-delete-comment")
	env.materialize()
	env.scalar(&outcome, `SELECT outcome FROM openfox.task_agent_events WHERE comment_id=$1`, deleted)
	if outcome != "comment_missing" {
		t.Fatalf("deleted outcome %q", outcome)
	}
	env.exec(`UPDATE openfox.agents SET trigger_mention=false WHERE id=$1`, id)
	disabled := post(task.ID, id, "a04-disabled", env.member())
	if n := env.count(`SELECT count(*) FROM openfox.task_agent_events WHERE comment_id=$1`, disabled); n != 0 {
		t.Fatal("disabled mention enqueued")
	}
	// At most five distinct Agent triggers, while preserving all mention records.
	var mentions []map[string]string
	for i := 0; i < 6; i++ {
		mentions = append(mentions, map[string]string{"agent_id": env.insertAgent(env.space.ID)})
	}
	var limit struct {
		ID string `json:"id"`
	}
	env.member().must(201, "POST", spacePath(env.space.ID)+"/tasks/"+task.ID+"/comments", map[string]any{"body": "bounded fanout", "client_comment_id": "a04-bounded", "mentions": mentions}, &limit)
	if n := env.count(`SELECT count(*) FROM openfox.task_agent_events WHERE comment_id=$1`, limit.ID); n != 5 {
		t.Fatalf("fanout=%d", n)
	}
	// Cancelled tasks retain their existing execution gate.
	env.exec(`UPDATE openfox.agents SET trigger_mention=true WHERE id=$1`, id)
	canceled := env.createTask(env.owner(), env.space.ID, map[string]any{"title": "canceled gate", "status_id": env.statusID(env.org, "CANCELLED")})
	gate := post(canceled.ID, id, "a04-task-gate", env.member())
	env.materialize()
	env.scalar(&outcome, `SELECT outcome FROM openfox.task_agent_events WHERE comment_id=$1`, gate)
	if outcome != "status_cancelled" {
		t.Fatalf("gate=%s", outcome)
	}
}

func TestWorkingHoursDirectChatSelection(t *testing.T) {
	env := newS3Env(t)
	id := env.insertAgent(env.space.ID)
	env.configureAgent(id)
	var agent agents.Agent
	base := spacePath(env.space.ID) + "/agents/" + id
	env.owner().must(200, "GET", base, nil, &agent)
	save := func(enabled bool) {
		env.owner().must(201, "POST", base+"/config-versions", map[string]any{"computer_id": agent.Config.ComputerID, "grant_id": agent.Config.GrantID, "runtime": "codex", "model": "default", "expected_agent_version": agent.Version, "working_hours": map[string]any{"enabled": true, "timezone": "UTC", "days": [][]any{{}, {}, {}, {}, {}, {}, {}}, "triggers": map[string]bool{"assignment": true, "comment_mention": true, "direct_chat": enabled}}}, &agent)
	}
	save(false)
	var conv struct {
		ID string `json:"id"`
	}
	env.owner().must(201, "POST", "conversations", map[string]any{"space_id": env.space.ID, "agent_id": id, "visibility": "PRIVATE"}, &conv)
	for n, selected := range []bool{false, true} {
		if selected {
			save(true)
		}
		env.owner().must(201, "POST", "conversations/"+conv.ID+"/messages", map[string]any{"body": "direct invocation", "client_message_id": s3NewID(t), "mentions": []map[string]string{{"agent_id": id}}}, nil)
		env.materialize()
		var run, kind string
		var pending bool
		env.scalar(&run, `SELECT id::text FROM openfox.runs WHERE conversation_id=$1 ORDER BY created_at DESC LIMIT 1`, conv.ID)
		env.scalar(&kind, `SELECT trigger_kind FROM openfox.runs WHERE id=$1`, run)
		env.scalar(&pending, `SELECT schedule_pending FROM openfox.runs WHERE id=$1`, run)
		if kind != "DIRECT_CHAT" || pending != selected {
			t.Fatalf("selection %d kind=%s pending=%v", n, kind, pending)
		}
		env.owner().must(200, "POST", "runs/"+run+"/cancel", nil, nil, "Idempotency-Key", "a04-direct-cancel-"+run)
	}
}
