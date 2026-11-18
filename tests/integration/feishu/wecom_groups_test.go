package feishuint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"openfox/internal/tenancy"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/integrations"
	"openfox/internal/integrations/wecom"
	"openfox/internal/spacetools"
)

// A real authenticated local WebSocket, tenant HTTP/PG, inbound worker, dispatch
// and task tool. The remote platform and Runtime are controlled fixtures.
func TestWeComGroupAuthorizationPipeline(t *testing.T) {
	b := &botWorld{world: setup(t)}
	b.exec(`INSERT INTO openfox.organization_capacity(organization_id,max_slots) VALUES($1::uuid,10) ON CONFLICT(organization_id) DO UPDATE SET max_slots=10`, b.org)
	b.deviceID = b.newUUID()
	b.exec(`INSERT INTO openfox.physical_devices(id,device_key,max_slots,owner_user_id,name,last_seen_at) VALUES($1::uuid,$2,4,$3::uuid,'group-device',clock_timestamp())`, b.deviceID, "i19-"+b.org, b.ownerID)
	b.exec(`INSERT INTO openfox.device_grants(organization_id,device_id) VALUES($1::uuid,$2::uuid)`, b.org, b.deviceID)
	b.scan(&b.grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1::uuid AND device_id=$2::uuid`, b.org, b.deviceID)
	agentID := b.createAgent(b.owner, b.foxSpace, "WeCom assistant", "SHARED")
	b.exec(`UPDATE openfox.agents SET builtin_kind='TASK_ASSISTANT',allow_assign=false WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, agentID)
	connectionID, botID, external := b.newUUID(), b.newUUID(), "wecom-"+b.org
	sealed, err := b.box.Seal([]byte(`{"app_secret":"controlled-group-secret"}`), "integration:"+b.org+":"+connectionID)
	if err != nil {
		t.Fatal(err)
	}
	blob := append([]byte(sealed.KeyID), sealed.Nonce...)
	blob = append(blob, sealed.Ciphertext...)
	b.exec(`INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,created_by,credentials_sealed,credentials_key_id) VALUES($1::uuid,$2::uuid,'wecom','ORGANIZATION',$3,'Group fixture','task_assistant',$4::uuid,$5,$6)`, b.org, connectionID, external, b.ownerID, blob, sealed.KeyID)
	b.exec(`INSERT INTO openfox.im_bots(organization_id,id,connection_id,kind,agent_id,space_id,created_by) VALUES($1::uuid,$2::uuid,$3::uuid,'TASK_ASSISTANT',$4::uuid,$6::uuid,$5::uuid)`, b.org, botID, connectionID, agentID, b.ownerID, b.foxSpace)
	t.Cleanup(func() {
		b.exec(`UPDATE openfox.integration_connections SET status='DISCONNECTED',disconnected_at=clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, connectionID)
	})
	outbound := make(chan []byte, 32)
	var mu sync.Mutex
	var sent []map[string]any
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case raw := <-outbound:
					if conn.Write(ctx, websocket.MessageText, raw) != nil {
						return
					}
				}
			}
		}()
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var frame struct {
				Command string            `json:"cmd"`
				Headers map[string]string `json:"headers"`
				Body    map[string]any    `json:"body"`
			}
			if json.Unmarshal(raw, &frame) != nil {
				return
			}
			if frame.Command == "aibot_subscribe" && frame.Body["secret"] != "controlled-group-secret" {
				t.Error("wrong fixture credentials")
				return
			}
			if frame.Command == "aibot_send_msg" {
				mu.Lock()
				sent = append(sent, frame.Body)
				mu.Unlock()
			}
			if frame.Command != "" {
				ack, _ := json.Marshal(map[string]any{"headers": frame.Headers})
				select {
				case outbound <- ack:
				case <-ctx.Done():
					return
				}
			}
		}
	}))
	defer ws.Close()
	service := integrations.Service{Box: b.box, WeCom: &wecom.Client{URL: "ws" + strings.TrimPrefix(ws.URL, "http")}, PublicURL: b.publicURL}
	manager := &integrations.LongConnManager{Pool: b.pool, Service: service, Instance: "i19-" + b.org, ScanInterval: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(b.ctx)
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("manager stop timeout")
		}
	}()
	waitFor(t, "WeCom connected", func() bool {
		return b.count(`SELECT count(*) FROM openfox.im_connection_states WHERE organization_id=$1::uuid AND connection_id=$2::uuid AND state='CONNECTED'`, b.org, connectionID) == 1
	})
	worker := &integrations.IMWorker{Pool: b.pool, Service: service, Connections: manager, Instance: "i19-worker-" + b.org}
	pump := func() {
		t.Helper()
		if err := worker.RunOrganization(b.ctx, b.org); err != nil {
			t.Fatal(err)
		}
	}
	send := func(id, chatType, chatID, user, body, quote string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"cmd": "aibot_msg_callback", "headers": map[string]string{"req_id": b.newUUID()}, "body": map[string]any{"msgid": id, "aibotid": external, "chattype": chatType, "chatid": chatID, "from": map[string]string{"userid": user}, "msgtype": "text", "text": map[string]string{"content": body}, "quote": map[string]any{"msgtype": "text", "text": map[string]string{"content": quote}}}})
		outbound <- raw
		waitFor(t, "inbound persisted", func() bool {
			return b.count(`SELECT count(*) FROM openfox.im_inbound_messages WHERE organization_id=$1::uuid AND bot_id=$2::uuid AND remote_message_id=$3`, b.org, botID, id) == 1
		})
	}
	group := "group-" + b.org
	path := b.path("/integrations/wecom/bots/" + botID + "/groups")
	send("discovery", "group", group, "secret-person", "DO NOT RETAIN", "DO NOT RETAIN QUOTE")
	var text, sender, status string
	b.scan(&text, `SELECT text FROM openfox.im_inbound_messages WHERE organization_id=$1::uuid AND bot_id=$2::uuid AND remote_message_id='discovery'`, b.org, botID)
	b.scan(&sender, `SELECT sender_open_id FROM openfox.im_inbound_messages WHERE organization_id=$1::uuid AND bot_id=$2::uuid AND remote_message_id='discovery'`, b.org, botID)
	if text != "" || sender != "" {
		t.Fatal("unapproved group retained body/identity")
	}
	var groups integrations.WeComGroups
	b.owner.json(200, "GET", path, nil, &groups)
	if len(groups.Groups) != 1 || groups.Groups[0].Enabled || groups.Groups[0].Version != 0 {
		t.Fatalf("discovery %+v", groups)
	}
	b.member.json(403, "GET", path, nil, nil)
	b.ownerB.json(404, "GET", "/v1/organizations/"+b.orgB+"/integrations/wecom/bots/"+botID+"/groups", nil, nil)
	patch := func(version int64, enabled bool) integrations.WeComGroup {
		t.Helper()
		var g integrations.WeComGroup
		b.owner.json(200, "PATCH", path, map[string]any{"chat_id": group, "expected_version": version, "enabled": enabled}, &g)
		return g
	}
	if file := os.Getenv("OPENFOX_I19_BROWSER_FILE"); file != "" {
		info, _ := json.Marshal(map[string]string{"api_url": b.server.URL, "organization_id": b.org, "email": "fs-owner-" + b.org[:8] + "@example.test", "member_email": "fs-member-" + b.org[:8] + "@example.test", "password": password, "group_id": group, "bot_id": botID})
		if err := os.WriteFile(file, info, 0600); err != nil {
			t.Fatal(err)
		}
		for {
			if _, err := os.Stat(file + ".stop"); err == nil {
				return
			}
			select {
			case <-b.ctx.Done():
				t.Fatal("browser fixture timeout")
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	initial := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			status, _ := b.owner.do("PATCH", path, map[string]any{"chat_id": group, "expected_version": 0, "enabled": true})
			initial <- status
		}()
	}
	first, second := <-initial, <-initial
	if !((first == 200 && second == 409) || (first == 409 && second == 200)) {
		t.Fatalf("first enable CAS=%d,%d", first, second)
	}
	b.owner.json(200, "GET", path, nil, &groups)
	g := groups.Groups[0]

	if !g.Enabled || g.Version != 1 || g.ConversationID == "" {
		t.Fatalf("enabled %+v", g)
	}
	b.owner.json(409, "PATCH", path, map[string]any{"chat_id": group, "expected_version": 0, "enabled": true}, nil)
	var code integrations.WeComBindingCode
	b.member.json(201, "POST", b.path("/integrations/wecom/personal/codes"), map[string]string{"bot_id": botID}, &code)
	send("group-code", "group", group, "member-external", code.Code, "")
	pump()
	if b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='wecom' AND scope='PERSONAL'`, b.org) != 0 {
		t.Fatal("group consumed private binding code")
	}
	send("private-code", "single", "", "member-external", code.Code, "")
	pump()
	if b.count(`SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND provider='wecom' AND scope='PERSONAL' AND owner_user_id=$2::uuid`, b.org, b.memberID) != 1 {
		t.Fatal("private binding missing")
	}
	send("oversize", "group", group, "member-external", strings.Repeat("中", 7000), strings.Repeat("文", 2000))
	pump()
	b.scan(&status, `SELECT result FROM openfox.im_inbound_messages WHERE organization_id=$1::uuid AND bot_id=$2::uuid AND remote_message_id='oversize'`, b.org, botID)
	if status != "text_too_long" {
		t.Fatalf("oversize=%s", status)
	}
	if b.count(`SELECT count(*) FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND bot_id=$2::uuid AND reply_to_remote_id='oversize'`, b.org, botID) != 1 {
		t.Fatal("oversize must send one explicit rejection")
	}
	send("group-request", "group", group, "member-external", "创建登录修复任务", "登录页面失败")
	pump()
	messages := b.messages(g.ConversationID)
	if len(messages) != 1 || !strings.Contains(messages[0].Body, "登录页面失败") || strings.Contains(messages[0].Body, "读取群聊记录失败") {
		t.Fatalf("accepted context %+v", messages)
	}
	send("group-request", "group", group, "member-external", "duplicate", "")
	pump()
	if len(b.messages(g.ConversationID)) != 1 {
		t.Fatal("duplicate message accepted")
	}
	store, err := dispatch.NewStore(dispatch.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	dw := &dispatch.Worker{Pool: b.pool, Store: store}
	if _, err := dw.RunOrganization(b.ctx, b.org); err != nil {
		t.Fatal(err)
	}
	run := toolRun{conversationID: g.ConversationID, agentID: agentID, actorID: b.memberID, role: "MEMBER"}
	b.scan(&run.runID, `SELECT id::text FROM openfox.runs WHERE organization_id=$1::uuid AND conversation_id=$2::uuid ORDER BY created_at DESC LIMIT 1`, b.org, g.ConversationID)
	// Controlled Runtime admission; the Run and its input were materialized by dispatch.
	b.exec(`UPDATE openfox.runs SET state='RUNNING',started_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '1 hour',computer_id=$3::uuid,grant_id=$4::uuid,grant_version=1 WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, run.runID, b.deviceID, b.grantID)
	result := b.mustCall(run, "space_task_create", "create-login", map[string]any{"title": "登录修复任务"})
	if !result.OK {
		t.Fatalf("create task %+v", result)
	}
	if replay := b.mustCall(run, "space_task_create", "create-login", map[string]any{"title": "登录修复任务"}); replay != result {
		t.Fatal("tool replay differs")
	}
	if b.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND title='登录修复任务'`, b.org) != 1 {
		t.Fatal("task not created exactly once")
	}
	if err := b.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
		_, err := conversations.AppendAgentMessage(ctx, tx, g.ConversationID, agentID, "TEXT", result.Text, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pump()
	mu.Lock()
	found := false
	for _, v := range sent {
		if v["chatid"] == group && strings.Contains(fmt.Sprint(v["markdown"]), "登录修复任务") {
			found = true
			if _, ok := v["chat_type"]; ok {
				t.Error("hardcoded private chat type")
			}
		}
	}
	mu.Unlock()
	if !found {
		t.Fatal("no task reply to original group")
	}
	// Prepare a tool, then disable before the real user transaction: no late write.
	input := dispatch.ToolCall{Generation: 1, CallID: "late-tool", Arguments: json.RawMessage(`{"title":"MUST NOT CREATE"}`)}
	var plan dispatch.SpaceToolPlan
	if err := b.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		plan, err = store.PrepareSpaceTool(ctx, tx, b.org, b.deviceID, run.runID, input)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	send("old-queued", "group", group, "member-external", "old queued", "")
	var queuedReply string
	if err := b.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		queuedReply, err = conversations.AppendAgentMessage(ctx, tx, g.ConversationID, agentID, "TEXT", "OLD QUEUED REPLY", nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	g2 := patch(g.Version, false)
	if _, err := spacetools.Execute(b.ctx, b.pool, b.org, plan, "space_task_create", input); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("late tool = %v", err)
	}
	fresh := patch(g2.Version, true)
	if fresh.ConversationID == g.ConversationID || fresh.Version != 3 {
		t.Fatalf("reopen %+v", fresh)
	}
	pump()
	b.scan(&status, `SELECT status FROM openfox.im_inbound_messages WHERE organization_id=$1::uuid AND bot_id=$2::uuid AND remote_message_id='old-queued'`, b.org, botID)
	var replyStatus string
	b.scan(&replyStatus, `SELECT status FROM openfox.im_reply_outbox WHERE organization_id=$1::uuid AND message_id=$2::uuid`, b.org, queuedReply)
	if replyStatus != "SKIPPED" {
		t.Fatalf("old reply=%s", replyStatus)
	}
	if status != "IGNORED" {
		t.Fatalf("old queue=%s", status)
	}
	if _, err := b.call(run, "space_task_create", "old-run", map[string]any{"title": "MUST NOT CREATE"}); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("old run authorization=%v", err)
	}
	if b.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND title='MUST NOT CREATE'`, b.org) != 0 {
		t.Fatal("disabled group wrote task")
	}
	send("new-request", "group", group, "member-external", "新的会话", "")
	pump()
	if len(b.messages(fresh.ConversationID)) != 1 {
		t.Fatal("new epoch did not accept message")
	}
	send("unbind-race", "group", group, "member-external", "MUST NOT POST", "")
	intercepted := false
	worker.Pool = wecomHookRunner{SyncRunner: b.pool, before: func(selection tenancy.OrganizationSelection) {
		if selection.ActorID == b.memberID && !intercepted {
			intercepted = true
			b.member.json(204, "DELETE", b.path("/integrations/wecom/personal/"+botID), nil, nil)
		}
	}}
	pump()
	worker.Pool = b.pool
	if !intercepted || len(b.messages(fresh.ConversationID)) != 1 {
		t.Fatal("unbind between identity resolution and persistence wrote a message")
	}
	b.scan(&status, `SELECT result FROM openfox.im_inbound_messages WHERE organization_id=$1::uuid AND bot_id=$2::uuid AND remote_message_id='unbind-race'`, b.org, botID)
	if status != "account_unbound" {
		t.Fatalf("unbind race=%s", status)
	}
	// Concurrent CAS: one caller changes the epoch, the other must see 409.
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			status, _ := b.owner.do("PATCH", path, map[string]any{"chat_id": group, "expected_version": fresh.Version, "enabled": false})
			statuses <- status
		}()
	}
	one, two := <-statuses, <-statuses
	if !((one == 200 && two == 409) || (one == 409 && two == 200)) {
		t.Fatalf("concurrent disable=%d,%d", one, two)
	}

	b.exec(`INSERT INTO openfox.im_inbound_messages(organization_id,bot_id,remote_message_id,chat_id,chat_type,status,result,finished_at) SELECT $1::uuid,$2::uuid,'page-'||i,'page-'||lpad(i::text,3,'0'),'group','IGNORED','group_disabled',clock_timestamp() FROM generate_series(1,51) i`, b.org, botID)
	var page1, page2 integrations.WeComGroups
	b.owner.json(200, "GET", path, nil, &page1)
	b.owner.json(200, "GET", path+"?after="+page1.NextCursor, nil, &page2)
	if len(page1.Groups) != 50 || len(page2.Groups) != 2 || page2.NextCursor != "" {
		t.Fatalf("pagination sizes %d,%d", len(page1.Groups), len(page2.Groups))
	}
	for _, a := range page1.Groups {
		for _, z := range page2.Groups {
			if a.ChatID == z.ChatID {
				t.Fatal("duplicate keyset row")
			}
		}
	}
	// A bot-removal transaction must wait for an already admitted task write.
	// No actual write can commit after a completed removal using a stale preflight.
	active := patch(fresh.Version+1, true)
	raceRun := toolRun{runID: b.newUUID(), conversationID: active.ConversationID, agentID: agentID, actorID: b.ownerID, role: "OWNER"}
	b.exec(`INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state,started_at,lease_expires_at) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,1,1,$8::uuid,'RUNNING',clock_timestamp(),clock_timestamp()+interval '1 hour')`, b.org, raceRun.runID, b.foxSpace, raceRun.conversationID, agentID, b.deviceID, b.grantID, b.ownerID)
	raceInput := dispatch.ToolCall{Generation: 1, CallID: "before-remove", Arguments: json.RawMessage(`{"title":"Committed before bot removal"}`)}
	var racePlan dispatch.SpaceToolPlan
	if err := b.deviceTx(func(ctx context.Context, tx pgx.Tx) error {
		var err error
		racePlan, err = store.PrepareSpaceTool(ctx, tx, b.org, b.deviceID, raceRun.runID, raceInput)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	locked, release := make(chan struct{}), make(chan struct{})
	toolDone := make(chan error, 1)
	gate := wecomHookRunner{SyncRunner: b.pool, inside: func(ctx context.Context, tx pgx.Tx) error {
		if err := conversations.LockIMGroupInvocation(ctx, tx, active.ConversationID); err != nil {
			return err
		}
		close(locked)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	go func() {
		result, err := spacetools.Execute(b.ctx, gate, b.org, racePlan, "space_task_create", raceInput)
		if err == nil && !result.OK {
			err = fmt.Errorf("tool: %s", result.Text)
		}
		toolDone <- err
	}()
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("write did not acquire gate")
	}
	removed := make(chan int, 1)
	go func() {
		status, _ := b.owner.do("POST", b.path("/integrations/wecom/bots/"+botID+"/remove"), map[string]int{"version": 1})
		removed <- status
	}()
	waitFor(t, "bot removal waits for admitted write", func() bool {
		return b.count(`SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%im_bots%' AND query LIKE '%FOR UPDATE%'`) > 0
	})
	close(release)
	if err := <-toolDone; err != nil {
		t.Fatal(err)
	}
	if code := <-removed; code != 204 {
		t.Fatalf("bot removal=%d", code)
	}
	raceInput.CallID = "after-remove"
	raceInput.Arguments = json.RawMessage(`{"title":"MUST NOT CREATE"}`)
	if _, err := spacetools.Execute(b.ctx, b.pool, b.org, racePlan, "space_task_create", raceInput); !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
		t.Fatalf("post removal tool=%v", err)
	}
	if b.count(`SELECT count(*) FROM openfox.tasks WHERE organization_id=$1::uuid AND title='Committed before bot removal'`, b.org) != 1 {
		t.Fatal("admitted write not committed before remove")
	}

	privateAgent := b.createAgent(b.owner, b.secSpace, "Private assistant", "SHARED")
	b.exec(`UPDATE openfox.agents SET builtin_kind='TASK_ASSISTANT',allow_assign=false WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, privateAgent)
	privateBot, privateConn := b.newUUID(), b.newUUID()
	b.exec(`INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,created_by) VALUES($1::uuid,$2::uuid,'wecom','ORGANIZATION',$3,'Private fixture','task_assistant',$4::uuid)`, b.org, privateConn, "private-"+b.org, b.ownerID)
	b.exec(`INSERT INTO openfox.im_bots(organization_id,id,connection_id,kind,agent_id,space_id,created_by) VALUES($1::uuid,$2::uuid,$3::uuid,'TASK_ASSISTANT',$4::uuid,$5::uuid,$6::uuid)`, b.org, privateBot, privateConn, privateAgent, b.secSpace, b.ownerID)
	b.exec(`INSERT INTO openfox.im_inbound_messages(organization_id,bot_id,remote_message_id,chat_id,chat_type,status,result,finished_at) VALUES($1::uuid,$2::uuid,'private-discovery','private-group','group','IGNORED','group_disabled',clock_timestamp())`, b.org, privateBot)
	codeStatus, _ := b.admin.do("PATCH", b.path("/integrations/wecom/bots/"+privateBot+"/groups"), map[string]any{"chat_id": "private-group", "expected_version": 0, "enabled": true})
	if codeStatus != 403 && codeStatus != 404 {
		t.Fatalf("private space denied manager must return 403/404, got %d", codeStatus)
	}
	if b.count(`SELECT count(*) FROM openfox.wecom_group_authorizations WHERE organization_id=$1::uuid AND bot_id=$2::uuid`, b.org, privateBot) != 0 {
		t.Fatal("private ACL bypass")
	}
	b.exec(`UPDATE openfox.integration_connections SET status='DISCONNECTED',disconnected_at=clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, privateConn)

}

// Inject a state change between real transactions, without mocking SQL/results.
type wecomHookRunner struct {
	integrations.SyncRunner
	before func(tenancy.OrganizationSelection)
	inside func(context.Context, pgx.Tx) error
}

func (r wecomHookRunner) RunForOrganization(ctx context.Context, org string, source tenancy.Source, selector tenancy.OrganizationSelector, fn func(context.Context, pgx.Tx) error) error {
	selection, err := selector.SelectOrganization(ctx, org)
	if err != nil {
		return err
	}
	if r.before != nil {
		r.before(selection)
	}
	return r.SyncRunner.RunForOrganization(ctx, org, source, selector, func(ctx context.Context, tx pgx.Tx) error {
		if r.inside != nil {
			if err := r.inside(ctx, tx); err != nil {
				return err
			}
		}
		return fn(ctx, tx)
	})
}
