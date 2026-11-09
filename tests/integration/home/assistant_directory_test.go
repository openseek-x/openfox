package home

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

func verifyAssistantDirectory(t *testing.T, h harness, call func(string, string, string, bool, any) int, assistant agents.Agent, grant string) {
	t.Helper()
	const second = "00000000-0000-0000-0000-000000000022"
	admin, err := pgx.Connect(h.ctx, os.Getenv("OPENFOX_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(h.ctx)
	_, err = admin.Exec(h.ctx, `UPDATE openfox.physical_devices SET owner_user_id=$1,capabilities=capabilities||'{"conversation_workdir":true,"conversation_workdir_roots":["qa"],"conversation_workdir_git_roots":["qa"]}'::jsonb WHERE id IN ($2,$3)`, homeOwnerA, adDevice, second)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/devices", "/devices/" + adDevice} {
		var metadata json.RawMessage
		if status := call(http.MethodGet, path, "", false, &metadata); status != http.StatusOK || !bytes.Contains(metadata, []byte(`"conversation_workdir":true`)) ||
			bytes.Contains(metadata, []byte("conversation_workdir_roots")) || bytes.Contains(metadata, []byte("conversation_workdir_git_roots")) {
			t.Fatalf("device metadata root redaction %s: HTTP %d", path, status)
		}
	}
	var secondGrant string
	if err := admin.QueryRow(h.ctx, `SELECT id::text FROM openfox.device_grants WHERE organization_id=$1 AND device_id=$2`, homeOrgA, second).Scan(&secondGrant); err != nil {
		t.Fatal(err)
	}
	encode := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	endpoint := "/assistant/work-directory"
	var settings agents.AssistantDirectorySettings
	if status := call("GET", endpoint, "", false, &settings); status != 200 || settings.Directory != "" || !slices.Equal(settings.Directories, []string{"qa"}) {
		t.Fatalf("initial settings %d %+v", status, settings)
	}
	propose := func(dir string) string {
		return encode(agents.AssistantDirectoryInput{Directory: dir, DeviceID: settings.DeviceID, Version: settings.Version})
	}
	if status := call("PUT", endpoint, propose("unregistered"), true, nil); status != 403 {
		t.Fatalf("unregistered root %d", status)
	}
	if status := call("PUT", endpoint, encode(map[string]any{"device_id": settings.DeviceID, "version": settings.Version}), true, nil); status != 400 {
		t.Fatalf("missing directory must not clear the grant: %d", status)
	}
	change := propose("qa")
	if status := call("PUT", endpoint, change, false, nil); status != 403 {
		t.Fatalf("unprotected write %d", status)
	}
	if status := call("PUT", endpoint, change, true, &settings); status != 200 || settings.Directory != "qa" {
		t.Fatalf("allow registered root %d %+v", status, settings)
	}
	if status := call("PUT", endpoint, change, true, nil); status != 409 {
		t.Fatalf("stale version %d", status)
	}
	store := agents.PostgresStore{Pool: h.pool}
	ivySel := tenancy.OrganizationSelection{OrganizationID: homeOrgA, ActorID: asIvy, AuthorizationVersion: 1}
	ivyActor := agents.Actor{Role: policy.OrganizationRoleMember, UserID: asIvy, AuthorizationVersion: 1}
	if _, err := store.AssistantDirectory(h.ctx, ivySel, ivyActor, nil); !errors.Is(err, agents.ErrForbidden) {
		t.Fatalf("other owner's computer: %v", err)
	}
	optsPath := "/conversation-workspace-options?agent_id=" + assistant.ID
	var opts conversations.WorkspaceOptions
	if status := call("GET", optsPath, "", false, &opts); status != 200 || !opts.CanManageDirectory || !slices.Equal(opts.Directories, []string{"qa"}) {
		t.Fatalf("assistant options %d %+v", status, opts)
	}
	id, _ := collaboration.NewID()
	if status := call("POST", "/conversations", encode(map[string]any{"id": id, "agent_id": assistant.ID, "space_id": assistant.SpaceID, "workspace_selection": conversations.WorkspaceSelection{Mode: "DIRECT", Directory: "qa/sub"}}), true, nil); status != 201 {
		t.Fatalf("selected directory %d", status)
	}
	if status := call("GET", optsPath, "", false, &opts); status != 200 || !slices.Equal(opts.RecentDirectories, []string{"qa/sub"}) {
		t.Fatalf("stored recent %d %+v", status, opts)
	}
	// Even a caller that knows all identifiers cannot mutate another actor's row.
	h.run(asIvy, policy.OrganizationRoleMember, func(ctx context.Context, tx pgx.Tx, actor conversations.Actor) error {
		return conversations.ForgetWorkDirectory(ctx, tx, homeOrgA, homeOwnerA, assistant.ID, adDevice, "qa/sub")
	})
	if status := call("GET", optsPath, "", false, &opts); status != 200 || len(opts.RecentDirectories) != 1 {
		t.Fatalf("foreign delete leaked %d %+v", status, opts)
	}
	recentPath := "/conversation-workspace-recents?agent_id=" + assistant.ID + "&device_id=" + adDevice
	for i := 0; i < 2; i++ {
		if status := call("DELETE", recentPath, `{"directory":"qa/sub"}`, true, nil); status != http.StatusNoContent {
			t.Fatalf("forget recent attempt %d=%d", i, status)
		}
	}
	if status := call("GET", optsPath, "", false, &opts); status != 200 || len(opts.RecentDirectories) != 0 {
		t.Fatalf("after delete %d %+v", status, opts)
	}
	var result struct {
		Assistant agents.Agent `json:"assistant"`
	}
	if status := call("PUT", "/assistant", encode(agents.AssistantInput{ComputerID: adDevice, GrantID: grant, Runtime: "claude-code"}), true, &result); status != 200 || result.Assistant.Config == nil || !slices.Equal(result.Assistant.Config.DirectoryRoots, []string{"qa"}) {
		t.Fatalf("same computer changed permission %d: %+v", status, result.Assistant.Config)
	}
	if status := call("PUT", "/assistant", encode(agents.AssistantInput{ComputerID: second, GrantID: secondGrant, Runtime: "codex"}), true, &result); status != 200 || result.Assistant.Config == nil || result.Assistant.Config.DirectoryMode != agents.DirectoryNone || len(result.Assistant.Config.DirectoryRoots) != 0 {
		t.Fatalf("other computer inherited permission %d: %+v", status, result.Assistant.Config)
	}
}
