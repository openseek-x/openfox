package home

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/agents"
	"openfox/internal/api"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/devices"
	"openfox/internal/dispatch"
	"openfox/internal/execution"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// 小助手：独立的成员，ivy 是 alice 私有空间的 EDITOR，ana 没有任何可管理的空间。
const (
	asIvy        = "00000000-0000-0000-0000-000000000d71"
	asAna        = "00000000-0000-0000-0000-000000000d72"
	asTaskSpace  = "00000000-0000-0000-0000-000000000d73"
	asTaskStatus = "00000000-0000-0000-0000-000000000d74"
	asTaskType   = "00000000-0000-0000-0000-000000000d75"
)

type assistantMessage struct {
	AssistantAction string          `json:"assistant_action"`
	ID              string          `json:"id"`
	AuthorKind      string          `json:"author_kind"`
	Body            string          `json:"body"`
	Kind            string          `json:"kind"`
	Payload         json.RawMessage `json:"payload"`
}

func TestPostgresHomeAssistant(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{
		MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash("home assistant password")
	if err != nil {
		t.Fatal(err)
	}
	seed, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
		  ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash,
		      credential_version = 1, status = 'ACTIVE', updated_at = clock_timestamp()`, []any{homeOwnerA, hash}},
		{`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, 'ivy@example.test'), ($2::uuid, 'ana@example.test')`, []any{asIvy, asAna}},
		{`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER'), ($1::uuid, $3::uuid, 'MEMBER')`, []any{homeOrgA, asIvy, asAna}},
		{`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status) VALUES ($1::uuid, $2::uuid, $3::uuid, 'EDITOR', 'ACTIVE')`, []any{homeOrgA, homeSpaceA, asIvy}},
		// 草稿确认创建任务用的空间：自带默认任务类型与工作流，不依赖其他测试的数据。
		{`INSERT INTO openfox.spaces (organization_id, id, name, visibility, prefix) VALUES ($1::uuid, $2::uuid, 'Assistant tasks', 'PRIVATE', 'astk')`, []any{homeOrgA, asTaskSpace}},
		{`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status) VALUES ($1::uuid, $2::uuid, $3::uuid, 'OWNER', 'ACTIVE')`, []any{homeOrgA, asTaskSpace, homeOwnerA}},
		{`INSERT INTO openfox.task_statuses (organization_id, id, name, category, position) VALUES ($1::uuid, $2::uuid, 'Assistant TODO', 'TODO', 41)`, []any{homeOrgA, asTaskStatus}},
		{`INSERT INTO openfox.task_types (organization_id, space_id, id, name, is_default) VALUES ($1::uuid, $2::uuid, $3::uuid, 'Assistant type', true)`, []any{homeOrgA, asTaskSpace, asTaskType}},
		{`INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 0, true)`, []any{homeOrgA, asTaskSpace, asTaskType, asTaskStatus}},
	} {
		if _, err := seed.Exec(ctx, statement.query, statement.args...); err != nil {
			_ = seed.Close(ctx)
			t.Fatal(err)
		}
	}
	if err := seed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := harness{t: t, ctx: ctx, pool: pool}
	store := agents.PostgresStore{Pool: pool}
	as := func(user string, role policy.OrganizationRole) (tenancy.OrganizationSelection, agents.Actor) {
		return tenancy.OrganizationSelection{OrganizationID: homeOrgA, ActorID: user, AuthorizationVersion: 1},
			agents.Actor{UserID: user, Role: role, AuthorizationVersion: 1, RequestID: "home-assistant"}
	}
	aliceSel, aliceActor := as(homeOwnerA, policy.OrganizationRoleOwner)
	deviceList, err := (devices.PostgresStore{Pool: pool}).List(ctx, aliceSel)
	if err != nil {
		t.Fatal(err)
	}
	grantID := ""
	for _, device := range deviceList {
		if device.ID == adDevice {
			grantID = device.GrantID
		}
	}
	if grantID == "" {
		t.Fatal("fixture device grant not found")
	}

	server := httptest.NewServer(api.New(identity.Service{
		Store: identity.PostgresStore{Pool: pool}, PasswordHasher: hasher,
		SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour, Now: time.Now,
	}, pool, false))
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	login, err := client.Post(server.URL+"/v1/auth/login", "application/json", strings.NewReader(
		`{"email":"alice@example.test","password":"home assistant password","client_kind":"web"}`))
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.StatusCode != http.StatusCreated {
		t.Fatalf("login status = %d", login.StatusCode)
	}
	csrf := ""
	for _, cookie := range jar.Cookies(mustURL(t, server.URL)) {
		if cookie.Name == "openfox_csrf" {
			csrf = cookie.Value
		}
	}
	org := "/v1/organizations/" + homeOrgA
	call := func(method, path, body string, withCSRF bool, out any) int {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request, err := http.NewRequest(method, server.URL+org+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if withCSRF {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if out != nil {
			_ = json.NewDecoder(response.Body).Decode(out)
		}
		return response.StatusCode
	}
	mustJSON := func(value any) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}

	// 1. 还没有小助手：GET 返回 null；PUT 需要 CSRF；首次选 Runtime 时创建。
	var got struct {
		Assistant *agents.Agent `json:"assistant"`
	}
	if status := call(http.MethodGet, "/assistant", "", false, &got); status != http.StatusOK || got.Assistant != nil {
		t.Fatalf("get assistant before ensure = %d %+v", status, got.Assistant)
	}
	runtime := mustJSON(map[string]string{"computer_id": adDevice, "grant_id": grantID})
	if status := call(http.MethodPut, "/assistant", runtime, false, nil); status != http.StatusForbidden {
		t.Fatalf("ensure without CSRF = %d", status)
	}
	if status := call(http.MethodPut, "/assistant", runtime, true, &got); status != http.StatusOK || got.Assistant == nil {
		t.Fatalf("ensure assistant = %d", status)
	}
	assistant := *got.Assistant
	if assistant.BuiltinKind != agents.BuiltinAssistant || assistant.Name != "小助手" || assistant.Visibility != agents.VisibilityPrivate ||
		assistant.Config == nil || assistant.Config.Runtime != "codex" || assistant.Config.Model != "default" || assistant.Config.ComputerID != adDevice {
		t.Fatalf("assistant = %+v config %+v", assistant, assistant.Config)
	}
	// 同样的输入幂等；换模型追加配置版本。
	if status := call(http.MethodPut, "/assistant", runtime, true, &got); status != http.StatusOK || got.Assistant.ID != assistant.ID || got.Assistant.Version != assistant.Version {
		t.Fatalf("repeat ensure = %d %+v", status, got.Assistant)
	}
	if status := call(http.MethodPut, "/assistant", mustJSON(map[string]string{"computer_id": adDevice, "grant_id": grantID, "model": "gpt-5"}), true, &got); status != http.StatusOK ||
		got.Assistant.CurrentConfigVersion != assistant.CurrentConfigVersion+1 || got.Assistant.Config.Model != "gpt-5" {
		t.Fatalf("ensure with model = %d %+v", status, got.Assistant)
	}
	assistant = *got.Assistant
	if status := call(http.MethodPut, "/assistant", `{"computer_id":"nope","grant_id":"nope"}`, true, nil); status != http.StatusBadRequest {
		t.Fatalf("ensure invalid computer = %d", status)
	}

	// 2. 内置助手不进目录、不进空间列表，也不能通过通用接口改名或改配置。
	directory, err := store.Directory(ctx, aliceSel, aliceActor, agents.DirectoryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range directory.Entries {
		if entry.ID == assistant.ID {
			t.Fatal("assistant leaked into the directory")
		}
	}
	listed, err := store.List(ctx, aliceSel, aliceActor, assistant.SpaceID, agents.ListOptions{IncludeArchived: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range listed {
		if item.ID == assistant.ID {
			t.Fatal("assistant leaked into the space Agent list")
		}
	}
	rename := "改名"
	if _, err := store.Update(ctx, aliceSel, aliceActor, assistant.SpaceID, assistant.ID, agents.UpdateInput{Name: &rename, Version: assistant.Version}); !errors.Is(err, agents.ErrForbidden) {
		t.Fatalf("update builtin error = %v", err)
	}
	if _, err := store.CreateConfig(ctx, aliceSel, aliceActor, assistant.SpaceID, assistant.ID, assistant.Version, agents.ConfigInput{
		ComputerID: adDevice, GrantID: grantID, Runtime: "codex", Model: "gpt-5",
	}); !errors.Is(err, agents.ErrForbidden) {
		t.Fatalf("create config on builtin error = %v", err)
	}
	// 每人一个：ivy 的助手同名不冲突；ana 没有可管理的空间。
	ivySel, ivyActor := as(asIvy, policy.OrganizationRoleMember)
	ivyAssistant, err := store.EnsureAssistant(ctx, ivySel, ivyActor, agents.AssistantInput{ComputerID: adDevice, GrantID: grantID})
	if err != nil || ivyAssistant.ID == assistant.ID || ivyAssistant.OwnerUserID != asIvy {
		t.Fatalf("ivy assistant = %+v, %v", ivyAssistant, err)
	}
	anaSel, anaActor := as(asAna, policy.OrganizationRoleMember)
	if _, err := store.EnsureAssistant(ctx, anaSel, anaActor, agents.AssistantInput{ComputerID: adDevice, GrantID: grantID}); !errors.Is(err, agents.ErrNoAssistantSpace) {
		t.Fatalf("ana ensure error = %v", err)
	}

	// 3. 会话：只能是本人私有会话；别人不能和你的助手开会话。
	conversationID, err := collaboration.NewID()
	if err != nil {
		t.Fatal(err)
	}
	newConversation := func(visibility string) string {
		return mustJSON(map[string]string{"id": conversationID, "space_id": assistant.SpaceID, "agent_id": assistant.ID, "visibility": visibility})
	}
	if status := call(http.MethodPost, "/conversations", newConversation("SHARED"), true, nil); status != http.StatusBadRequest {
		t.Fatalf("shared assistant conversation = %d", status)
	}
	if status := call(http.MethodPost, "/conversations", newConversation("PRIVATE"), true, nil); status != http.StatusCreated {
		t.Fatalf("create assistant conversation = %d", status)
	}
	h.run(asIvy, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		otherID, err := collaboration.NewID()
		if err != nil {
			return err
		}
		_, err = (conversations.Service{}).Create(ctx, tx, homeOrgA, actor, conversations.CreateInput{
			ID: otherID, SpaceID: assistant.SpaceID, AgentID: assistant.ID, Visibility: "PRIVATE",
		})
		if !errors.Is(err, conversations.ErrNotFound) && !errors.Is(err, conversations.ErrForbidden) {
			t.Fatalf("ivy conversation with alice assistant error = %v", err)
		}
		return nil
	})

	// 4. 不需要 @：每条消息都生成执行输入。
	var posted assistantMessage
	postPath := "/conversations/" + conversationID + "/messages"
	request := `{"body":"整理每周进展","assistant_action":"create_agent","client_message_id":"assistant-test-1"}`
	if status := call(http.MethodPost, postPath, request, true, &posted); status != http.StatusCreated || posted.AssistantAction != "create_agent" || posted.Body != "整理每周进展" {
		t.Fatalf("post message = %d", status)
	}
	var replay assistantMessage
	if status := call(http.MethodPost, postPath, request, true, &replay); status != http.StatusCreated || replay.ID != posted.ID || replay.AssistantAction != posted.AssistantAction {
		t.Fatalf("replay = %d %+v", status, replay)
	}
	if status := call(http.MethodPost, postPath, strings.Replace(request, "create_agent", "create_task", 1), true, nil); status != http.StatusConflict {
		t.Fatalf("conflicting assistant action = %d", status)
	}
	if status := call(http.MethodPost, postPath, strings.Replace(request, "create_agent", "unknown", 1), true, nil); status != http.StatusBadRequest {
		t.Fatalf("unknown assistant action = %d", status)
	}
	h.run(homeOwnerA, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		var inputs int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.conversation_inputs WHERE message_id = $1::uuid`, posted.ID).Scan(&inputs); err != nil {
			return err
		}
		if inputs != 1 {
			t.Fatalf("implicit run inputs = %d", inputs)
		}
		return nil
	})

	// 5. 助手回复里的草稿块拆成正文 + 草稿卡；不合法的块原样保留为正文。
	finalize := func(body string) {
		t.Helper()
		h.run(homeOwnerA, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
			runID, err := collaboration.NewID()
			if err != nil {
				return err
			}
			_, err = dispatch.FinalizeConversation(ctx, tx, execution.Run{
				ID: runID, ConversationID: conversationID, AgentID: assistant.ID, ActorID: homeOwnerA, State: execution.StateSucceeded,
			}, dispatch.AuthorAgent, body, false)
			return err
		})
	}
	finalize("好的，草稿如下：\n```openfox-action\n{\"action\":\"create_agent\",\"agent\":{\"name\":\"周报助手\",\"instructions\":\"每周五汇总进展\\n列出风险\"}}\n```")
	finalize("```openfox-action\n{\"action\":\"create_task\",\"task\":{\"title\":\"整理本周周报\",\"description\":\"周五前\"}}\n```")
	finalize("```openfox-action\n{not json}\n```")
	finalize("```openfox-action\n{\"action\":\"create_agent\",\"agent\":{\"name\":\"要取消的\"}}\n```")
	var page struct {
		Messages []assistantMessage `json:"messages"`
	}
	loadMessages := func() []assistantMessage {
		t.Helper()
		if status := call(http.MethodGet, "/conversations/"+conversationID+"/messages?limit=50", "", false, &page); status != http.StatusOK {
			t.Fatalf("list messages = %d", status)
		}
		return page.Messages
	}
	messages := loadMessages()
	if len(messages) == 0 || messages[0].Body != posted.Body || messages[0].AssistantAction != "create_agent" {
		t.Fatal("missing assistant action or modified history body")
	}
	kinds := make([]string, 0, len(messages))
	for _, message := range messages {
		kinds = append(kinds, message.AuthorKind+":"+message.Kind)
	}
	want := "USER:TEXT AGENT:TEXT AGENT:ACTION_PROPOSAL AGENT:ACTION_PROPOSAL AGENT:TEXT AGENT:ACTION_PROPOSAL"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("message kinds = %v, want %s", kinds, want)
	}
	if messages[1].Body != "好的，草稿如下：" || !strings.Contains(messages[4].Body, "openfox-action") {
		t.Fatalf("split bodies = %q / %q", messages[1].Body, messages[4].Body)
	}
	agentProposal, taskProposal, cancelProposal := messages[2], messages[3], messages[5]
	var payload conversations.ProposalPayload
	if err := json.Unmarshal(agentProposal.Payload, &payload); err != nil || payload.Status != "pending" || payload.Agent == nil ||
		payload.Agent.Name != "周报助手" || payload.Agent.Instructions != "每周五汇总进展\n列出风险" || agentProposal.Body != "已生成：周报助手" {
		t.Fatalf("agent proposal = %+v %s, %v", payload, agentProposal.Body, err)
	}

	// 6. 会话选择器：标题取第一条用户消息，可重命名、归档。
	var sessions struct {
		Sessions []conversations.AssistantSession `json:"sessions"`
	}
	if status := call(http.MethodGet, "/assistant/sessions", "", false, &sessions); status != http.StatusOK || len(sessions.Sessions) != 1 ||
		sessions.Sessions[0].ID != conversationID || sessions.Sessions[0].Title != "整理每周进展" {
		t.Fatalf("sessions = %+v", sessions.Sessions)
	}
	var session conversations.AssistantSession
	if status := call(http.MethodPatch, "/conversations/"+conversationID+"/session", `{"title":"周报"}`, true, &session); status != http.StatusOK || session.Title != "周报" {
		t.Fatalf("rename session = %d %+v", status, session)
	}
	if status := call(http.MethodPatch, "/conversations/"+conversationID+"/session", `{"archived":true}`, true, &session); status != http.StatusOK || session.ArchivedAt == nil || session.Title != "周报" {
		t.Fatalf("archive session = %d %+v", status, session)
	}
	// 已归档的会话从通用会话列表（侧栏「对话」）收起，恢复后回来
	inConversationList := func() bool {
		t.Helper()
		var list struct {
			Conversations []conversations.Conversation `json:"conversations"`
		}
		if status := call(http.MethodGet, "/conversations?limit=50", "", false, &list); status != http.StatusOK {
			t.Fatalf("list conversations = %d", status)
		}
		for _, item := range list.Conversations {
			if item.ID == conversationID {
				return true
			}
		}
		return false
	}
	if inConversationList() {
		t.Fatal("archived session still in conversation list")
	}
	session = conversations.AssistantSession{}
	if status := call(http.MethodPatch, "/conversations/"+conversationID+"/session", `{"archived":false}`, true, &session); status != http.StatusOK || session.ArchivedAt != nil || session.Title != "周报" {
		t.Fatalf("unarchive session = %d %+v", status, session)
	}
	if !inConversationList() {
		t.Fatal("unarchived session missing from conversation list")
	}
	if status := call(http.MethodPatch, "/conversations/"+conversationID+"/session", `{}`, true, nil); status != http.StatusBadRequest {
		t.Fatalf("empty session patch = %d", status)
	}

	// 7. 确认：CSRF、按编辑后的字段创建、失败整体回滚、重复确认 409。
	confirm := func(id, body string, withCSRF bool, out any) int {
		return call(http.MethodPost, "/conversations/"+conversationID+"/messages/"+id+"/confirm", body, withCSRF, out)
	}
	agentBody := mustJSON(map[string]string{
		"space_id": asTaskSpace, "name": "周报助手 Pro", "instructions": "每周五汇总进展\n列出风险",
		"computer_id": adDevice, "grant_id": grantID, "runtime": "codex", "visibility": "SHARED",
	})
	if status := confirm(agentProposal.ID, agentBody, false, nil); status != http.StatusForbidden {
		t.Fatalf("confirm without CSRF = %d", status)
	}
	var confirmed struct {
		Proposal assistantMessage `json:"proposal"`
		Executed assistantMessage `json:"executed"`
	}
	if status := confirm(agentProposal.ID, agentBody, true, &confirmed); status != http.StatusOK || confirmed.Executed.Kind != "ACTION_EXECUTED" {
		t.Fatalf("confirm agent = %d %+v", status, confirmed)
	}
	var executed conversations.ExecutedPayload
	if err := json.Unmarshal(confirmed.Executed.Payload, &executed); err != nil || executed.AgentID == "" || executed.Name != "周报助手 Pro" || executed.ProposalID != agentProposal.ID {
		t.Fatalf("executed agent = %+v, %v", executed, err)
	}
	if err := json.Unmarshal(confirmed.Proposal.Payload, &payload); err != nil || payload.Status != "confirmed" || payload.Agent.Name != "周报助手 Pro" || confirmed.Proposal.Body != "已生成：周报助手 Pro" {
		t.Fatalf("confirmed proposal = %s", confirmed.Proposal.Payload)
	}
	created, err := store.Get(ctx, aliceSel, aliceActor, asTaskSpace, executed.AgentID)
	if err != nil || created.Config == nil || created.Config.Instructions != "每周五汇总进展\n列出风险" || created.BuiltinKind != "" || created.Visibility != agents.VisibilityShared || created.Config.ComputerID != adDevice || created.Config.Runtime != "codex" {
		t.Fatalf("created agent = %+v, %v", created, err)
	}
	if status := confirm(agentProposal.ID, agentBody, true, nil); status != http.StatusConflict {
		t.Fatalf("repeat confirm = %d", status)
	}
	// 标题为空：创建失败，草稿保持待确认。
	if status := confirm(taskProposal.ID, mustJSON(map[string]string{"space_id": asTaskSpace, "title": "  "}), true, nil); status != http.StatusBadRequest {
		t.Fatalf("confirm blank task = %d", status)
	}
	for _, message := range loadMessages() {
		if message.ID == taskProposal.ID {
			if err := json.Unmarshal(message.Payload, &payload); err != nil || payload.Status != "pending" {
				t.Fatalf("task proposal after failed confirm = %s", message.Payload)
			}
		}
	}
	if status := confirm(taskProposal.ID, mustJSON(map[string]any{"space_id": asTaskSpace, "title": "整理每周进展", "description": "周四前", "assignee_user_id": homeOwnerA, "executor_agent_id": created.ID, "start_working": false}), true, &confirmed); status != http.StatusOK {
		t.Fatalf("confirm task = %d", status)
	}
	if err := json.Unmarshal(confirmed.Executed.Payload, &executed); err != nil || executed.TaskID == "" || executed.Identifier == "" || executed.Name != "整理每周进展" {
		t.Fatalf("executed task = %+v, %v", executed, err)
	}

	if err := json.Unmarshal(confirmed.Proposal.Payload, &payload); err != nil || payload.Task.Title != "整理每周进展" || payload.Task.Description != "周四前" {
		t.Fatalf("saved edited task draft = %s", confirmed.Proposal.Payload)
	}
	h.run(homeOwnerA, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		var human, agent string
		var starts, activities int
		err := tx.QueryRow(ctx, `SELECT assignee_id::text, executor_agent_id::text,
			(SELECT count(*) FROM openfox.task_agent_events WHERE task_id=t.id AND kind='ASSIGNED'),
			(SELECT count(*) FROM openfox.task_activities WHERE task_id=t.id AND kind='task.created')
			FROM openfox.tasks t WHERE t.id=$1::uuid`, executed.TaskID).Scan(&human, &agent, &starts, &activities)
		if err != nil {
			return err
		}
		if human != homeOwnerA || agent != created.ID || starts != 0 || activities != 1 {
			t.Fatalf("created task human=%s agent=%s starts=%d activities=%d", human, agent, starts, activities)
		}
		return nil
	})

	// 8. 取消：只改状态；重复取消 409；别人锁不到你的草稿。
	h.run(asIvy, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		if _, err := (conversations.Service{}).LockProposal(ctx, tx, homeOrgA, conversationID, cancelProposal.ID, actor); !errors.Is(err, conversations.ErrNotFound) {
			t.Fatalf("ivy lock alice proposal error = %v", err)
		}
		return nil
	})
	var cancelled struct {
		Proposal assistantMessage `json:"proposal"`
	}
	if status := call(http.MethodDelete, "/conversations/"+conversationID+"/messages/"+cancelProposal.ID+"/proposal", "", true, &cancelled); status != http.StatusOK {
		t.Fatalf("cancel proposal = %d", status)
	}
	if err := json.Unmarshal(cancelled.Proposal.Payload, &payload); err != nil || payload.Status != "cancelled" {
		t.Fatalf("cancelled proposal = %s", cancelled.Proposal.Payload)
	}
	if status := call(http.MethodDelete, "/conversations/"+conversationID+"/messages/"+cancelProposal.ID+"/proposal", "", true, nil); status != http.StatusConflict {
		t.Fatalf("repeat cancel = %d", status)
	}
	if status := call(http.MethodDelete, "/conversations/"+conversationID+"/messages/"+posted.ID+"/proposal", "", true, nil); status != http.StatusNotFound {
		t.Fatalf("cancel a text message = %d", status)
	}
	if got := len(loadMessages()); got != 8 {
		t.Fatalf("messages after confirm = %d, want 8", got)
	}
	retryOriginalAssistantRun(t, h, call, conversationID, assistant.SpaceID, created.ID, posted.ID)
	verifyAssistantDirectory(t, h, call, assistant, grantID)
}
