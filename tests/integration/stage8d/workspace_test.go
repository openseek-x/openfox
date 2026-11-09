package stage8d

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/api"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
	"openfox/internal/identity"
	"openfox/internal/tenancy"
)

func (h *harness) testWorkspacePermissions(t *testing.T) {
	const caps = `{"conversation_workdir":true,"conversation_workdir_roots":["team","team/secret","registered-only"],"conversation_workdir_git_roots":["team","registered-only"]}`
	h.setCapabilities(caps)
	h.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH config AS (
			INSERT INTO openfox.agent_config_versions(organization_id,agent_id,config_version,computer_id,grant_id,runtime,model,max_concurrency,queue_timeout_seconds,run_timeout_seconds,directory_mode,directory_roots,created_by)
			SELECT organization_id,agent_id,1+(SELECT max(config_version) FROM openfox.agent_config_versions WHERE organization_id=$1 AND agent_id=$2),computer_id,grant_id,runtime,model,max_concurrency,queue_timeout_seconds,run_timeout_seconds,'EXPLICIT','["team","agent-only"]'::jsonb,created_by
			FROM openfox.agent_config_versions WHERE organization_id=$1 AND agent_id=$2 AND config_version=1 RETURNING config_version)
			UPDATE openfox.agents SET current_config_version=config.config_version FROM config WHERE organization_id=$1 AND id=$2`, orgA, agentA)
		return err
	})
	// These fixtures are intentionally in just one tenant and one space.
	var taskID string
	h.queryRow(`WITH typ AS (INSERT INTO openfox.task_types(organization_id,space_id,name) VALUES ($1,$2,'Workspace test') RETURNING id),
		st AS (INSERT INTO openfox.task_statuses(organization_id,name,category,position) VALUES ($1,'Workspace ready','TODO',980) RETURNING id)
		INSERT INTO openfox.tasks(organization_id,space_id,title,type_id,status_id,sequence)
		SELECT $1,$2,'Workdir',typ.id,st.id,97412 FROM typ,st RETURNING id::text`, []any{orgA, spaceA}, &taskID)
	create := func(user, task string, selection conversations.WorkspaceSelection) (string, error) {
		id := newID(t)
		actor := h.actor(orgA, user)
		err := h.pool.RunForOrganization(h.ctx, orgA, tenancy.SourceUserRequest, selector(tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: user, AuthorizationVersion: actor.AuthorizationVersion}), func(ctx context.Context, tx pgx.Tx) error {
			c, err := (conversations.Service{}).Create(ctx, tx, orgA, actor, conversations.CreateInput{ID: id, AgentID: agentA, SpaceID: spaceA, TaskID: task, WorkspaceSelection: &selection})
			if err == nil && (c.WorkspaceSelection == nil || *c.WorkspaceSelection != selection) {
				t.Fatalf("create returned %+v", c.WorkspaceSelection)
			}
			return err
		})
		return id, err
	}
	options := func(user, source string) conversations.WorkspaceOptions {
		var o conversations.WorkspaceOptions
		h.inUser(orgA, user, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			o, err = (conversations.Service{}).WorkspaceOptions(ctx, tx, orgA, h.actor(orgA, user), agentA, source)
			return err
		})
		return o
	}
	for _, bad := range []struct {
		sel conversations.WorkspaceSelection
		err error
	}{
		{conversations.WorkspaceSelection{Mode: "DIRECT", Directory: "/etc"}, conversations.ErrInvalidRequest},
		{conversations.WorkspaceSelection{Mode: "DIRECT", Directory: "team/../secret"}, conversations.ErrInvalidRequest},
		{conversations.WorkspaceSelection{Mode: "DIRECT", Directory: "registered-only"}, conversations.ErrForbidden},
		{conversations.WorkspaceSelection{Mode: "DIRECT", Directory: "agent-only"}, conversations.ErrForbidden},
		{conversations.WorkspaceSelection{Mode: "DIRECT", Directory: "team/secret/file"}, conversations.ErrForbidden},
		{conversations.WorkspaceSelection{Mode: "WORKTREE", Directory: "team", BaseRef: "a:b"}, conversations.ErrInvalidRequest},
	} {
		if _, err := create(alice, "", bad.sel); !errors.Is(err, bad.err) {
			t.Fatalf("%+v: %v", bad.sel, err)
		}
	}
	direct := conversations.WorkspaceSelection{Mode: "DIRECT", Directory: "team"}
	source, err := create(alice, taskID, direct)
	if err != nil {
		t.Fatal(err)
	}
	if o := options(alice, source); !o.Supported || !slices.Equal(o.Directories, []string{"team"}) || !slices.Equal(o.GitDirectories, []string{"team"}) || !slices.Equal(o.RecentDirectories, []string{"team"}) || o.CanReuse {
		t.Fatalf("options before first run %+v", o)
	}
	if o := options(carol, ""); len(o.RecentDirectories) > 0 || o.CanManageDirectory {
		t.Fatalf("member options %+v", o)
	}
	reuse := conversations.WorkspaceSelection{Mode: "REUSE", SourceConversationID: source}
	if _, err := create(alice, taskID, reuse); !errors.Is(err, conversations.ErrWorkspaceUnavailable) {
		t.Fatalf("never-started source: %v", err)
	}
	sourceRun := h.sendAndDispatch(alice, source, "prepare original directory")
	lease := h.claim(sourceRun)
	if c := h.context(lease); c.WorkspaceSelection == nil || c.WorkspaceSelection.Directory != direct.Directory || c.WorkspaceSelection.ComputerID != device1 {
		t.Fatalf("context workspace %+v", c.WorkspaceSelection)
	}
	h.verifyWorkspaceContextProtocol(lease)
	h.finish(sourceRun, lease)
	if o := options(alice, source); !o.CanReuse || !o.CanSetBaseline {
		t.Fatalf("source options %+v", o)
	}
	if _, err := create(carol, taskID, reuse); !errors.Is(err, conversations.ErrNotFound) {
		t.Fatalf("private source: %v", err)
	}
	if _, err := create(alice, "", reuse); !errors.Is(err, conversations.ErrForbidden) {
		t.Fatalf("wrong task: %v", err)
	}
	target, err := create(alice, taskID, reuse)
	if err != nil {
		t.Fatal(err)
	}
	baseline := conversations.WorkspaceSelection{Mode: "BASELINE", SourceConversationID: source, BaseRef: "feature/work"}
	if _, err := create(alice, taskID, baseline); err != nil {
		t.Fatal(err)
	}
	run := h.sendAndDispatch(alice, target, "reuse original")
	lease = h.claim(run)
	if c := h.context(lease); c.WorkspaceSelection == nil || c.WorkspaceSelection.SourceAgentID != agentA || c.WorkspaceSelection.SourceDirectory != "team" || c.WorkspaceSelection.Mode != "REUSE" {
		t.Fatalf("reuse context %+v", c.WorkspaceSelection)
	}
	// The same device downgrading or withdrawing approval after claim loses
	// access to this run's context, despite the original grant still being active.
	h.setCapabilities(`{"conversation_workdir":true,"conversation_workdir_roots":[]}`)
	h.inDevice(orgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.Context(ctx, tx, orgA, device1, lease.RunID, lease.Generation)
		if !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
			t.Fatalf("context after removal: %v", err)
		}
		return nil
	})
	h.setCapabilities(caps)
	h.finish(run, lease)
	// Share the intermediate conversation B, but keep the original A private.
	h.exec(`UPDATE openfox.conversations SET visibility='SHARED' WHERE organization_id=$1 AND id=$2`, orgA, target)
	h.exec(`INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id,access_level) VALUES ($1,$2,$3,'COMMENTER')`, orgA, target, carol)
	if !options(alice, target).CanReuse || options(carol, target).CanReuse {
		t.Fatal("nested reuse skipped source authorization")
	}
	if _, err := create(carol, taskID, conversations.WorkspaceSelection{Mode: "REUSE", SourceConversationID: target}); !errors.Is(err, conversations.ErrWorkspaceUnavailable) {
		t.Fatalf("reuse chain A -> B -> C bypassed private A: %v", err)
	}
	// A live grant on another computer with an identically named root cannot
	// transplant an old conversation's directory.
	pending := h.sendAndDispatch(alice, source, "before machine replacement")
	leased := h.claim(pending)
	var previousConfig int64
	h.queryRow(`SELECT current_config_version FROM openfox.agents WHERE organization_id=$1::uuid AND id=$2::uuid`, []any{orgA, agentA}, &previousConfig)
	t.Cleanup(func() {
		h.exec(`UPDATE openfox.agents SET device_id=$2::uuid,current_config_version=$4 WHERE organization_id=$1::uuid AND id=$3::uuid`, orgA, device1, agentA, previousConfig)
		h.setCapabilities(caps)
		h.finish(pending, leased)
		h.setAgentConfig("codex", "gpt-5.1", "FOLLOW_RUNTIME")
		h.setCapabilities(fullCapabilities)
	})
	const replacement = "00000000-0000-0000-0000-000000000022"
	h.exec(`UPDATE openfox.physical_devices SET capabilities=$1::jsonb WHERE id=$2`, caps, replacement)
	h.inUser(orgA, alice, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH config AS (
			INSERT INTO openfox.agent_config_versions(organization_id,agent_id,config_version,computer_id,grant_id,runtime,model,max_concurrency,queue_timeout_seconds,run_timeout_seconds,directory_mode,directory_roots,created_by)
			SELECT c.organization_id,c.agent_id,a.current_config_version+1,$3,g.id,c.runtime,c.model,c.max_concurrency,c.queue_timeout_seconds,c.run_timeout_seconds,c.directory_mode,c.directory_roots,c.created_by
			  FROM openfox.agent_config_versions c JOIN openfox.agents a ON a.organization_id=c.organization_id AND a.id=c.agent_id AND a.current_config_version=c.config_version
			  JOIN openfox.device_grants g ON g.organization_id=c.organization_id AND g.device_id=$3
			 WHERE a.organization_id=$1 AND a.id=$2 RETURNING config_version)
			UPDATE openfox.agents SET current_config_version=config.config_version,device_id=$3 FROM config WHERE organization_id=$1 AND id=$2`, orgA, agentA, replacement)
		return err
	})
	h.inDevice(orgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := h.store.Context(ctx, tx, orgA, device1, leased.RunID, leased.Generation)
		if !errors.Is(err, dispatch.ErrAuthorizationRevoked) {
			t.Fatalf("old binding after machine change: %v", err)
		}
		return nil
	})
	h.exec(`UPDATE openfox.physical_devices SET capabilities=$1::jsonb WHERE id=$2`, fullCapabilities, replacement)
	if _, err := create(alice, "", direct); !errors.Is(err, conversations.ErrWorkspaceUnavailable) {
		t.Fatalf("legacy daemon: %v", err)
	}
}

// A live session authenticates the request in both cases. Capability negotiation
// is per request to protect an older client reconnecting after a newer heartbeat.
func (h *harness) verifyWorkspaceContextProtocol(lease dispatch.Lease) {
	h.t.Helper()
	keyID, token := newID(h.t), newID(h.t)
	h.exec(`WITH k AS (INSERT INTO openfox.device_keys(device_id,key_id,public_key)
		VALUES($1,$2,$3) RETURNING device_id,key_id,generation)
		INSERT INTO openfox.device_sessions(device_id,key_id,device_generation,key_generation,token_hash,expires_at)
		SELECT d.id,k.key_id,d.generation,k.generation,$4,clock_timestamp()+interval '1 minute' FROM k JOIN openfox.physical_devices d ON d.id=k.device_id`, device1, keyID, identity.HashSessionToken(keyID), identity.HashSessionToken(token))
	handler := api.New(identity.Service{}, h.pool, false)
	path := fmt.Sprintf("/v1/device/organizations/%s/runs/%s/context?generation=%d", orgA, lease.RunID, lease.Generation)
	for _, supported := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if supported {
			req.Header.Set("X-OpenFox-Conversation-Workdir", "1")
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if !supported && (out.Code != http.StatusConflict || !bytes.Contains(out.Body.Bytes(), []byte("WORKSPACE_SELECTION_UNSUPPORTED")) || bytes.Contains(out.Body.Bytes(), []byte("instructions"))) {
			h.t.Fatalf("legacy context: HTTP %d", out.Code)
		}
		if supported && (out.Code != http.StatusOK || !bytes.Contains(out.Body.Bytes(), []byte(`"workspace_selection"`))) {
			h.t.Fatalf("selected context: HTTP %d", out.Code)
		}
	}
}
