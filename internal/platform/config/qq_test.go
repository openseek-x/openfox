package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestQQConnectorConfiguration(t *testing.T) {
	for _, key := range []string{"OPENFOX_QQ_CONNECTOR_NODE_PATH", "OPENFOX_QQ_CONNECTOR_ROOT"} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Integrations.QQConnectorNodePath != "" || cfg.Integrations.QQConnectorRoot != "" {
		t.Fatal("QQ connector must be unconfigured by default")
	}
	// Load validates only deployment syntax. It must neither launch Node nor
	// install an SDK; the connector verifies the exact artifacts before use.
	dir := t.TempDir()
	node, root := filepath.Join(dir, "not-installed-node"), filepath.Join(dir, "not-installed-sdk")
	for _, tt := range []struct {
		name, node, root string
		valid            bool
	}{
		{"node only", node, "", false},
		{"root only", "", root, false},
		{"relative node", "node", root, false},
		{"relative root", node, "sdk", false},
		{"absolute pair", node, root, true},
		{"trimmed pair", " " + node + " ", " " + root + " ", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OPENFOX_QQ_CONNECTOR_NODE_PATH", tt.node)
			t.Setenv("OPENFOX_QQ_CONNECTOR_ROOT", tt.root)
			got, err := Load()
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v, err=%v", tt.valid, err)
			}
			if tt.valid && (got.Integrations.QQConnectorNodePath != strings.TrimSpace(tt.node) || got.Integrations.QQConnectorRoot != strings.TrimSpace(tt.root)) {
				t.Fatal("connector paths were not loaded")
			}
		})
	}
}
