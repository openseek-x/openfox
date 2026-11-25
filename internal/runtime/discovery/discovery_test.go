package discovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPriorityFallbackAndRescan(t *testing.T) {
	home := t.TempDir()
	tool := Tool{Name: "pi", EnvKeys: []string{"OPENFOX_PI_PATH"}}
	env := map[string]string{}
	search := Search{Home: home, OS: "linux", Getenv: func(k string) string { return env[k] }}
	install := func(dir, contents string) string {
		t.Helper()
		dir = filepath.Join(home, dir)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, tool.Name)
		if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	probe := func(path string) (string, error) {
		b, e := os.ReadFile(path)
		if string(b) == "broken" {
			return "", errors.New("invalid version")
		}
		return string(b), e
	}
	older := install(".nvm/versions/node/v22.9.1/bin", "old")
	newer := install(".nvm/versions/node/v22.10.1/bin", "new")
	local := install("bin", "broken")
	env["PATH"] = filepath.Dir(local)
	check := func(path, source, version string) {
		t.Helper()
		c, v, err := search.Resolve(t.Context(), tool, "", probe)
		if err != nil || c.Path != path || c.Source != source || v != version {
			t.Fatalf("got %+v / %q / %v", c, v, err)
		}
	}
	check(newer, "well_known_bin", "new")
	install("bin", "replacement")
	check(local, "path", "replacement")
	env["OPENFOX_PI_PATH"] = older
	check(older, "environment", "old")
	if err := os.Remove(older); err != nil {
		t.Fatal(err)
	}
	if _, _, err := search.Resolve(t.Context(), tool, "", probe); err == nil {
		t.Fatal("失效的显式路径不应静默回退")
	}
	if _, err := search.KnownPath(t.Context(), tool, "", "/bin/sh"); err == nil {
		t.Fatal("不应接受无关的系统可执行文件")
	}
}

func TestBundledPiOnMac(t *testing.T) {
	home := t.TempDir()
	bundle := filepath.Join(home, ".local/share/openfox/desktop-pi")
	if err := os.MkdirAll(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	pi := filepath.Join(bundle, "pi")
	if err := os.WriteFile(pi, nil, 0700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	search := Search{Home: home, OS: "darwin", Getenv: func(k string) string { return env[k] }}
	tool := Tool{Name: "pi", EnvKeys: []string{"OPENFOX_PI_PATH"}}
	probe := func(string) (string, error) { return "1.5.0", nil }
	check := func(source string) {
		t.Helper()
		c, _, err := search.Resolve(t.Context(), tool, "", probe)
		if err != nil || c.Source != source || c.Path != pi {
			t.Fatalf("candidate=%+v err=%v", c, err)
		}
	}
	check("macos_app")
	if got, err := search.KnownPath(t.Context(), tool, "", pi); err != nil || got != pi {
		t.Fatalf("known path=%q err=%v", got, err)
	}
	env["PATH"] = bundle
	check("path")
	env["OPENFOX_PI_PATH"] = filepath.Join(home, "missing")
	if _, _, err := search.Resolve(t.Context(), tool, "", probe); err == nil {
		t.Fatal("显式选择无效时不应改用内置 Pi")
	}
	delete(env, "OPENFOX_PI_PATH")
	delete(env, "PATH")
	search.OS = "linux"
	if _, _, err := search.Resolve(t.Context(), tool, "", probe); err == nil {
		t.Fatal("其他平台不应发现 macOS 安装包")
	}
}

func TestOneLoginShellPerScan(t *testing.T) {
	home := t.TempDir()
	calls := 0
	search := Search{Home: home, OS: "darwin", Getenv: func(k string) string {
		if k == "SHELL" {
			return "/bin/sh"
		}
		return ""
	}, LoginPath: func(context.Context, string) string { calls++; return home }}
	ctx := WithScan(t.Context())
	for _, name := range []string{"pi", "opencode"} {
		if err := os.WriteFile(filepath.Join(home, name), nil, 0700); err != nil {
			t.Fatal(err)
		}
		item, _, err := search.Resolve(ctx, Tool{Name: name}, "", func(string) (string, error) { return "1.0.0", nil })
		if err != nil || item.Source != "login_shell" {
			t.Fatalf("got %+v %v", item, err)
		}
	}
	if calls != 1 {
		t.Fatalf("启动了 %d 次登录 shell", calls)
	}
}

func TestExecutablePathSelectsMatchingInterpreterFirst(t *testing.T) {
	got := ExecutablePath("/bin:/opt/node/bin:/usr/bin", "/opt/node/bin/pi")
	if got != "/opt/node/bin:/bin:/usr/bin" {
		t.Fatalf("PATH = %s", got)
	}
}
