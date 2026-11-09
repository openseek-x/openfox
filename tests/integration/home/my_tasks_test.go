package home

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
)

// 「我的任务」用独立的成员与空间，避免和收件箱测试的数据互相影响。
const (
	mtUserD       = "00000000-0000-0000-0000-000000000e01"
	mtUserE       = "00000000-0000-0000-0000-000000000e02"
	mtGuest       = "00000000-0000-0000-0000-000000000e03"
	mtShared      = "00000000-0000-0000-0000-000000000e10"
	mtPrivate     = "00000000-0000-0000-0000-000000000e11"
	mtTodo        = "00000000-0000-0000-0000-000000000e20"
	mtDone        = "00000000-0000-0000-0000-000000000e21"
	mtCancelled   = "00000000-0000-0000-0000-000000000e22"
	mtTypeA       = "00000000-0000-0000-0000-000000000e30"
	mtTypeShared  = "00000000-0000-0000-0000-000000000e31"
	mtTypePrivate = "00000000-0000-0000-0000-000000000e32"
	mtAssigned    = "00000000-0000-0000-0000-000000000e41"
	mtCreated     = "00000000-0000-0000-0000-000000000e42"
	mtInShared    = "00000000-0000-0000-0000-000000000e43"
	mtInPrivate   = "00000000-0000-0000-0000-000000000e44"
	mtCancelledID = "00000000-0000-0000-0000-000000000e45"
	mtBoth        = "00000000-0000-0000-0000-000000000e46"
	mtGuestTask   = "00000000-0000-0000-0000-000000000e47"
)

func TestPostgresHomeMyTasks(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	superuser := seedMyTasks(t, ctx, url)
	defer superuser.Close(ctx)
	pool, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := harness{t: t, ctx: ctx, pool: pool}
	service := collaboration.Service{}
	asActor := func(user string, role policy.OrganizationRole) collaboration.Actor {
		return collaboration.Actor{UserID: user, Role: role, AuthorizationVersion: 1}
	}
	list := func(user string, role policy.OrganizationRole, query collaboration.MyTasksQuery) (collaboration.MyTasksPage, error) {
		t.Helper()
		if query.Limit == 0 {
			query.Limit = 50
		}
		var page collaboration.MyTasksPage
		var listErr error
		h.run(user, role, func(ctx context.Context, tx pgx.Tx, _ conversations.Actor) error {
			page, listErr = service.ListMyTasks(ctx, tx, homeOrgA, asActor(user, role), query)
			return nil
		})
		return page, listErr
	}
	mustList := func(query collaboration.MyTasksQuery) collaboration.MyTasksPage {
		t.Helper()
		page, err := list(mtUserD, policy.OrganizationRoleMember, query)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	ids := func(page collaboration.MyTasksPage) []string {
		result := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			result = append(result, item.ID)
		}
		return result
	}
	sameSet := func(label string, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
		seen := map[string]bool{}
		for _, id := range got {
			seen[id] = true
		}
		for _, id := range want {
			if !seen[id] {
				t.Fatalf("%s = %v, want %v", label, got, want)
			}
		}
	}

	// 1. 通过真实创建路径写入 task.created 活动：D 自己创建一个任务、再创建一个指派给自己的任务。
	createAsD := func(id, title, assignee string) {
		h.run(mtUserD, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, _ conversations.Actor) error {
			task, err := service.CreateTask(ctx, tx, homeOrgA, homeSpaceA, asActor(mtUserD, policy.OrganizationRoleMember),
				collaboration.TaskInput{ID: id, TypeID: mtTypeA, Title: title, AssigneeUserID: assignee})
			if err != nil {
				return err
			}
			return service.RecordActivity(ctx, tx, homeOrgA, homeSpaceA, task.ID, asActor(mtUserD, policy.OrganizationRoleMember),
				"task.created", "my-tasks:"+id, map[string]any{"title": task.Title})
		})
	}
	createAsD(mtCreated, "D created", "")
	createAsD(mtBoth, "D created for self", mtUserD)

	// 2. 全部 = 指派给我 ∪ 我创建的；无权访问的私有空间被排除（已取消任务不再被排除）；SHARED 空间对非成员可见。
	all := mustList(collaboration.MyTasksQuery{})
	sameSet("all", ids(all), []string{mtAssigned, mtCreated, mtInShared, mtBoth, mtCancelledID})
	if all.Items[0].ID != mtBoth || all.Items[1].ID != mtCreated {
		t.Fatalf("order = %v, want most recently updated first", ids(all))
	}
	for _, item := range all.Items {
		switch item.ID {
		case mtBoth:
			if !item.AssignedToMe || !item.CreatedByMe {
				t.Fatalf("both flags = %+v", item)
			}
		case mtAssigned:
			if !item.AssignedToMe || item.CreatedByMe || item.ExecutorAgentName != "Legacy capacity agent" {
				t.Fatalf("assigned item = %+v", item)
			}
		case mtInShared:
			if item.SpaceName != "Shared space" || item.DisplayID != "shr-1" {
				t.Fatalf("shared item = %+v", item)
			}
		}
	}
	sameSet("assigned", ids(mustList(collaboration.MyTasksQuery{Scope: collaboration.MyTaskScopeAssigned})),
		[]string{mtAssigned, mtInShared, mtBoth, mtCancelledID})
	// 任务类别不硬编码：completed=hide 才排除已完成/已取消/重复。
	sameSet("hide completed", ids(mustList(collaboration.MyTasksQuery{Filter: collaboration.TaskFilter{HideCompleted: true}})),
		[]string{mtAssigned, mtCreated, mtBoth})
	sameSet("created", ids(mustList(collaboration.MyTasksQuery{Scope: collaboration.MyTaskScopeCreated})),
		[]string{mtCreated, mtBoth})
	sameSet("space", ids(mustList(collaboration.MyTasksQuery{SpaceID: mtShared})), []string{mtInShared})

	// 3. 游标翻页：两页拼起来等于全量且不重复（全量 5 条，已取消任务不再被排除）。
	first := mustList(collaboration.MyTasksQuery{Limit: 3})
	if len(first.Items) != 3 || first.NextCursor == "" {
		t.Fatalf("first page = %v cursor %q", ids(first), first.NextCursor)
	}
	second := mustList(collaboration.MyTasksQuery{Limit: 3, Cursor: first.NextCursor})
	if len(second.Items) != 2 || second.NextCursor != "" {
		t.Fatalf("second page = %v cursor %q", ids(second), second.NextCursor)
	}
	sameSet("pages", append(ids(first), ids(second)...), ids(all))

	// 4. 非法输入。
	for name, query := range map[string]collaboration.MyTasksQuery{
		"scope": {Scope: "everything", Limit: 10},
		"space": {SpaceID: "not-a-uuid", Limit: 10},
	} {
		if _, err := list(mtUserD, policy.OrganizationRoleMember, query); !errors.Is(err, collaboration.ErrInvalidTask) {
			t.Fatalf("%s error = %v", name, err)
		}
	}
	// 游标与排序绑定，非法游标返回 400 INVALID_CURSOR（RuleError）。
	var rule *collaboration.RuleError
	if _, err := list(mtUserD, policy.OrganizationRoleMember, collaboration.MyTasksQuery{Cursor: "!!", Limit: 10}); !errors.As(err, &rule) || rule.Code != "INVALID_CURSOR" {
		t.Fatalf("cursor error = %v", err)
	}
	if _, err := list(mtUserD, policy.OrganizationRoleMember, collaboration.MyTasksQuery{Limit: collaboration.MaxMyTasksPageSize + 1}); !errors.Is(err, collaboration.ErrInvalidPageSize) {
		t.Fatalf("limit error = %v", err)
	}

	// 5. 访客看不到未加入的 SHARED 空间里指派给自己的任务。
	guest, err := list(mtGuest, policy.OrganizationRoleGuest, collaboration.MyTasksQuery{})
	if err != nil || len(guest.Items) != 0 {
		t.Fatalf("guest = %v, %v", ids(guest), err)
	}

	// 6. 收件箱的任务分支同样按协作策略：SHARED 空间里的分配通知对非成员可见。
	var inbox conversations.InboxPage
	h.run(mtUserD, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		var err error
		inbox, err = (conversations.Service{}).ListInbox(ctx, tx, homeOrgA, actor,
			conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox}, Limit: 50})
		return err
	})
	if len(inbox.Groups) != 1 || inbox.Groups[0].ID != mtInShared {
		t.Fatalf("shared inbox = %+v", inbox.Groups)
	}

	// 7. 显式被撤销的 SHARED 空间成员既看不到任务，也看不到对应通知。
	if _, err := superuser.Exec(ctx, `INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'MEMBER', 'REVOKED')`, homeOrgA, mtShared, mtUserD); err != nil {
		t.Fatal(err)
	}
	sameSet("revoked", ids(mustList(collaboration.MyTasksQuery{})), []string{mtAssigned, mtCreated, mtBoth, mtCancelledID})
	h.run(mtUserD, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		var err error
		inbox, err = (conversations.Service{}).ListInbox(ctx, tx, homeOrgA, actor,
			conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox}, Limit: 50})
		return err
	})
	if len(inbox.Groups) != 0 || inbox.Counts.Inbox != 0 {
		t.Fatalf("revoked inbox = %+v %+v", inbox.Groups, inbox.Counts)
	}
}

func seedMyTasks(t *testing.T, ctx context.Context, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	org := homeOrgA
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, 'dave@example.test'), ($2::uuid, 'erin@example.test'), ($3::uuid, 'gus@example.test')`, []any{mtUserD, mtUserE, mtGuest}},
		{`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER'), ($1::uuid, $3::uuid, 'MEMBER'), ($1::uuid, $4::uuid, 'GUEST')`, []any{org, mtUserD, mtUserE, mtGuest}},
		{`INSERT INTO openfox.spaces (organization_id, id, name, visibility, prefix) VALUES ($1::uuid, $2::uuid, 'Shared space', 'SHARED', 'shr'), ($1::uuid, $3::uuid, 'Hidden space', 'PRIVATE', 'hid')`, []any{org, mtShared, mtPrivate}},
		{`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status) VALUES ($1::uuid, $2::uuid, $3::uuid, 'MEMBER', 'ACTIVE'), ($1::uuid, $4::uuid, $5::uuid, 'MEMBER', 'ACTIVE')`, []any{org, homeSpaceA, mtUserD, mtShared, mtUserE}},
		{`INSERT INTO openfox.task_statuses (organization_id, id, name, category, position) VALUES ($1::uuid, $2::uuid, 'MT todo', 'TODO', 50), ($1::uuid, $3::uuid, 'MT done', 'DONE', 51), ($1::uuid, $4::uuid, 'MT cancelled', 'CANCELLED', 52)`, []any{org, mtTodo, mtDone, mtCancelled}},
		{`INSERT INTO openfox.task_types (organization_id, space_id, id, name, is_default) VALUES ($1::uuid, $2::uuid, $3::uuid, 'MT type', false), ($1::uuid, $4::uuid, $5::uuid, 'MT shared type', false), ($1::uuid, $6::uuid, $7::uuid, 'MT private type', false)`, []any{org, homeSpaceA, mtTypeA, mtShared, mtTypeShared, mtPrivate, mtTypePrivate}},
		{`INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default)
		  SELECT $1::uuid, v.space_id, v.type_id, s.status_id, s.position, s.is_default
		    FROM (VALUES ($2::uuid, $3::uuid), ($4::uuid, $5::uuid), ($6::uuid, $7::uuid)) AS v(space_id, type_id)
		   CROSS JOIN (VALUES ($8::uuid, 0, true), ($9::uuid, 1, false), ($10::uuid, 2, false)) AS s(status_id, position, is_default)`,
			[]any{org, homeSpaceA, mtTypeA, mtShared, mtTypeShared, mtPrivate, mtTypePrivate, mtTodo, mtDone, mtCancelled}},
		{`INSERT INTO openfox.tasks (organization_id, id, space_id, title, status, sequence, type_id, status_id, description, acceptance, priority, assignee_id, executor_agent_id, updated_at)
		  VALUES ($1::uuid, $2::uuid, $3::uuid, 'Assigned to D', 'TODO', 9201, $4::uuid, $5::uuid, '', '', 'NORMAL', $6::uuid, '00000000-0000-0000-0000-0000000000a2'::uuid, now() - interval '3 hours'),
		         ($1::uuid, $7::uuid, $3::uuid, 'Cancelled for D', 'CANCELED', 9202, $4::uuid, $8::uuid, '', '', 'NORMAL', $6::uuid, NULL, now() - interval '4 hours')`,
			[]any{org, mtAssigned, homeSpaceA, mtTypeA, mtTodo, mtUserD, mtCancelledID, mtCancelled}},
		{`INSERT INTO openfox.tasks (organization_id, id, space_id, title, status, sequence, type_id, status_id, description, acceptance, priority, assignee_id, updated_at)
		  VALUES ($1::uuid, $2::uuid, $3::uuid, 'Shared for D', 'DONE', 1, $4::uuid, $5::uuid, '', '', 'NORMAL', $6::uuid, now() - interval '5 hours'),
		         ($1::uuid, $7::uuid, $3::uuid, 'Shared for guest', 'TODO', 2, $4::uuid, $8::uuid, '', '', 'NORMAL', $9::uuid, now() - interval '5 hours'),
		         ($1::uuid, $10::uuid, $11::uuid, 'Private for D', 'TODO', 1, $12::uuid, $8::uuid, '', '', 'NORMAL', $6::uuid, now() - interval '1 hour')`,
			[]any{org, mtInShared, mtShared, mtTypeShared, mtDone, mtUserD, mtGuestTask, mtTodo, mtGuest, mtInPrivate, mtPrivate, mtTypePrivate}},
		// E 创建了指派给 D 的任务：这条活动不属于 D，D 的「我创建的」里不应出现。
		{`INSERT INTO openfox.task_activities (organization_id, space_id, task_id, actor_id, kind, source_id)
		  VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'task.created', 'my-tasks:seed:e41')`, []any{org, homeSpaceA, mtAssigned, mtUserE}},
		{`INSERT INTO openfox.notifications (organization_id, recipient_user_id, kind, task_id, dedupe_key, safe_summary)
		  VALUES ($1::uuid, $2::uuid, 'TASK_ASSIGNED', $3::uuid, 'my-tasks:shared', 'A task was assigned to you.')`, []any{org, mtUserD, mtInShared}},
	}
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement.query, statement.args...); err != nil {
			conn.Close(ctx)
			t.Fatal(err)
		}
	}
	return conn
}
