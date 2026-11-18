package stage2

import (
	"encoding/json"
	"net/http"
	"testing"

	"openfox/internal/devices"
)

// R07: real registration/signature/session, heartbeat JSONB persistence and the
// existing organization/device authorization boundary, without running tools.
func TestDevHeartbeatDevelopmentTools(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	device := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	path := "devices/" + device.ID
	read := func() []devices.DevelopmentTool {
		var detail struct {
			Tools []devices.DevelopmentTool `json:"development_tools"`
		}
		owner.must(http.StatusOK, http.MethodGet, path, nil, &detail)
		if detail.Tools == nil {
			t.Fatal("development_tools must be an array")
		}
		return detail.Tools
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("old device=%+v", got)
	}
	tools := []map[string]any{
		{"name": "node", "version": "v22.23.1", "path": "/工具 bin/node", "status": "AVAILABLE", "checked_at": "2026-11-13T01:02:03Z"},
		{"name": "npm", "version": "", "path": "", "status": "MISSING", "checked_at": "2026-11-13T01:02:03Z"},
		{"name": "python", "version": "", "path": "/bin/python3", "status": "TIMEOUT", "checked_at": "2026-11-13T01:02:05Z"},
		{"name": "uv", "version": "", "path": "/bin/uv", "status": "ERROR", "checked_at": "2026-11-13T01:02:05Z"},
	}
	heartbeat := func(capabilities map[string]any) {
		r := e.deviceCall(device, http.MethodPost, "/v1/device/heartbeat", map[string]any{
			"center_epoch": device.CenterEpoch, "protocol_major": 1, "protocol_minor": 0, "daemon_version": "r07-test",
			"operating_system": "darwin", "architecture": "arm64", "capabilities": capabilities,
		})
		if r.Status != http.StatusOK {
			t.Fatalf("heartbeat=%d %s", r.Status, r.Body)
		}
	}
	for i := 0; i < 2; i++ {
		heartbeat(map[string]any{"development_tools": tools})
		got := read()
		if len(got) != 4 || got[0].Path != "/工具 bin/node" || got[0].Version != "v22.23.1" || got[2].Status != "TIMEOUT" {
			t.Fatalf("detail=%+v", got)
		}
	}
	var persisted []byte
	if err := e.superuser.QueryRow(e.ctx, `SELECT capabilities->'development_tools' FROM openfox.physical_devices WHERE id=$1::uuid`, device.ID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	var saved []devices.DevelopmentTool
	if err := json.Unmarshal(persisted, &saved); err != nil || len(saved) != 4 {
		t.Fatalf("persisted=%s err=%v", persisted, err)
	}
	var page struct {
		Devices []struct {
			ID    string                    `json:"id"`
			Tools []devices.DevelopmentTool `json:"development_tools"`
		} `json:"devices"`
	}
	owner.must(http.StatusOK, http.MethodGet, "devices", nil, &page)
	found := false
	for _, item := range page.Devices {
		if item.ID == device.ID {
			found = len(item.Tools) == 4
		}
	}
	if !found {
		t.Fatal("list omitted tool snapshot")
	}
	// Existing settings PATCH must not erase the independently reported inventory.
	owner.must(http.StatusOK, http.MethodPatch, path, map[string]any{"name": "R07 computer", "settings_version": 1}, nil)
	if len(read()) != 4 {
		t.Fatal("settings patch erased tool snapshot")
	}
	outsider := e.client(agOutsiderID)
	for _, org := range []string{e.org, e.otherOrg} {
		if r := outsider.doIn(org, http.MethodGet, path, nil); r.Status != http.StatusNotFound {
			t.Fatalf("cross-tenant detail=%d %s", r.Status, r.Body)
		}
	}
	// A malformed entry is ignored independently; later legacy heartbeat replaces
	// the complete capability document and removes the old snapshot.
	tools[0]["status"] = "invalid"
	heartbeat(map[string]any{"development_tools": tools})
	if len(read()) != 3 {
		t.Fatal("invalid tool was not ignored")
	}
	heartbeat(map[string]any{})
	if len(read()) != 0 {
		t.Fatal("legacy heartbeat retained stale inventory")
	}
}
