package codex

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	rt "openfox/internal/runtime"
)

func TestWriteSkillsPermissionsAndRemoval(t *testing.T) {
	source := writeSourceAuth(t, "token")
	home, err := prepareCodexHome(t.TempDir(), source)
	if err != nil {
		t.Fatal(err)
	}
	skills := []rt.Skill{{Name: "alpha", Files: []rt.SkillFile{
		{Path: "SKILL.md", Content: []byte("---\nname: alpha\n---\n")},
		{Path: "refs/deep/a.txt", Content: []byte("A")},
	}}}
	if err := writeSkills(home.dir, skills); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{
			"skills": 0o700, "skills/alpha": 0o700, "skills/alpha/refs": 0o700, "skills/alpha/refs/deep": 0o700,
			"skills/alpha/SKILL.md": 0o600, "skills/alpha/refs/deep/a.txt": 0o600,
		} {
			info, err := os.Stat(filepath.Join(home.dir, path))
			if err != nil || info.Mode().Perm() != want {
				t.Fatalf("%s mode = %v, %v; want %v", path, info, err, want)
			}
		}
	}
	if content, _ := os.ReadFile(filepath.Join(home.dir, "skills/alpha/refs/deep/a.txt")); string(content) != "A" {
		t.Fatalf("content = %q", content)
	}
	if _, err := home.finish(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(home.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("home still exists: %v", err)
	}
}

func TestWriteSkillsRejectsUnsafeInput(t *testing.T) {
	file := func(path string) []rt.SkillFile { return []rt.SkillFile{{Path: path, Content: []byte("x")}} }
	cases := map[string][]rt.Skill{
		"absolute path":  {{Name: "a", Files: file("/etc/passwd")}},
		"dotdot":         {{Name: "a", Files: file("../evil")}},
		"nested dotdot":  {{Name: "a", Files: file("x/../../evil")}},
		"empty segment":  {{Name: "a", Files: file("x//y")}},
		"backslash":      {{Name: "a", Files: file(`x\y`)}},
		"control char":   {{Name: "a", Files: file("x\ny")}},
		"empty path":     {{Name: "a", Files: file("")}},
		"bad name":       {{Name: "../a", Files: file("SKILL.md")}},
		"dot name":       {{Name: ".hidden", Files: file("SKILL.md")}},
		"no files":       {{Name: "a"}},
		"duplicate name": {{Name: "a", Files: file("SKILL.md")}, {Name: "a", Files: file("SKILL.md")}},
		"duplicate file": {{Name: "a", Files: append(file("SKILL.md"), file("SKILL.md")...)}},
		"file then dir":  {{Name: "a", Files: append(file("x"), file("x/y")...)}},
		"too many bytes": {{Name: "a", Files: []rt.SkillFile{{Path: "big", Content: make([]byte, 5<<20+1)}}}},
	}
	for name, skills := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			home := filepath.Join(parent, "home")
			if err := os.Mkdir(home, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := writeSkills(home, skills); err == nil {
				t.Fatal("expected error")
			}
			// 父目录里不能出现越界文件。
			if entries, _ := os.ReadDir(parent); len(entries) != 1 {
				t.Fatalf("parent entries = %v", entries)
			}
		})
	}
}

func TestWriteSkillsDoesNotFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	outside := t.TempDir()
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	// 预置 skills/a -> outside，写入必须失败且 outside 不被改动。
	if err := os.Symlink(outside, filepath.Join(home, "skills", "a")); err != nil {
		t.Fatal(err)
	}
	if err := writeSkills(home, []rt.Skill{{Name: "a", Files: []rt.SkillFile{{Path: "SKILL.md", Content: []byte("x")}}}}); err == nil {
		t.Fatal("expected error for symlinked skill directory")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("outside modified: %v", entries)
	}
	// 技能目录内预置指向外部的符号链接目录与文件。
	home2 := t.TempDir()
	skillDir := filepath.Join(home2, "skills", "b")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outside, filepath.Join(skillDir, "link"))
	_ = os.Symlink(filepath.Join(outside, "target"), filepath.Join(skillDir, "SKILL.md"))
	for _, path := range []string{"link/x", "SKILL.md"} {
		_ = os.RemoveAll(filepath.Join(home2, "skills", "b2"))
		if err := writeSkillFile(skillDir, rt.SkillFile{Path: path, Content: []byte("x")}); err == nil {
			t.Fatalf("%s: expected error", path)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("outside modified: %v", entries)
	}
}

func TestPrepareWithSkillsAndWithout(t *testing.T) {
	source := writeSourceAuth(t, "token")
	adapter := &Adapter{HomeRoot: t.TempDir(), Environment: map[string]string{"CODEX_HOME": source}}
	// 没有技能：独立 CODEX_HOME 不出现 skills 目录。
	prepared, err := adapter.prepare("/bin/codex", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(prepared.home.dir, "skills")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("skills dir without skills: %v", err)
	}
	prepared.home.remove()

	prepared, err = adapter.prepareWithSkills("/bin/codex", nil, nil, []rt.Skill{{Name: "alpha", Files: []rt.SkillFile{{Path: "SKILL.md", Content: []byte("x")}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(prepared.home.dir, "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	dir := prepared.home.dir
	prepared.home.remove()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("home still exists: %v", err)
	}
	// 写入失败时目录被清理，错误是 invalid request。
	before, _ := os.ReadDir(adapter.HomeRoot)
	_, err = adapter.prepareWithSkills("/bin/codex", nil, nil, []rt.Skill{{Name: "alpha", Files: []rt.SkillFile{{Path: "../x", Content: []byte("x")}}}})
	if !errors.Is(err, rt.ErrInvalidRequest) {
		t.Fatalf("err = %v", err)
	}
	if after, _ := os.ReadDir(adapter.HomeRoot); len(after) != len(before) {
		t.Fatalf("leftover homes: %v", after)
	}
}

func TestSourceHomeFollowsAdapterEnvironment(t *testing.T) {
	adapter := &Adapter{Environment: map[string]string{"CODEX_HOME": "/tmp/custom-codex"}}
	if got, err := adapter.SourceHome(); err != nil || got != "/tmp/custom-codex" {
		t.Fatalf("SourceHome = %q, %v", got, err)
	}
}
