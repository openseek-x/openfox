package home

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/attachments"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// retryOriginalAssistantRun uses the session and pending create_agent input from TestPostgresHomeAssistant.
func retryOriginalAssistantRun(t *testing.T, h harness, call func(string, string, string, bool, any) int, conv, space, normalAgent, firstID string) {
	t.Helper()
	newID := func() string {
		id, err := collaboration.NewID()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	messagesPath := "/conversations/" + conv + "/messages"
	attachID := newID()
	h.run(homeOwnerA, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		_, err := attachments.InsertConversation(ctx, tx, space, conv, actor.UserID, attachID, "requirements.txt",
			attachments.FileInfo{Key: "test/assistant-retry.txt", ContentType: "text/plain", SizeBytes: 6, SHA256: strings.Repeat("1", 64)})
		return err
	})
	var second assistantMessage
	if status := call(http.MethodPost, messagesPath, `{"body":"","client_message_id":"requirement","assistant_action":"create_task","attachment_ids":["`+attachID+`"]}`, true, &second); status != 201 {
		t.Fatalf("post second input with attachment = %d", status)
	}
	materialize := func() {
		t.Helper()
		selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return tenancy.OrganizationSelection{OrganizationID: homeOrgA, ActorID: dispatch.WorkerActorID, AuthorizationVersion: 1}, nil
		})
		err := h.pool.RunForOrganization(h.ctx, homeOrgA, tenancy.SourceOrganizationWorker, selector, func(ctx context.Context, tx pgx.Tx) error {
			_, _, err := (dispatch.Store{}).MaterializeInputs(ctx, tx, homeOrgA, 100)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	materialize()
	var runID string
	h.run(homeOwnerA, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		return tx.QueryRow(ctx, `SELECT run_id::text FROM openfox.conversation_inputs WHERE message_id=$1::uuid`, firstID).Scan(&runID)
	})
	path := "/conversations/" + conv + "/runs/" + runID + "/retry"
	if status := call(http.MethodPost, path, "", false, nil); status != http.StatusForbidden {
		t.Fatalf("retry without CSRF = %d", status)
	}
	if status := call(http.MethodPost, path, "", true, nil); status != http.StatusConflict {
		t.Fatalf("retry queued run = %d", status)
	}
	h.run(homeOwnerA, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		_, err := tx.Exec(ctx, `UPDATE openfox.runs SET state='FAILED', finished_at=clock_timestamp() WHERE id=$1::uuid`, runID)
		return err
	})
	ordinary := newID()
	h.run(homeOwnerA, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		_, err := (conversations.Service{}).Create(ctx, tx, homeOrgA, actor, conversations.CreateInput{ID: ordinary, AgentID: normalAgent, SpaceID: asTaskSpace, Visibility: "PRIVATE"})
		return err
	})
	if status := call(http.MethodPost, "/conversations/"+ordinary+"/runs/"+runID+"/retry", "", true, nil); status != http.StatusForbidden {
		t.Fatalf("retry in ordinary conversation = %d", status)
	}
	h.run(asIvy, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		if err := (conversations.Service{}).RetryAssistantRun(ctx, tx, homeOrgA, conv, runID, actor); !errors.Is(err, conversations.ErrNotFound) {
			t.Fatalf("retry another member's assistant = %v", err)
		}
		return nil
	})

	var wg sync.WaitGroup
	statuses := [2]int{}
	for i := range statuses {
		wg.Add(1)
		go func() { defer wg.Done(); statuses[i] = call(http.MethodPost, path, "", true, nil) }()
	}
	wg.Wait()
	if statuses != [2]int{204, 204} {
		t.Fatalf("concurrent retries = %v", statuses)
	}
	if status := call(http.MethodPost, messagesPath, `{"body":"separate input","client_message_id":"later"}`, true, nil); status != http.StatusCreated {
		t.Fatalf("post after retry = %d", status)
	}
	materialize()
	h.run(homeOwnerA, policy.OrganizationRoleOwner, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		var ids []string
		var attachmentsSeen, pending, msgCount int
		err := tx.QueryRow(ctx, `SELECT array_agg(i.message_id::text ORDER BY i.created_at, i.id),
			count(a.id) FROM openfox.runs r JOIN openfox.conversation_inputs i ON i.organization_id=r.organization_id AND i.run_id=r.id
			LEFT JOIN openfox.attachments a ON a.organization_id=i.organization_id AND a.message_id=i.message_id AND a.id=$2::uuid
			WHERE r.retry_of=$1::uuid`, runID, attachID).Scan(&ids, &attachmentsSeen)
		if err != nil {
			return err
		}
		if len(ids) != 2 || ids[0] != firstID || ids[1] != second.ID || attachmentsSeen != 1 {
			t.Fatalf("replayed messages or attachments = %v, %d", ids, attachmentsSeen)
		}
		err = tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM openfox.conversation_inputs WHERE conversation_id=$1::uuid AND status='PENDING' AND body='separate input'), (SELECT count(*) FROM openfox.messages WHERE conversation_id=$1::uuid AND author_kind='USER')`, conv).Scan(&pending, &msgCount)
		if err == nil && (pending != 1 || msgCount != 3) {
			t.Fatalf("new input or transcript changed on retry: pending %d, user messages %d", pending, msgCount)
		}
		return err
	})
}
