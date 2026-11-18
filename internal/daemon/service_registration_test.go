package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScopedServiceRegistrationArguments(t *testing.T) {
	m, binary := testServiceManager(t)
	config := filepath.Join(filepath.Dir(m.SocketPath), DeviceConfigFileName)
	if err := m.InstallService(binary, config); err != nil {
		t.Fatal(err)
	}
	scoped := m.ForRegistration(config)
	base := []string{binary, "daemon", "run"}
	for _, tc := range []struct {
		name    string
		args    []string
		allowed bool
	}{
		{"generated", []string{"--socket", m.SocketPath, "--config", config}, true},
		{"equals_syntax", []string{"-config=" + config, "--socket=" + m.SocketPath}, true},
		{"implicit_socket", []string{"--config", config}, true},
		{"different_config", []string{"--socket", m.SocketPath, "--config", config + ".other"}, false},
		{"different_socket", []string{"--socket", m.SocketPath + ".other", "--config", config}, false},
		{"missing_config", []string{"--socket", m.SocketPath}, false},
		{"config_override_last", []string{"--config", config, "--socket", m.SocketPath, "--config", config + ".other"}, false},
		{"config_match_last", []string{"--config", config + ".other", "--config", config, "--socket", m.SocketPath}, false},
		{"duplicate_matching_config", []string{"--config", config, "--config", config, "--socket", m.SocketPath}, false},
		{"socket_override_last", []string{"--config", config, "--socket", m.SocketPath, "--socket=" + m.SocketPath + ".other"}, false},
		{"unknown_argument", []string{"--config", config, "--socket", m.SocketPath, "--device-id", "different"}, false},
		{"positional_argument", []string{"--config", config, "--socket", m.SocketPath, "unexpected"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := launchAgentPlist(append(append([]string{}, base...), tc.args...), m.LogPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := WritePrivateFile(m.LaunchAgentPath, data); err != nil {
				t.Fatal(err)
			}
			_, err = scoped.ServiceSettings()
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
		})
	}
	// Legacy generic control remains unscoped, even when the plist's arguments
	// are deliberately not eligible for the new desktop controls.
	calls := 0
	m.RunCommand = func(context.Context, string, ...string) error { calls++; return nil }
	if err := m.Start(context.Background()); err != nil || calls != 1 {
		t.Fatalf("legacy start changed: %v", err)
	}
	if _, err := os.Stat(m.LaunchAgentPath); err != nil {
		t.Fatal(err)
	}
}
