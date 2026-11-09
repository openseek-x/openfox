package stage2

// 对话导航集成测试夹具：复用 agEnv（真实 HTTP 处理器 + 每个测试独立的两个组织），
// 在其上补会话、消息、Run 的构造工具。Run 与 Agent 消息直接用超级用户写库，以便精确构造状态与时间。

import (
	"net/http"
	"strconv"
)

type convSummary struct {
	ID          string  `json:"id"`
	SpaceID     string  `json:"space_id"`
	AgentID     string  `json:"agent_id"`
	TaskID      string  `json:"task_id"`
	OwnerUserID string  `json:"owner_user_id"`
	Lifecycle   string  `json:"lifecycle"`
	Handoff     string  `json:"handoff"`
	Version     int     `json:"version"`
	UpdatedAt   string  `json:"updated_at"`
	Title       *string `json:"title"`
	ArchivedAt  *string `json:"archived_at"`
	Pinned      bool    `json:"pinned"`
	LastReadSeq int64   `json:"last_read_seq"`
	Unread      bool    `json:"unread"`
	LastMessage *struct {
		Seq        int64  `json:"seq"`
		AuthorKind string `json:"author_kind"`
		Preview    string `json:"preview"`
	} `json:"last_message"`
	RecentMessages []struct {
		Seq        int64  `json:"seq"`
		AuthorKind string `json:"author_kind"`
		AuthorName string `json:"author_name"`
		Preview    string `json:"preview"`
	} `json:"recent_messages"`
	Agent struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Online bool   `json:"online"`
	} `json:"agent"`
	Task *struct {
		ID        string `json:"id"`
		DisplayID string `json:"display_id"`
		Title     string `json:"title"`
	} `json:"task"`
	Run *struct {
		ID    string `json:"id"`
		State string `json:"state"`
	} `json:"run"`
	Status     string  `json:"status"`
	StatusMark *string `json:"status_mark"`
}

type convPage struct {
	Conversations []convSummary `json:"conversations"`
	NextCursor    *string       `json:"next_cursor"`
}

type convMessage struct {
	ID         string  `json:"id"`
	Seq        int64   `json:"seq"`
	AuthorKind string  `json:"author_kind"`
	Kind       string  `json:"kind"`
	RunID      *string `json:"run_id"`
}

type convMessagePage struct {
	Messages   []convMessage `json:"messages"`
	NextCursor int64         `json:"next_cursor"`
}

// convFixture 是一个 SHARED 空间 + 其中一个共享 Agent（由 owner 创建，允许单聊）。
type convFixture struct {
	env     *agEnv
	space   string
	agentID string
}

func newConvFixture(e *agEnv) convFixture {
	e.t.Helper()
	space := e.createSpace("shared")
	agent := e.createAgent(e.client(agOwnerID), space, "SHARED", nil)
	return convFixture{env: e, space: space, agentID: agent.ID}
}

func conversationPath(id string) string { return "conversations/" + id }

// newConversation 由 client 新建一个不挂任务的会话。
func (f convFixture) newConversation(client *agClient) convSummary {
	f.env.t.Helper()
	var created convSummary
	client.must(http.StatusCreated, http.MethodPost, "conversations",
		map[string]any{"space_id": f.space, "agent_id": f.agentID}, &created)
	return created
}

func (f convFixture) get(client *agClient, id string) convSummary {
	f.env.t.Helper()
	var item convSummary
	client.must(http.StatusOK, http.MethodGet, conversationPath(id), nil, &item)
	return item
}

// post 发一条不 @ Agent 的消息（不触发执行），返回 seq。
func (f convFixture) post(client *agClient, id, body string) int64 {
	f.env.t.Helper()
	var message convMessage
	client.must(http.StatusCreated, http.MethodPost, conversationPath(id)+"/messages",
		map[string]any{"body": body, "client_message_id": "c-" + agSuffix(f.env.t)}, &message)
	return message.Seq
}

// agentMessage 以 Agent 身份追加一条消息（与 FinalizeConversation 写法一致：递增 next_message_seq 与 version）。
func (f convFixture) agentMessage(id, kind string) (string, int64) {
	f.env.t.Helper()
	var seq int64
	f.env.scalar(&seq, `UPDATE openfox.conversations
		   SET next_message_seq = next_message_seq + 1, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = $1::uuid AND id = $2::uuid RETURNING next_message_seq - 1`, f.env.org, id)
	messageID := agNewID(f.env.t)
	var payload any
	if kind != "TEXT" {
		payload = `{"action":"create_task","status":"pending"}`
	}
	f.env.exec(`INSERT INTO openfox.messages (organization_id, id, conversation_id, seq, author_kind, author_agent_id, body, kind, payload)
		SELECT c.organization_id, $3::uuid, c.id, $4, 'AGENT', c.agent_id, 'agent reply', $5, $6::jsonb
		  FROM openfox.conversations c WHERE c.organization_id = $1::uuid AND c.id = $2::uuid`,
		f.env.org, id, messageID, seq, kind, payload)
	return messageID, seq
}

// insertRun 直接写一条 Run。终态写 finished_at，取消类写 cancel_requested_at；运行中写 started_at。
func (f convFixture) insertRun(conversationID, state string) string {
	f.env.t.Helper()
	id := agNewID(f.env.t)
	f.env.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, task_id, agent_id, computer_id, grant_id,
			config_version, grant_version, actor_id, state, started_at, finished_at, cancel_requested_at)
		SELECT c.organization_id, $2::uuid, c.space_id, c.id, c.task_id, c.agent_id, a.device_id, g.id,
		       a.current_config_version, g.permission_version, c.owner_user_id, $3,
		       CASE WHEN $3 IN ('RUNNING', 'CANCEL_REQUESTED', 'SUCCEEDED', 'FAILED') THEN clock_timestamp() END,
		       CASE WHEN $3 IN ('SUCCEEDED', 'FAILED', 'CANCELED') THEN clock_timestamp() END,
		       CASE WHEN $3 IN ('CANCEL_REQUESTED', 'CANCELED') THEN clock_timestamp() END
		  FROM openfox.conversations c
		  JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.agent_id
		  JOIN openfox.device_grants g ON g.organization_id = a.organization_id AND g.device_id = a.device_id
		 WHERE c.organization_id = $1::uuid AND c.id = $4::uuid`,
		f.env.org, id, state, conversationID)
	return id
}

// finishRun 把 Run 改为终态（finished_at = 当前时间）。
func (f convFixture) finishRun(runID, state string) {
	f.env.t.Helper()
	f.env.exec(`UPDATE openfox.runs SET state = $3, finished_at = clock_timestamp(),
		       cancel_requested_at = CASE WHEN $3 = 'CANCELED' THEN COALESCE(cancel_requested_at, clock_timestamp()) END
		 WHERE organization_id = $1::uuid AND id = $2::uuid`, f.env.org, runID, state)
}

// createTask 由 owner 经 API 在空间里建任务，返回 ID。
func (f convFixture) createTask(spaceID, title string) string {
	f.env.t.Helper()
	var task struct {
		ID string `json:"id"`
	}
	f.env.client(agOwnerID).must(http.StatusCreated, http.MethodPost, "spaces/"+spaceID+"/tasks",
		map[string]any{"title": title}, &task)
	return task.ID
}

// listAll 按 limit 翻完全部页，返回 ID 序列。
func (f convFixture) listAll(client *agClient, query string, limit int) []string {
	f.env.t.Helper()
	ids := []string{}
	cursor := ""
	for page := 0; page < 50; page++ {
		path := "conversations?limit=" + strconv.Itoa(limit) + query
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var result convPage
		client.must(http.StatusOK, http.MethodGet, path, nil, &result)
		for _, item := range result.Conversations {
			ids = append(ids, item.ID)
		}
		if result.NextCursor == nil {
			return ids
		}
		cursor = *result.NextCursor
	}
	f.env.t.Fatal("pagination did not terminate")
	return nil
}
