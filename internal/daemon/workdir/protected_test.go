package workdir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// protectedFixture 在临时目录里建一个假的主目录，避免依赖真实 HOME。
func protectedFixture(t *testing.T, goos string) (home, config string, list ProtectedDirectories) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(base, "home")
	config = filepath.Join(home, "Library", "Application Support", "OpenFox")
	for _, dir := range []string{
		filepath.Join(home, ".ssh"), filepath.Join(home, "code", "project"),
		filepath.Join(home, "Library", "Keychains"), filepath.Join(config, "workspaces"),
		filepath.Join(home, ".config", "gcloud"), filepath.Join(home, "Documents"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return home, config, DefaultProtectedDirectories(home, config, goos)
}

func TestProtectedDirectoriesMatch(t *testing.T) {
	home, config, list := protectedFixture(t, "linux")
	cases := []struct {
		name      string
		path      string
		protected bool
	}{
		{"home root", home, true},
		{"home root trailing slash", home + "/", true},
		{"ssh directory", filepath.Join(home, ".ssh"), true},
		{"inside ssh", filepath.Join(home, ".ssh", "keys"), true},
		{"keychains", filepath.Join(home, "Library", "Keychains"), true},
		{"library root itself", filepath.Join(home, "Library"), true},
		{"parent of gcloud", filepath.Join(home, ".config"), true},
		{"openfox config dir", config, true},
		{"openfox workspaces (outside guard exemption)", filepath.Join(config, "workspaces"), true},
		{"parent of home", filepath.Dir(home), true},
		{"filesystem root", "/", true},
		{"system directory", "/System/Library", true},
		{"usr", "/usr/bin", true},
		{"project directory", filepath.Join(home, "code", "project"), false},
		{"documents", filepath.Join(home, "Documents"), false},
		{"code root", filepath.Join(home, "code"), false},
		{"not yet created project", filepath.Join(home, "code", "new-project"), false},
		{"dotdot back into ssh", filepath.Join(home, "code", "..", ".ssh"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, hit := list.ProtectedMatch(tc.path); hit != tc.protected {
				t.Fatalf("ProtectedMatch(%q) = %v, want %v", tc.path, hit, tc.protected)
			}
		})
	}
}

func TestProtectedDirectoriesSymlinkBypass(t *testing.T) {
	home, _, list := protectedFixture(t, "linux")
	link := filepath.Join(home, "code", "innocent")
	if err := os.Symlink(filepath.Join(home, ".ssh"), link); err != nil {
		t.Fatal(err)
	}
	if _, hit := list.ProtectedMatch(link); !hit {
		t.Fatal("symlink to ~/.ssh must be protected")
	}
	if _, hit := list.ProtectedMatch(filepath.Join(link, "nested")); !hit {
		t.Fatal("path below a symlink to ~/.ssh must be protected")
	}
	homeLink := filepath.Join(home, "code", "home-alias")
	if err := os.Symlink(home, homeLink); err != nil {
		t.Fatal(err)
	}
	if _, hit := list.ProtectedMatch(homeLink); !hit {
		t.Fatal("symlink to the home root must be protected")
	}
}

func TestProtectedDirectoriesCaseFolding(t *testing.T) {
	home, _, darwin := protectedFixture(t, "darwin")
	upper := filepath.Join(home, ".SSH")
	if _, hit := darwin.ProtectedMatch(upper); !hit {
		t.Fatal("darwin list must match ~/.SSH case-insensitively")
	}
	if _, hit := darwin.ProtectedMatch(strings.ToUpper(filepath.Join(home, "code", "project")) + "-x"); hit {
		// 大写路径只是不存在的普通目录，不能因为大小写折叠误伤。
		t.Fatal("unrelated upper-case path must not be protected")
	}
	linux := DefaultProtectedDirectories(home, "", "linux")
	if linux.FoldCase {
		t.Fatal("linux list must compare case-sensitively")
	}
}

func TestGuardPolicyProtectedDirectories(t *testing.T) {
	home, config, list := protectedFixture(t, "linux")
	managed := filepath.Join(config, "workspaces")
	policy, err := GuardPolicy{ManagedRoot: managed, Protected: list}.normalize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.check(filepath.Join(managed, "wt-1")); err != nil {
		t.Fatalf("managed workspace below the config dir must stay usable: %v", err)
	}
	if _, err := policy.check(filepath.Join(home, "code", "project")); err != nil {
		t.Fatalf("project directory must stay usable: %v", err)
	}
	for _, path := range []string{home, filepath.Join(home, ".ssh"), config, filepath.Join(config, "runs.db")} {
		if _, err := policy.check(path); !errors.Is(err, ErrPathProtected) {
			t.Fatalf("check(%q) = %v, want ErrPathProtected", path, err)
		}
	}
	// 管理根本身命中名单（主目录根）时不整体放行：其下的路径照样按名单判断。
	unsafe, err := GuardPolicy{ManagedRoot: home, Protected: list}.normalize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unsafe.check(filepath.Join(home, ".ssh")); !errors.Is(err, ErrPathProtected) {
		t.Fatalf("managed root at home must not exempt ~/.ssh: %v", err)
	}
	if _, err := unsafe.check(home); !errors.Is(err, ErrPathProtected) {
		t.Fatalf("managed root at home must be refused: %v", err)
	}
}
