package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"openfox/internal/daemon"
	"openfox/internal/devices"
)

func TestDirectoryResolverAndReportedGitRoots(t *testing.T) {
	center := newFakeCenter(t)
	registered, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ordinary := filepath.Join(registered, "nested")
	if err := os.Mkdir(ordinary, 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", registered, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	var stdout bytes.Buffer
	deps := testDeps(t, &stdout, "darwin", &fakeLaunchctl{})
	configDir := filepath.Join(t.TempDir(), "config")
	if err := runComputerWith([]string{"register", "--install-token", center.installToken(t), "--no-service", "--config-dir", configDir, "--directory-root", "repo=" + registered}, deps); err != nil {
		t.Fatal(err)
	}
	config, err := daemon.LoadDeviceConfig(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		OK        bool
		Path      string
		Directory string
		Reason    string
		Git       bool `json:"is_git"`
		Match     bool `json:"root_match"`
	}
	resolve := func(path, allow, device string) result {
		t.Helper()
		stdout.Reset()
		request := []string{"resolve-directory", "--config-dir", configDir, "--organization-id", config.OrganizationID, "--device-id", device, "--path", path}
		if allow != "" {
			request = append(request, "--allow-root", allow)
		}
		if err := runComputerWith(request, deps); err != nil {
			t.Fatal(err)
		}
		assertNoSecrets(t, stdout.String())
		var r result
		if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := resolve(ordinary, "repo", config.DeviceID); !r.OK || r.Git || r.Match || r.Directory != "repo/nested" || r.Path != ordinary {
		t.Fatalf("nested %+v", r)
	}
	if r := resolve("repo", "repo", config.DeviceID); !r.OK || !r.Git || !r.Match || r.Directory != "repo" || r.Path != registered {
		t.Fatalf("repo %+v", r)
	}
	if r := resolve(registered, "", config.DeviceID); r.OK || r.Reason != "outside_allowed_roots" || r.Path != "" {
		t.Fatalf("allowlist %+v", r)
	}
	if r := resolve(registered, "repo", "another"); r.OK || r.Reason != "wrong_device" || r.Path != "" {
		t.Fatalf("device %+v", r)
	}
	if r := resolve("repo/../escape", "repo", config.DeviceID); r.OK || r.Reason != "outside_allowed_roots" {
		t.Fatalf("escape %+v", r)
	}
	roots := map[string]string{"repo": registered, "docs": ordinary}
	reporter := capabilityReporter{directoryRoots: roots, gitRoots: probeGitRoots(t.Context(), roots), source: daemon.NewStaticHeartbeatSource(devices.Heartbeat{})}
	reporter.publish()
	hb, err := reporter.source.Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(hb.Capabilities), registered) {
		t.Fatal("physical path leaked in heartbeat")
	}
	var caps struct {
		Enabled bool     `json:"conversation_workdir"`
		Roots   []string `json:"conversation_workdir_roots"`
		Git     []string `json:"conversation_workdir_git_roots"`
	}
	if err = json.Unmarshal(hb.Capabilities, &caps); err != nil || !caps.Enabled || strings.Join(caps.Roots, ",") != "docs,repo" || strings.Join(caps.Git, ",") != "repo" {
		t.Fatalf("heartbeat %s (%v)", hb.Capabilities, err)
	}
}

// 阶段 12e（D-04）：注册根目录命中受保护名单时拒绝；已注册的旧根命中名单时选择它作为工作目录被拒绝。
func TestProtectedDirectoriesAtRegistrationAndSelection(t *testing.T) {
	center := newFakeCenter(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	ssh := filepath.Join(home, ".ssh")
	repo := filepath.Join(home, "code", "repo")
	for _, dir := range []string{ssh, repo} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	var stdout bytes.Buffer
	deps := testDeps(t, &stdout, "darwin", &fakeLaunchctl{})
	configDir := filepath.Join(base, "config")
	err = runComputerWith([]string{"register", "--install-token", center.installToken(t), "--no-service", "--config-dir", configDir, "--directory-root", "keys=" + ssh}, deps)
	if err == nil || !strings.Contains(err.Error(), "protected directory") {
		t.Fatalf("registering ~/.ssh as a root must be refused, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(configDir, "config.json")); !os.IsNotExist(statErr) {
		t.Fatalf("refused registration must not write config.json: %v", statErr)
	}
	if err := runComputerWith([]string{"register", "--install-token", center.installToken(t), "--no-service", "--config-dir", configDir, "--directory-root", "repo=" + repo}, deps); err != nil {
		t.Fatal(err)
	}
	config, err := daemon.LoadDeviceConfig(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 模拟旧版本写入的配置里已有命中名单的根：只告警不移除，但选择它作为工作目录时拒绝。
	config.DirectoryRoots["keys"] = ssh
	if err := daemon.WriteDeviceConfig(filepath.Join(configDir, "config.json"), config); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := runComputerWith([]string{"resolve-directory", "--config-dir", configDir, "--organization-id", config.OrganizationID,
		"--device-id", config.DeviceID, "--path", "keys", "--allow-root", "keys"}, deps); err != nil {
		t.Fatal(err)
	}
	var result struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.OK || result.Reason != "protected_directory" {
		t.Fatalf("~/.ssh must be refused as a working directory: %s (%v)", stdout.String(), err)
	}
}
