package stage8d

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/api"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/identity"
)

func (h *harness) testCommitTypeSnapshots(t *testing.T) {
	h.setCapabilities(fullCapabilities)
	h.setAgentConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")
	var taskID, typeID string
	h.queryRow(`WITH typ AS (INSERT INTO openfox.task_types(organization_id,space_id,name,commit_type) VALUES ($1,$2,'Commit snapshot','Fix-42') RETURNING id),
 st AS (INSERT INTO openfox.task_statuses(organization_id,name,category,position) VALUES ($1,'Commit ready','TODO',981) RETURNING id)
 INSERT INTO openfox.tasks(organization_id,space_id,title,type_id,status_id,sequence)
 SELECT $1,$2,'Commit snapshot',typ.id,st.id,97413 FROM typ,st RETURNING id::text,type_id::text`, []any{orgA, spaceA}, &taskID, &typeID)
	conversation := newID(t)
	h.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) error {
		_, err := (conversations.Service{}).Create(ctx, tx, orgA, h.actor(orgA, alice), conversations.CreateInput{ID: conversation, SpaceID: spaceA, AgentID: agentA, TaskID: taskID, Visibility: "PRIVATE"})
		return err
	})
	run := h.sendAndDispatch(alice, conversation, "task run with prefix")
	lease := h.claim(run)
	initial := h.context(lease)
	if !strings.Contains(initial.Instructions, "`Fix-42: <description>`") || !strings.Contains(initial.Instructions, "does not request or authorize creating a commit") {
		t.Fatalf("missing conditional guidance: %q", initial.Instructions)
	}
	if got := h.commitContextHTTP(lease); got != initial.Instructions {
		t.Fatal("device HTTP instructions differ from snapshot")
	}
	h.exec(`UPDATE openfox.task_types SET commit_type='chore' WHERE organization_id=$1::uuid AND id=$2::uuid`, orgA, typeID)
	if again := h.context(lease); again.Instructions != initial.Instructions {
		t.Fatal("running prefix changed")
	}
	var replacementType string
	h.queryRow(`INSERT INTO openfox.task_types(organization_id,space_id,name,commit_type) VALUES ($1,$2,'Documentation','Docs') RETURNING id::text`, []any{orgA, spaceA}, &replacementType)
	h.exec(`UPDATE openfox.tasks SET type_id=$3::uuid WHERE organization_id=$1::uuid AND id=$2::uuid`, orgA, taskID, replacementType)
	if again := h.context(lease); again.Instructions != initial.Instructions {
		t.Fatal("task type change modified active snapshot")
	}
	h.finish(run, lease)
	switched := h.sendAndDispatch(alice, conversation, "task changed type")
	switchedLease := h.claim(switched)
	if !strings.Contains(h.context(switchedLease).Instructions, "`Docs: <description>`") {
		t.Fatal("next run did not use replacement task type")
	}
	h.exec(`UPDATE openfox.tasks SET type_id=$3::uuid WHERE organization_id=$1::uuid AND id=$2::uuid`, orgA, taskID, typeID)
	if !strings.Contains(h.context(switchedLease).Instructions, "`Docs: <description>`") {
		t.Fatal("switching back changed active snapshot")
	}
	h.finish(switched, switchedLease)
	for _, prefix := range []string{"chore", "feat"} {
		if prefix == "feat" {
			h.exec(`UPDATE openfox.task_types SET commit_type=NULL WHERE organization_id=$1::uuid AND id=$2::uuid`, orgA, typeID)
		}
		next := h.sendAndDispatch(alice, conversation, "next prefix")
		nextLease := h.claim(next)
		if got := h.context(nextLease); !strings.Contains(got.Instructions, "`"+prefix+": <description>`") {
			t.Fatalf("expected %s: %q", prefix, got.Instructions)
		}
		// Old JSON and NULL snapshots stay unchanged even when the current type is set.
		h.exec(`UPDATE openfox.runs SET git_settings_snapshot=git_settings_snapshot-'commit_type' WHERE organization_id=$1::uuid AND id=$2::uuid`, orgA, next)
		if strings.Contains(h.context(nextLease).Instructions, "## Git commit type") {
			t.Fatal("legacy JSON acquired prefix")
		}
		h.exec(`UPDATE openfox.runs SET git_settings_snapshot=NULL WHERE organization_id=$1::uuid AND id=$2::uuid`, orgA, next)
		if strings.Contains(h.context(nextLease).Instructions, "## Git commit type") {
			t.Fatal("legacy NULL acquired prefix")
		}
		h.finish(next, nextLease)
	}
	direct := h.newConversation(alice)
	plain := h.sendAndDispatch(alice, direct, "non-task conversation")
	plainLease := h.claim(plain)
	if strings.Contains(h.context(plainLease).Instructions, "## Git commit type") {
		t.Fatal("direct conversation acquired task prefix")
	}
	h.finish(plain, plainLease)
}

func (h *harness) commitContextHTTP(lease dispatch.Lease) string {
	h.t.Helper()
	keyID, token := newID(h.t), newID(h.t)
	h.exec(`WITH k AS (INSERT INTO openfox.device_keys(device_id,key_id,public_key)
 VALUES($1,$2,$3) RETURNING device_id,key_id,generation)
 INSERT INTO openfox.device_sessions(device_id,key_id,device_generation,key_generation,token_hash,expires_at)
 SELECT d.id,k.key_id,d.generation,k.generation,$4,clock_timestamp()+interval '1 minute' FROM k JOIN openfox.physical_devices d ON d.id=k.device_id`, device1, keyID, identity.HashSessionToken(keyID), identity.HashSessionToken(token))
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/device/organizations/%s/runs/%s/context?generation=%d", orgA, lease.RunID, lease.Generation), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	api.New(identity.Service{}, h.pool, false).ServeHTTP(out, req)
	if out.Code != http.StatusOK {
		h.t.Fatalf("device context HTTP %d: %s", out.Code, out.Body.String())
	}
	var response struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil {
		h.t.Fatal(err)
	}
	return response.Instructions
}
