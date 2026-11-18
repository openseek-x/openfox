package stage12d

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/agents"
	"openfox/internal/conversations"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// Time admission and a task's planning gate are independent. Releasing one
// must not authorize a BACKLOG task to reserve a device or receive an offer.
func TestParityScheduleAdmissionPreservesTaskGate(t *testing.T) {
	closed := json.RawMessage(`{"enabled":true,"timezone":"UTC","days":[[],[],[],[],[],[],[]],"triggers":{"assignment":true,"comment_mention":true,"direct_chat":false}}`)
	for _, mode := range []string{"schedule_change", "trigger_now"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			setWorkingHours := func(raw json.RawMessage) {
				t.Helper()
				var grant string
				var version uint64
				h.queryRow(`SELECT c.grant_id::text,a.version FROM openfox.agents a
				 JOIN openfox.agent_config_versions c ON c.organization_id=a.organization_id
				 AND c.agent_id=a.id AND c.config_version=a.current_config_version WHERE a.id=$1`,
					[]any{gateAgent}, &grant, &version)
				v := h.authVersion()
				_, err := (agents.PostgresStore{Pool: h.pool}).CreateConfig(h.ctx,
					tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: v},
					agents.Actor{UserID: alice, Role: policy.OrganizationRoleOwner, AuthorizationVersion: v},
					spaceA, gateAgent, version, agents.ConfigInput{
						ComputerID: gateDevice, GrantID: grant, Runtime: "codex", Model: "default",
						MaxConcurrency: 4, QueueTimeoutSeconds: 3600, RunTimeoutSeconds: 600,
						DirectoryMode: "NONE", RunSettings: agents.RunSettings{WorkingHours: raw},
					})
				if err != nil {
					t.Fatal(err)
				}
			}
			setWorkingHours(closed)
			taskID := h.createAssignedTask(statusBacklog, gateAgent, "scheduled planning gate")
			result := h.runOnce()
			if result.RunsCreated != 1 || result.OffersCreated != 0 {
				t.Fatalf("initial pass = %+v, want one scheduled run and no offer", result)
			}
			runID, _ := h.taskRun(taskID)
			h.expectRow(`SELECT schedule_pending::text || ':' || wait_reason || ':' || (queue_expires_at IS NULL)::text
			 FROM openfox.runs WHERE id=$1`, []any{runID}, "true:TASK_GATE:true")
			if mode == "schedule_change" {
				setWorkingHours(json.RawMessage("null"))
				h.inDevice(gateDevice, func(ctx context.Context, tx pgx.Tx) error {
					_, err := h.store.AdmitScheduled(ctx, tx, orgA, 50)
					return err
				})
			} else {
				h.inUser(func(ctx context.Context, tx pgx.Tx) error {
					_, err := h.store.TriggerNow(ctx, tx, orgA, runID,
						conversations.Actor{UserID: alice, Role: policy.OrganizationRoleOwner, AuthorizationVersion: h.authVersion()},
						"parity-gated-trigger-"+runID)
					return err
				})
				setWorkingHours(json.RawMessage("null"))
			}
			h.expectRow(`SELECT schedule_pending::text || ':' || wait_reason || ':' || (queue_expires_at IS NOT NULL)::text
			 FROM openfox.runs WHERE id=$1`, []any{runID}, "false:TASK_GATE:true")
			if _, offered := offerFor(h.runOnce(), runID); offered {
				t.Fatal("time admission bypassed the task planning gate")
			}
			if n := h.activeReservations(runID); n != 0 {
				t.Fatalf("gated run holds %d reservations", n)
			}
			h.setTaskStatus(taskID, statusTodo)
			offer, ok := offerFor(h.runOnce(), runID)
			if !ok {
				t.Fatal("run was not offered after both gates opened")
			}
			h.finish(gateDevice, h.claim(gateDevice, offer.OfferID))
		})
	}
}
