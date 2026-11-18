package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// 重启 / 启动在服务已载入时用 kickstart，未载入（kickstart 失败）时重新 bootstrap plist，
// 不会出现「先 bootout 再 kickstart」导致服务被卸载后起不来的情况。
func TestManagerRestartAndStartBootstrapWhenUnloaded(t *testing.T) {
	for _, test := range []struct {
		name     string
		call     func(Manager, context.Context) error
		loaded   bool
		expected []string
	}{
		{"restart loaded", Manager.Restart, true, []string{"kickstart -k gui/501/" + LaunchAgentLabel}},
		{"restart unloaded", Manager.Restart, false, []string{"kickstart -k gui/501/" + LaunchAgentLabel, "bootstrap gui/501 /tmp/agent.plist"}},
		{"start loaded", Manager.Start, true, []string{"kickstart gui/501/" + LaunchAgentLabel}},
		{"start unloaded", Manager.Start, false, []string{"kickstart gui/501/" + LaunchAgentLabel, "bootstrap gui/501 /tmp/agent.plist"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			path := filepath.Join(t.TempDir(), "agent.plist")
			expected := append([]string(nil), test.expected...)
			for i := range expected {
				expected[i] = strings.ReplaceAll(expected[i], "/tmp/agent.plist", path)
			}
			manager := Manager{UserID: 501, LaunchAgentPath: path, RunCommand: func(_ context.Context, name string, args ...string) error {
				calls = append(calls, strings.Join(args, " "))
				if args[0] == "kickstart" && !test.loaded {
					return errors.New("exit status 113")
				}
				return nil
			}}
			if err := test.call(manager, context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, expected) {
				t.Fatalf("launchctl calls = %v, want %v", calls, expected)
			}
		})
	}
}
