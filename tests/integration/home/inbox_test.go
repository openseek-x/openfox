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
	"openfox/internal/tenancy"
)

// 依赖 tests/integration/db/fixture.sql 中的组织 A、成员 alice 与私有空间 aa。
const (
	homeOrgA    = "00000000-0000-0000-0000-0000000000a1"
	homeOwnerA  = "00000000-0000-0000-0000-000000000001"
	homeSpaceA  = "00000000-0000-0000-0000-0000000000aa"
	homeUserC   = "00000000-0000-0000-0000-000000000c01"
	homeStatus  = "00000000-0000-0000-0000-000000000c02"
	homeType    = "00000000-0000-0000-0000-000000000c03"
	homeTaskOne = "00000000-0000-0000-0000-000000000c04"
	homeTaskTwo = "00000000-0000-0000-0000-000000000c05"
	homeTaskSix = "00000000-0000-0000-0000-000000000c06"
)

type harness struct {
	t    *testing.T
	ctx  context.Context
	pool *postgres.Pool
}

func (h harness) run(userID string, role policy.OrganizationRole, fn func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error) {
	h.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: homeOrgA, ActorID: userID, AuthorizationVersion: 1}
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil })
	actor := conversations.Actor{UserID: userID, Role: role, AuthorizationVersion: 1}
	if err := h.pool.RunForOrganization(h.ctx, homeOrgA, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, tx, actor)
	}); err != nil {
		h.t.Fatal(err)
	}
}

func (h harness) inbox(query conversations.InboxQuery) conversations.InboxPage {
	h.t.Helper()
	if query.Limit == 0 {
		query.Limit = 50
	}
	var page conversations.InboxPage
	h.run(homeUserC, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		var err error
		page, err = (conversations.Service{}).ListInbox(ctx, tx, homeOrgA, actor, query)
		return err
	})
	return page
}

func (h harness) action(action conversations.InboxAction, section conversations.InboxSection, ids []string, until *time.Time) int64 {
	h.t.Helper()
	var affected int64
	h.run(homeUserC, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		var err error
		affected, err = (conversations.Service{}).ApplyInboxAction(ctx, tx, homeOrgA, actor, action, section, ids, until)
		return err
	})
	return affected
}

func (h harness) bulk(operation conversations.InboxBulkOperation, section conversations.InboxSection) int64 {
	return h.bulkScope(operation, conversations.InboxScope{Section: section})
}

func (h harness) bulkScope(operation conversations.InboxBulkOperation, scope conversations.InboxScope) int64 {
	h.t.Helper()
	var affected int64
	h.run(homeUserC, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		var err error
		affected, err = (conversations.Service{}).ApplyInboxBulk(ctx, tx, homeOrgA, actor, operation, scope)
		return err
	})
	return affected
}

func groupIDs(page conversations.InboxPage) []string {
	ids := make([]string, 0, len(page.Groups))
	for _, group := range page.Groups {
		ids = append(ids, group.ID)
	}
	return ids
}

func TestPostgresHomeInbox(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	superuser := seed(t, ctx, url)
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

	// 1. 负责人创建任务并分配给 C：写入 TASK_ASSIGNED，发起人由数据库从上下文记录。
	var created collaboration.Task
	h.run(homeOwnerA, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, _ conversations.Actor) error {
		var err error
		created, err = (collaboration.Service{}).CreateTask(ctx, tx, homeOrgA, homeSpaceA,
			collaboration.Actor{UserID: homeOwnerA, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1},
			collaboration.TaskInput{ID: homeTaskOne, TypeID: homeType, Title: "Inbox assigned task", AssigneeUserID: homeUserC})
		return err
	})
	var actorID string
	if err := superuser.QueryRow(ctx, `SELECT COALESCE(actor_user_id::text, '') FROM openfox.notifications
		WHERE organization_id = $1::uuid AND recipient_user_id = $2::uuid AND kind = 'TASK_ASSIGNED' AND task_id = $3::uuid`,
		homeOrgA, homeUserC, created.ID).Scan(&actorID); err != nil {
		t.Fatalf("assignment notification: %v", err)
	}
	if actorID != homeOwnerA {
		t.Fatalf("assignment actor = %q", actorID)
	}
	// 自己分配给自己不产生通知。
	h.run(homeOwnerA, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, _ conversations.Actor) error {
		_, err := (collaboration.Service{}).CreateTask(ctx, tx, homeOrgA, homeSpaceA,
			collaboration.Actor{UserID: homeOwnerA, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1},
			collaboration.TaskInput{ID: homeTaskSix, TypeID: homeType, Title: "Inbox self task", AssigneeUserID: homeOwnerA})
		return err
	})
	var selfCount int
	if err := superuser.QueryRow(ctx, `SELECT count(*) FROM openfox.notifications WHERE task_id = $1::uuid`, homeTaskSix).Scan(&selfCount); err != nil || selfCount != 0 {
		t.Fatalf("self assignment notifications = %d, %v", selfCount, err)
	}

	// This exercise explicitly enables both handoffs; new accounts default to mute.
	if _, err := superuser.Exec(ctx, `INSERT INTO openfox.user_inbox_preferences(user_id,key,level) VALUES ($1,'task_needs_human_input_participant','important'),($1,'task_review_requested_participant','normal'),($1,'task_failed_participant','important')`, homeUserC); err != nil {
		t.Fatal(err)
	}
	// 2. 补充同任务的 HANDOFF（同组计数 2）与另一任务的 HANDOFF_REVIEW（等你审阅，归入「其他」）。
	for _, row := range []struct{ kind, task, key, summary string }{
		{"HANDOFF", homeTaskOne, "home:reply", "A conversation is waiting for your reply."},
		{"HANDOFF_REVIEW", homeTaskTwo, "home:review", "A conversation is waiting for your review."},
	} {
		if _, err := superuser.Exec(ctx, `INSERT INTO openfox.notifications
			(organization_id, recipient_user_id, kind, task_id, dedupe_key, safe_summary, created_at)
			VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6, clock_timestamp() + interval '1 second')`,
			homeOrgA, homeUserC, row.kind, row.task, row.key, row.summary); err != nil {
			t.Fatal(err)
		}
	}

	page := h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox}})
	if len(page.Groups) != 2 || page.Counts.Inbox != 2 || page.Counts.InboxUnread != 2 || page.Counts.InboxImportantUnread != 1 || page.Counts.Later != 0 || page.Counts.Cleared != 0 {
		t.Fatalf("initial inbox = %+v", page)
	}
	var one conversations.InboxGroup
	for _, group := range page.Groups {
		if group.ID == homeTaskOne {
			one = group
		}
	}
	if one.Count != 2 || !one.Unread || one.Kind != "HANDOFF" || one.TaskTitle != "Inbox assigned task" || one.TaskStatusCategory != "TODO" || one.SpaceName != "A private space" {
		t.Fatalf("task group = %+v", one)
	}
	if other := h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox, Importance: conversations.InboxImportanceOther}}); len(other.Groups) != 1 || other.Groups[0].ID != homeTaskTwo {
		t.Fatalf("other importance = %+v", other.Groups)
	}
	if primary := h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox, Importance: conversations.InboxImportancePrimary}}); len(primary.Groups) != 1 || primary.Groups[0].ID != homeTaskOne {
		t.Fatalf("primary importance = %+v", primary.Groups)
	}
	if search := h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox, Query: "assigned"}}); len(search.Groups) != 1 || search.Groups[0].ID != homeTaskOne {
		t.Fatalf("search = %+v", search.Groups)
	}
	if search := h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox, Query: "100%_"}}); len(search.Groups) != 0 {
		t.Fatalf("escaped search = %+v", search.Groups)
	}

	// 2b. 空间筛选「是 / 不是」（多选），旧版单空间参数与新参数合并。
	missingSpace := "00000000-0000-0000-0000-0000000000ff"
	for _, tc := range []struct {
		name  string
		scope conversations.InboxScope
		want  int
	}{
		{"is own space", conversations.InboxScope{SpaceIDs: []string{homeSpaceA}, SpaceMode: conversations.InboxSpaceIs}, 2},
		{"is other space", conversations.InboxScope{SpaceIDs: []string{missingSpace}}, 0},
		{"is either space", conversations.InboxScope{SpaceIDs: []string{missingSpace, homeSpaceA}}, 2},
		{"is not own space", conversations.InboxScope{SpaceIDs: []string{homeSpaceA}, SpaceMode: conversations.InboxSpaceIsNot}, 0},
		{"is not other space", conversations.InboxScope{SpaceIDs: []string{missingSpace}, SpaceMode: conversations.InboxSpaceIsNot}, 2},
		{"legacy space id", conversations.InboxScope{SpaceID: homeSpaceA}, 2},
	} {
		tc.scope.Section = conversations.InboxSectionInbox
		if got := h.inbox(conversations.InboxQuery{InboxScope: tc.scope}); len(got.Groups) != tc.want {
			t.Fatalf("%s: groups = %d, want %d", tc.name, len(got.Groups), tc.want)
		}
	}
	h.run(homeUserC, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		for _, scope := range []conversations.InboxScope{
			{Section: conversations.InboxSectionInbox, SpaceMode: "maybe"},
			{Section: conversations.InboxSectionInbox, SpaceIDs: []string{"not-a-uuid"}},
		} {
			if _, err := (conversations.Service{}).ListInbox(ctx, tx, homeOrgA, actor, conversations.InboxQuery{InboxScope: scope, Limit: 10}); !errors.Is(err, conversations.ErrInvalidRequest) {
				t.Fatalf("invalid scope %+v error = %v", scope, err)
			}
			if _, err := (conversations.Service{}).ApplyInboxBulk(ctx, tx, homeOrgA, actor, conversations.InboxBulkMarkRead, scope); !errors.Is(err, conversations.ErrInvalidRequest) {
				t.Fatalf("invalid bulk scope %+v error = %v", scope, err)
			}
		}
		return nil
	})
	// 批量操作遵守空间筛选与搜索词：排除所在空间时不动任何通知；按搜索词只标记命中的组。
	if affected := h.bulkScope(conversations.InboxBulkMarkRead, conversations.InboxScope{Section: conversations.InboxSectionInbox,
		SpaceIDs: []string{homeSpaceA}, SpaceMode: conversations.InboxSpaceIsNot}); affected != 0 {
		t.Fatalf("bulk is_not affected = %d", affected)
	}
	if affected := h.bulkScope(conversations.InboxBulkMarkRead, conversations.InboxScope{Section: conversations.InboxSectionInbox, Query: "assigned"}); affected != 2 {
		t.Fatalf("bulk search affected = %d", affected)
	}
	page = h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox}})
	for _, group := range page.Groups {
		if group.Unread != (group.ID == homeTaskTwo) {
			t.Fatalf("after bulk search group %s unread = %v", group.ID, group.Unread)
		}
	}
	if page.Counts.InboxUnread != 1 {
		t.Fatalf("after bulk search counts = %+v", page.Counts)
	}

	// 3. 游标分页：每页 1 组，两页覆盖全部且不重复。
	first := h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox}, Limit: 1})
	if len(first.Groups) != 1 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	second := h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox}, Limit: 1, Cursor: first.NextCursor})
	if len(second.Groups) != 1 || second.NextCursor != "" || second.Groups[0].ID == first.Groups[0].ID {
		t.Fatalf("second page = %+v", second)
	}

	// 4. 按组动作。
	if affected := h.action(conversations.InboxActionRead, conversations.InboxSectionInbox, []string{homeTaskOne}, nil); affected != 2 {
		t.Fatalf("read affected = %d", affected)
	}
	if affected := h.action(conversations.InboxActionRead, conversations.InboxSectionInbox, []string{homeTaskOne}, nil); affected != 2 {
		t.Fatalf("repeat read affected = %d", affected)
	}
	until := time.Now().Add(time.Hour)
	if affected := h.action(conversations.InboxActionSnooze, conversations.InboxSectionInbox, []string{homeTaskTwo}, &until); affected != 1 {
		t.Fatalf("snooze affected = %d", affected)
	}
	page = h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionLater}})
	if len(page.Groups) != 1 || page.Groups[0].ID != homeTaskTwo || page.Groups[0].SnoozedUntil == nil || page.Counts.Inbox != 1 || page.Counts.InboxUnread != 0 || page.Counts.Later != 1 {
		t.Fatalf("later = %+v", page)
	}
	h.run(homeUserC, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		past := time.Now().Add(-time.Minute)
		far := time.Now().Add(400 * 24 * time.Hour)
		for _, value := range []*time.Time{nil, &past, &far} {
			if _, err := (conversations.Service{}).ApplyInboxAction(ctx, tx, homeOrgA, actor, conversations.InboxActionSnooze, conversations.InboxSectionInbox, []string{homeTaskOne}, value); !errors.Is(err, conversations.ErrInvalidRequest) {
				t.Fatalf("invalid snooze %v error = %v", value, err)
			}
		}
		for _, ids := range [][]string{nil, {"not-a-uuid"}} {
			if _, err := (conversations.Service{}).ApplyInboxAction(ctx, tx, homeOrgA, actor, conversations.InboxActionRead, conversations.InboxSectionInbox, ids, nil); !errors.Is(err, conversations.ErrInvalidRequest) {
				t.Fatalf("invalid ids %v error = %v", ids, err)
			}
		}
		if _, err := (conversations.Service{}).ApplyInboxAction(ctx, tx, homeOrgA, actor, conversations.InboxActionUnclear, conversations.InboxSectionInbox, []string{homeTaskOne}, nil); !errors.Is(err, conversations.ErrInvalidRequest) {
			t.Fatalf("unclear from inbox error = %v", err)
		}
		return nil
	})
	if affected := h.action(conversations.InboxActionUnsnooze, conversations.InboxSectionLater, []string{homeTaskTwo}, nil); affected != 1 {
		t.Fatalf("unsnooze affected = %d", affected)
	}
	if affected := h.action(conversations.InboxActionUnread, conversations.InboxSectionInbox, []string{homeTaskTwo}, nil); affected != 1 {
		t.Fatalf("unread affected = %d", affected)
	}
	if affected := h.action(conversations.InboxActionClear, conversations.InboxSectionInbox, []string{homeTaskOne}, nil); affected != 2 {
		t.Fatalf("clear affected = %d", affected)
	}
	page = h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionCleared}})
	if len(page.Groups) != 1 || page.Groups[0].ClearedAt == nil || page.Counts.Cleared != 1 || page.Counts.Inbox != 1 {
		t.Fatalf("cleared = %+v", page)
	}
	if affected := h.action(conversations.InboxActionUnclear, conversations.InboxSectionCleared, []string{homeTaskOne}, nil); affected != 2 {
		t.Fatalf("unclear affected = %d", affected)
	}

	// 5. 分区级批量：归档全部已读只清走整组已读的 T1，未读的 T2 留下。
	if affected := h.bulk(conversations.InboxBulkClearRead, conversations.InboxSectionInbox); affected != 2 {
		t.Fatalf("clear read affected = %d", affected)
	}
	if ids := groupIDs(h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox}})); len(ids) != 1 || ids[0] != homeTaskTwo {
		t.Fatalf("inbox after clear read = %v", ids)
	}
	if affected := h.bulk(conversations.InboxBulkMarkRead, conversations.InboxSectionInbox); affected != 1 {
		t.Fatalf("mark read affected = %d", affected)
	}
	if affected := h.bulk(conversations.InboxBulkClearAll, conversations.InboxSectionInbox); affected != 1 {
		t.Fatalf("clear all affected = %d", affected)
	}
	page = h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox}})
	if len(page.Groups) != 0 || page.Counts.Cleared != 2 {
		t.Fatalf("inbox after clear all = %+v", page)
	}

	// 6. 已清理的任务来了新通知：新通知单独成组回到收件箱。
	if _, err := superuser.Exec(ctx, `INSERT INTO openfox.notifications
		(organization_id, recipient_user_id, kind, task_id, dedupe_key, safe_summary, created_at)
		VALUES ($1::uuid, $2::uuid, 'RUN_FAILED', $3::uuid, 'home:failed', 'Our servers are currently overloaded', clock_timestamp() + interval '2 seconds')`,
		homeOrgA, homeUserC, homeTaskOne); err != nil {
		t.Fatal(err)
	}
	page = h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox}})
	if len(page.Groups) != 1 || page.Groups[0].Kind != "RUN_FAILED" || page.Groups[0].Count != 1 || page.Groups[0].SafeSummary != "Our servers are currently overloaded" || page.Counts.InboxUnread != 1 {
		t.Fatalf("resurfaced = %+v", page)
	}

	// 7. 他人看不到也改不动 C 的通知。
	h.run(homeOwnerA, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		owner, err := (conversations.Service{}).ListInbox(ctx, tx, homeOrgA, actor, conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox}, Limit: 50})
		if err != nil {
			return err
		}
		for _, group := range owner.Groups {
			if group.ID == homeTaskOne || group.ID == homeTaskTwo {
				t.Fatalf("owner sees recipient group %+v", group)
			}
		}
		affected, err := (conversations.Service{}).ApplyInboxAction(ctx, tx, homeOrgA, actor, conversations.InboxActionClear, conversations.InboxSectionInbox, []string{homeTaskOne}, nil)
		if err != nil || affected != 0 {
			t.Fatalf("cross-recipient clear = %d, %v", affected, err)
		}
		return nil
	})

	// 8. 失去私有空间成员资格后，通知不再展示，计数同步。
	if _, err := superuser.Exec(ctx, `UPDATE openfox.space_members SET status = 'REVOKED' WHERE organization_id = $1::uuid AND space_id = $2::uuid AND user_id = $3::uuid`, homeOrgA, homeSpaceA, homeUserC); err != nil {
		t.Fatal(err)
	}
	page = h.inbox(conversations.InboxQuery{InboxScope: conversations.InboxScope{Section: conversations.InboxSectionInbox}})
	if len(page.Groups) != 0 || page.Counts.Inbox != 0 || page.Counts.Cleared != 0 {
		t.Fatalf("inbox after space removal = %+v", page)
	}
}

func seed(t *testing.T, ctx context.Context, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, 'carol@example.test')`, []any{homeUserC}},
		{`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER')`, []any{homeOrgA, homeUserC}},
		{`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status) VALUES ($1::uuid, $2::uuid, $3::uuid, 'MEMBER', 'ACTIVE')`, []any{homeOrgA, homeSpaceA, homeUserC}},
		{`INSERT INTO openfox.task_statuses (organization_id, id, name, category, position) VALUES ($1::uuid, $2::uuid, 'Home TODO', 'TODO', 40)`, []any{homeOrgA, homeStatus}},
		{`INSERT INTO openfox.task_types (organization_id, space_id, id, name, is_default) VALUES ($1::uuid, $2::uuid, $3::uuid, 'Home type', false)`, []any{homeOrgA, homeSpaceA, homeType}},
		{`INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 0, true)`, []any{homeOrgA, homeSpaceA, homeType, homeStatus}},
		{`INSERT INTO openfox.tasks (organization_id, id, space_id, title, status, sequence, type_id, status_id, description, acceptance, priority) VALUES ($1::uuid, $2::uuid, $3::uuid, 'Inbox mention task', 'TODO', 9102, $4::uuid, $5::uuid, '', '', 'NORMAL')`, []any{homeOrgA, homeTaskTwo, homeSpaceA, homeType, homeStatus}},
	}
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement.query, statement.args...); err != nil {
			conn.Close(ctx)
			t.Fatal(err)
		}
	}
	return conn
}
