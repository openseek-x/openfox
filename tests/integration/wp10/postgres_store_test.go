package wp10

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

const (
	wp10OrgA         = "00000000-0000-0000-0000-0000000000a1"
	wp10OrgB         = "00000000-0000-0000-0000-0000000000b1"
	wp10OwnerA       = "00000000-0000-0000-0000-000000000001"
	wp10OtherA       = "00000000-0000-0000-0000-000000000003"
	wp10OwnerB       = "00000000-0000-0000-0000-000000000002"
	wp10SpaceA       = "00000000-0000-0000-0000-0000000000aa"
	wp10AgentA       = "00000000-0000-0000-0000-0000000000a8"
	wp10TypeA        = "00000000-0000-0000-0000-000000000011"
	wp10TodoStatus   = "00000000-0000-0000-0000-000000000012"
	wp10DoneStatus   = "00000000-0000-0000-0000-000000000013"
	wp10TaskA        = "00000000-0000-0000-0000-000000000014"
	wp10Conversation = "00000000-0000-0000-0000-000000000015"
	wp10Run          = "00000000-0000-0000-0000-000000000016"
)

func TestPostgresConversationMessageCommentAndHandoff(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	seedDatabase(t, ctx, url)
	p, err := postgres.New(ctx, postgres.Config{
		URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	owner := conversations.Actor{UserID: wp10OwnerA, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	selection := tenancy.OrganizationSelection{OrganizationID: wp10OrgA, ActorID: wp10OwnerA, AuthorizationVersion: 1}
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil })
	var conversation conversations.Conversation
	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		conversation, err = (conversations.Service{}).Create(ctx, tx, wp10OrgA, owner, conversations.CreateInput{
			ID: wp10Conversation, SpaceID: wp10SpaceA, AgentID: wp10AgentA, TaskID: wp10TaskA,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if conversation.Lifecycle != conversations.LifecycleOpen || conversation.Visibility != conversations.VisibilityPrivate {
		t.Fatalf("created conversation = %+v", conversation)
	}

	otherSelection := tenancy.OrganizationSelection{OrganizationID: wp10OrgA, ActorID: wp10OtherA, AuthorizationVersion: 1}
	otherSelector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return otherSelection, nil })
	other := conversations.Actor{UserID: wp10OtherA, Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}
	var offerID string
	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		offerID, err = (conversations.Service{}).SendOffer(ctx, tx, wp10OrgA, wp10Conversation, owner, conversations.OfferInput{Kind: "INVITE", Recipient: wp10OtherA})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, otherSelector, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (conversations.Service{}).DecideOffer(ctx, tx, wp10OrgA, offerID, other, true, "wp10-accept")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		conversation, err = (conversations.Service{}).Get(ctx, tx, wp10OrgA, wp10Conversation, owner)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var message conversations.Message
	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		message, err = (conversations.Service{}).PostMessage(ctx, tx, wp10OrgA, wp10Conversation, owner, conversations.MessageInput{
			ID: wp10Run, Body: "Please build this and notify me", ClientMessageID: "client-message-1",
			Mentions: []conversations.Mention{{UserID: wp10OtherA}, {AgentID: wp10AgentA}},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if message.Seq != 1 || len(message.Mentions) != 2 {
		t.Fatalf("message = %+v", message)
	}
	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		conversation, err = (conversations.Service{}).Get(ctx, tx, wp10OrgA, wp10Conversation, owner)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		page, err := (conversations.Service{}).ListMessages(ctx, tx, wp10OrgA, wp10Conversation, owner, 10, 0)
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || page.Items[0].Seq != message.Seq || len(page.Items[0].Mentions) != 2 {
			t.Fatalf("message page = %+v", page)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		duplicate, err := (conversations.Service{}).PostMessage(ctx, tx, wp10OrgA, wp10Conversation, owner, conversations.MessageInput{
			ID: "00000000-0000-0000-0000-000000000017", Body: message.Body, ClientMessageID: "client-message-1",
			Mentions: []conversations.Mention{{UserID: wp10OtherA}, {AgentID: wp10AgentA}},
		})
		if err != nil {
			return err
		}
		if duplicate.ID != message.ID {
			t.Fatalf("idempotent message = %+v", duplicate)
		}
		_, err = (conversations.Service{}).PostMessage(ctx, tx, wp10OrgA, wp10Conversation, owner, conversations.MessageInput{
			ID: "00000000-0000-0000-0000-000000000018", Body: "different", ClientMessageID: "client-message-1",
			Mentions: []conversations.Mention{{AgentID: wp10AgentA}},
		})
		if !errors.Is(err, conversations.ErrDuplicateMessage) {
			t.Fatalf("duplicate message error = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		comment, err := (conversations.Service{}).PostComment(ctx, tx, wp10OrgA, wp10SpaceA, wp10TaskA, owner, conversations.CommentInput{
			ID: "00000000-0000-0000-0000-000000000019", Body: "Review this result", ClientCommentID: "comment-1",
		})
		if err != nil {
			return err
		}
		if comment.TaskID != wp10TaskA {
			t.Fatalf("comment = %+v", comment)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		updated, err := (conversations.Service{}).SetHandoff(ctx, tx, wp10OrgA, wp10Conversation, owner, conversations.HandoffWaitingReply, conversation.Version)
		if err != nil {
			return err
		}
		conversation = updated
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, otherSelector, func(ctx context.Context, tx pgx.Tx) error {
		items, err := (conversations.Service{}).ListNotifications(ctx, tx, wp10OrgA, other, 10)
		if err != nil {
			return err
		}
		if len(items) != 2 {
			t.Fatalf("notifications = %+v", items)
		}
		return (conversations.Service{}).MarkNotificationRead(ctx, tx, wp10OrgA, items[0].ID, other)
	}); err != nil {
		t.Fatal(err)
	}

	if err := insertActiveRun(ctx, url); err != nil {
		t.Fatal(err)
	}
	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (conversations.Service{}).Complete(ctx, tx, wp10OrgA, wp10Conversation, owner, conversation.Version, false)
		if !errors.Is(err, conversations.ErrRunActive) {
			t.Fatalf("active run completion error = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	markRunComplete(t, ctx, url)

	if err := p.RunForOrganization(ctx, wp10OrgA, tenancy.SourceUserRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		updated, err := (conversations.Service{}).Complete(ctx, tx, wp10OrgA, wp10Conversation, owner, conversation.Version, true)
		if err != nil {
			return err
		}
		if updated.Lifecycle != conversations.LifecycleCompleted {
			t.Fatalf("completed conversation = %+v", updated)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	orgBSelection := tenancy.OrganizationSelection{OrganizationID: wp10OrgB, ActorID: wp10OwnerB, AuthorizationVersion: 1}
	orgBSelector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return orgBSelection, nil })
	orgBOwner := conversations.Actor{UserID: wp10OwnerB, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	if err := p.RunForOrganization(ctx, wp10OrgB, tenancy.SourceUserRequest, orgBSelector, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (conversations.Service{}).Get(ctx, tx, wp10OrgB, wp10Conversation, orgBOwner)
		if !errors.Is(err, conversations.ErrNotFound) {
			t.Fatalf("cross-tenant conversation error = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func seedDatabase(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, 'other-a@example.test') ON CONFLICT DO NOTHING`, []any{wp10OtherA}},
		{`INSERT INTO openfox.user_inbox_preferences(user_id,key,level) VALUES ($1::uuid,'task_needs_human_input_participant','important') ON CONFLICT (user_id,key) DO UPDATE SET level=EXCLUDED.level`, []any{wp10OtherA}},
		{`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER') ON CONFLICT DO NOTHING`, []any{wp10OrgA, wp10OtherA}},
		{`INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status) VALUES ($1::uuid, $2::uuid, $3::uuid, 'MEMBER', 'ACTIVE') ON CONFLICT DO NOTHING`, []any{wp10OrgA, wp10SpaceA, wp10OtherA}},
		{`INSERT INTO openfox.task_statuses (organization_id, id, name, category, position) VALUES ($1::uuid, $2::uuid, 'WP10 TODO', 'TODO', 20), ($1::uuid, $3::uuid, 'WP10 DONE', 'DONE', 21)`, []any{wp10OrgA, wp10TodoStatus, wp10DoneStatus}},
		{`INSERT INTO openfox.task_types (organization_id, space_id, id, name, is_default) VALUES ($1::uuid, $2::uuid, $3::uuid, 'WP10 type', false)`, []any{wp10OrgA, wp10SpaceA, wp10TypeA}},
		{`INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 0, true), ($1::uuid, $2::uuid, $3::uuid, $5::uuid, 1, false)`, []any{wp10OrgA, wp10SpaceA, wp10TypeA, wp10TodoStatus, wp10DoneStatus}},
		{`INSERT INTO openfox.agents (organization_id, id, device_id, status, generation, max_slots, space_id, owner_user_id, name, kind, visibility, current_config_version) VALUES ($1::uuid, $2::uuid, '00000000-0000-0000-0000-000000000011', 'ACTIVE', 1, 1, $3::uuid, '00000000-0000-0000-0000-000000000001', 'WP10 agent', 'CODE', 'PRIVATE', 1)`, []any{wp10OrgA, wp10AgentA, wp10SpaceA}},
		{`INSERT INTO openfox.tasks (organization_id, id, space_id, title, status, sequence, type_id, status_id, description, acceptance, priority, executor_agent_id) VALUES ($1::uuid, $2::uuid, $3::uuid, 'WP10 task', 'TODO', 9002, $4::uuid, $5::uuid, '', '', 'NORMAL', $6::uuid)`, []any{wp10OrgA, wp10TaskA, wp10SpaceA, wp10TypeA, wp10TodoStatus, wp10AgentA}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func insertActiveRun(ctx context.Context, url string) error {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	var grantID string
	if err := conn.QueryRow(ctx, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = '00000000-0000-0000-0000-000000000011'`, wp10OrgA).Scan(&grantID); err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `
		INSERT INTO openfox.runs (
			organization_id, id, space_id, conversation_id, task_id, agent_id, computer_id,
			grant_id, config_version, grant_version, actor_id, state
		) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid,
		          '00000000-0000-0000-0000-000000000011', $7::uuid, 1, 1, $8::uuid, 'RUNNING')`,
		wp10OrgA, wp10Run, wp10SpaceA, wp10Conversation, wp10TaskA, wp10AgentA, grantID, wp10OwnerA)
	return err
}

func markRunComplete(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `UPDATE openfox.runs SET state = 'SUCCEEDED', finished_at = clock_timestamp(), version = version + 1 WHERE organization_id = $1::uuid AND id = $2::uuid`, wp10OrgA, wp10Run); err != nil {
		t.Fatal(err)
	}
}
