package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCenterBaseURL(t *testing.T) {
	cases := map[string]string{
		"wss://center.example.com/v1/device/connect": "https://center.example.com",
		"ws://127.0.0.1:8080/v1/device/connect":      "http://127.0.0.1:8080",
		// 中心 API 挂在 /api 下时保留前缀，否则设备 HTTP 调用会 404。
		"wss://center.example.com/api/v1/device/connect": "https://center.example.com/api",
		"ws://127.0.0.1:28080/api/v1/device/connect/":    "http://127.0.0.1:28080/api",
	}
	for input, want := range cases {
		if got, err := centerBaseURL(input); err != nil || got != want {
			t.Errorf("centerBaseURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"https://center.example.com", "wss://", "::"} {
		if _, err := centerBaseURL(input); err == nil {
			t.Errorf("centerBaseURL(%q) accepted", input)
		}
	}
}

func TestDaemonRunRejectsWorkspaceRootWithoutRemote(t *testing.T) {
	if err := run([]string{"daemon", "run", "--socket", t.TempDir() + "/d.sock", "--workspace-root", t.TempDir()}); err == nil {
		t.Fatal("workspace root accepted without remote mode")
	}
}

func TestParseDirectoryRoots(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	managed, repo := filepath.Join(base, "managed"), filepath.Join(base, "repo")
	for _, dir := range []string{managed, repo, filepath.Join(managed, "inner")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	roots, err := parseDirectoryRoots([]string{"team/openfox=" + repo}, managed)
	if err != nil || roots["team/openfox"] != repo {
		t.Fatalf("roots = %v, %v", roots, err)
	}
	for name, values := range map[string][]string{
		"missing separator": {"openfox"},
		"absolute name":     {"/openfox=" + repo},
		"parent traversal":  {"a/../b=" + repo},
		"relative path":     {"openfox=repo"},
		"missing path":      {"openfox=" + filepath.Join(base, "absent")},
		"duplicate name":    {"openfox=" + repo, "openfox=" + repo},
		"inside managed":    {"openfox=" + filepath.Join(managed, "inner")},
		"contains managed":  {"openfox=" + base},
	} {
		if _, err := parseDirectoryRoots(values, managed); err == nil {
			t.Errorf("%s: %v accepted", name, values)
		}
	}
}

func TestDaemonRunRejectsDirectoryRootWithoutWorkspaceRoot(t *testing.T) {
	err := run([]string{"daemon", "run", "--socket", t.TempDir() + "/d.sock", "--wss-url", "ws://127.0.0.1:1/v1/device/connect",
		"--device-id", "device", "--token-file", t.TempDir() + "/token", "--center-epoch", "1", "--directory-root", "a=" + t.TempDir()})
	if err == nil || err.Error() != "daemon --directory-root requires --workspace-root" {
		t.Fatalf("err = %v", err)
	}
}
