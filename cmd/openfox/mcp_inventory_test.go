package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"openfox/internal/daemon"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/devices"
	"openfox/internal/runtime/mcpconfig"
)

func TestMCPInventoryAlwaysUsesPrivateDeviceEndpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers.private_qa]\ncommand=\"do-not-run\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var version atomic.Int32
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-device-session" {
			t.Error("missing device authorization")
		}
		switch r.URL.Path {
		case "/v1/device/organizations/org/offers":
			switch version.Load() {
			case 1:
				w.Header().Set("X-OpenFox-MCP-Inventory-Private", "1")
			case 2:
				w.Header().Set("X-OpenFox-MCP-Inventory-Private", "2")
			}
			_, _ = w.Write([]byte(`{"offers":[]}`))
		case "/v1/device/organizations/org/mcp-inventory":
			var body struct {
				Runtimes map[string]mcpconfig.Inventory `json:"runtimes"`
			}
			if r.Method != "PUT" || json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Error("bad inventory PUT")
			}
			entry := body.Runtimes["codex"]
			if len(entry.Servers) != 1 || entry.Servers[0].Name != "private_qa" || entry.Status != "ok" {
				t.Error("wrong private inventory")
			}
			uploads.Add(1)
			w.WriteHeader(204)
		default:
			t.Error("unexpected route")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	src := daemon.NewStaticHeartbeatSource(devices.Heartbeat{})
	r := &capabilityReporter{prober: &fakeProber{}, source: src}
	client := &supervisor.Client{BaseURL: server.URL, Token: func(context.Context) (string, error) { return "fixture-device-session", nil }, OnMCPInventoryPrivacy: r.setInventoryPrivacy}
	r.storeInventory = client.StoreMCPInventory
	for _, v := range []int32{0, 1, 2, 1} {
		version.Store(v)
		before := uploads.Load()
		if _, e := client.ListOffers(t.Context(), "org"); e != nil {
			t.Fatal(e)
		}
		r.refresh(t.Context())
		if (uploads.Load() > before) != (v == 2) {
			t.Errorf("protocol %d upload count %d", v, uploads.Load())
		}
		hb, e := src.Current(t.Context())
		if e != nil || strings.Contains(string(hb.Capabilities), "private_qa") || strings.Contains(string(hb.Capabilities), "mcp_inventory") {
			t.Fatal("inventory leaked into heartbeat")
		}
	}
}
