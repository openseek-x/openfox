package skillpack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func write(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseDescription(t *testing.T) {
	cases := map[string]string{
		"---\nname: a\ndescription: Hello world\n---\nbody":           "Hello world",
		"---\ndescription: \"Quoted: yes\"\n---\n":                    "Quoted: yes",
		"---\ndescription: 'it''s'\n---\n":                            "it's",
		"---\r\nname: a\r\ndescription: crlf\r\n---\r\n":              "crlf",
		"---\ndescription: >\n  line one\n  line two\nname: a\n---\n": "line one line two",
		"---\nname: a\n---\ndescription: in body":                     "",
		"no frontmatter\ndescription: x":                              "",
		"---\nname: a\n":                                              "",
		"\ufeff---\ndescription: bom\n---\n":                          "bom",
	}
	for input, want := range cases {
		if got := ParseDescription([]byte(input)); got != want {
			t.Errorf("%q → %q, want %q", input, got, want)
		}
	}
}

func TestScan(t *testing.T) {
	skills := t.TempDir()
	write(t, skills, "alpha/SKILL.md", "---\nname: alpha\ndescription: "+strings.Repeat("字", 200)+"\n---\n")
	write(t, skills, "alpha/refs/a.txt", "12345")
	write(t, skills, "beta/SKILL.md", "---\ndescription: B\n---\n")
	write(t, skills, "nofile/README.md", "x")
	write(t, skills, ".system/other/SKILL.md", "x")
	write(t, skills, "loose.md", "x")
	write(t, skills, "has space/SKILL.md", "x")
	items, err := Scan([]string{skills})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, item := range items {
		names = append(names, item.Name)
	}
	if strings.Join(names, ",") != "alpha,beta,has space" {
		t.Fatalf("names = %v", names)
	}
	alpha := items[0]
	if len([]rune(alpha.Description)) != MaxDescriptionRunes || alpha.FileCount != 2 || !alpha.Importable || alpha.TotalBytes < 5 {
		t.Fatalf("alpha = %+v", alpha)
	}
	if items[2].Importable {
		t.Fatal("invalid directory name must not be importable")
	}
}

func TestScanMissingDirectory(t *testing.T) {
	items, err := Scan([]string{filepath.Join(t.TempDir(), "none")})
	if err != nil || items == nil || len(items) != 0 {
		t.Fatalf("items = %v, %v", items, err)
	}
}

func TestScanLimits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	skills := t.TempDir()
	write(t, skills, "link/SKILL.md", "x")
	if err := os.Symlink("/etc/hosts", filepath.Join(skills, "link", "evil")); err != nil {
		t.Fatal(err)
	}
	write(t, skills, "many/SKILL.md", "x")
	for i := 0; i < MaxFiles+5; i++ {
		write(t, skills, fmt.Sprintf("many/f%03d", i), "x")
	}
	write(t, skills, "big/SKILL.md", "x")
	write(t, skills, "big/blob", strings.Repeat("a", MaxBytes))
	write(t, skills, "ok/SKILL.md", "x")
	// 顶层符号链接目录不被列出。
	if err := os.Symlink(filepath.Join(skills, "ok"), filepath.Join(skills, "okcopy")); err != nil {
		t.Fatal(err)
	}
	items, err := Scan([]string{skills})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ScanItem{}
	for _, item := range items {
		got[item.Name] = item
	}
	if len(got) != 4 || got["okcopy"].Name != "" {
		t.Fatalf("items = %+v", items)
	}
	for _, name := range []string{"link", "many", "big"} {
		if got[name].Importable {
			t.Errorf("%s must not be importable: %+v", name, got[name])
		}
	}
	if !got["ok"].Importable {
		t.Errorf("ok = %+v", got["ok"])
	}
	// 超限时尽早停止：文件数不超过上限 + 1。
	if got["many"].FileCount > MaxFiles+1 {
		t.Errorf("walk did not stop early: %d", got["many"].FileCount)
	}
}

func TestScanResultSizeAndCount(t *testing.T) {
	skills := t.TempDir()
	desc := strings.Repeat("\"", 400) // JSON 转义后每个字符 2 字节
	for i := 0; i < MaxScanSkills+20; i++ {
		write(t, skills, fmt.Sprintf("s%03d/SKILL.md", i), "---\ndescription: '"+strings.ReplaceAll(desc, "\"", "x")+"'\n---\n")
	}
	items, err := Scan([]string{skills})
	if err != nil || len(items) != MaxScanSkills {
		t.Fatalf("len = %d, %v", len(items), err)
	}
	// 含需要转义的字符时仍不超过 64 KB。
	dir2 := t.TempDir()
	for i := 0; i < MaxScanSkills; i++ {
		write(t, dir2, fmt.Sprintf("s%03d/SKILL.md", i), "---\ndescription: "+strings.Repeat("<", 160)+"\n---\n")
	}
	items, err = Scan([]string{dir2})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{"supported": true, "runtime": "codex", "skills": items})
	if len(encoded) > MaxScanResultBytes || len(items) == 0 || len(items) == MaxScanSkills {
		t.Fatalf("encoded = %d bytes, items = %d", len(encoded), len(items))
	}
}

func packCode(err error) string {
	var packErr *PackError
	if errors.As(err, &packErr) {
		return packErr.Code
	}
	return "other:" + fmt.Sprint(err)
}

func TestPack(t *testing.T) {
	skills := t.TempDir()
	write(t, skills, "alpha/SKILL.md", "---\nname: alpha\n---\n")
	write(t, skills, "alpha/refs/deep/a.txt", "A")
	files, err := Pack([]string{skills}, "alpha")
	if err != nil || len(files) != 2 {
		t.Fatalf("files = %+v, %v", files, err)
	}
	paths := map[string]string{}
	for _, file := range files {
		if !ValidPath(file.Path) || strings.Contains(file.Path, `\`) {
			t.Fatalf("path %q", file.Path)
		}
		paths[file.Path] = string(file.Content)
	}
	if paths["refs/deep/a.txt"] != "A" || paths["SKILL.md"] == "" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestPackFailures(t *testing.T) {
	skills := t.TempDir()
	write(t, skills, "nomd/readme", "x")
	write(t, skills, "big/SKILL.md", "x")
	write(t, skills, "big/blob", strings.Repeat("a", MaxBytes))
	write(t, skills, "many/SKILL.md", "x")
	for i := 0; i < MaxFiles; i++ {
		write(t, skills, fmt.Sprintf("many/f%03d", i), "x")
	}
	write(t, skills, "file", "x")
	if runtime.GOOS != "windows" {
		write(t, skills, "sym/SKILL.md", "x")
		_ = os.Symlink("/etc/hosts", filepath.Join(skills, "sym", "evil"))
		_ = os.Symlink(filepath.Join(skills, "nomd"), filepath.Join(skills, "dirlink"))
	}
	cases := map[string]string{
		"missing": CodeNotFound, "nomd": CodeInvalid, "big": CodeTooLarge, "many": CodeTooLarge, "file": CodeInvalid,
		"../outside": CodeInvalid, "a/b": CodeInvalid, "..": CodeInvalid, ".": CodeInvalid, "": CodeInvalid,
		"/etc": CodeInvalid, ".hidden": CodeInvalid, strings.Repeat("a", 65): CodeInvalid,
	}
	if runtime.GOOS != "windows" {
		cases["sym"], cases["dirlink"] = CodeInvalid, CodeInvalid
	}
	for name, want := range cases {
		files, err := Pack([]string{skills}, name)
		if err == nil || files != nil || packCode(err) != want {
			t.Errorf("Pack(%q) = %v, %v; want %s", name, files, err, want)
		}
	}
}

func TestValidPath(t *testing.T) {
	good := []string{"SKILL.md", "a/b.txt", "a/b/c/.hidden", "中文/文件.md"}
	bad := []string{"", "/a", "a/", "a//b", "./a", "a/./b", "../a", "a/..", `a\b`, "a\x00b", "a\nb", strings.Repeat("a", 257)}
	for _, path := range good {
		if !ValidPath(path) {
			t.Errorf("%q should be valid", path)
		}
	}
	for _, path := range bad {
		if ValidPath(path) {
			t.Errorf("%q should be invalid", path)
		}
	}
}

func TestScanAndPackMultipleDirectories(t *testing.T) {
	agents, legacy := t.TempDir(), t.TempDir()
	write(t, agents, "shared/SKILL.md", "---\ndescription: from agents\n---\n")
	write(t, agents, "shared/new.txt", "N")
	write(t, agents, "only-agents/SKILL.md", "x")
	write(t, agents, ".system/hidden/SKILL.md", "x")
	write(t, agents, ".hidden/SKILL.md", "x")
	write(t, legacy, "shared/SKILL.md", "---\ndescription: from legacy\n---\n")
	write(t, legacy, "only-legacy/SKILL.md", "x")
	write(t, legacy, ".system/sys/SKILL.md", "x")
	// 靠前目录里同名但没有 SKILL.md 的目录不遮蔽后面的技能。
	write(t, agents, "broken/readme", "x")
	write(t, legacy, "broken/SKILL.md", "---\ndescription: legacy broken\n---\n")
	dirs := []string{agents, legacy, filepath.Join(t.TempDir(), "absent")}
	items, err := Scan(dirs)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ScanItem{}
	names := []string{}
	for _, item := range items {
		got[item.Name] = item
		names = append(names, item.Name)
	}
	if strings.Join(names, ",") != "broken,only-agents,only-legacy,shared" {
		t.Fatalf("names = %v", names)
	}
	if got["shared"].Description != "from agents" || got["shared"].FileCount != 2 || got["broken"].Description != "legacy broken" {
		t.Fatalf("items = %+v", items)
	}
	files, err := Pack(dirs, "shared")
	if err != nil || len(files) != 2 {
		t.Fatalf("shared files = %+v, %v", files, err)
	}
	files, err = Pack(dirs, "only-legacy")
	if err != nil || len(files) != 1 {
		t.Fatalf("only-legacy files = %+v, %v", files, err)
	}
	if files, err = Pack(dirs, "broken"); err != nil || len(files) != 1 {
		t.Fatalf("broken files = %+v, %v", files, err)
	}
	for _, name := range []string{"hidden", ".system", "nope"} {
		if _, err := Pack(dirs, name); packCode(err) != CodeNotFound && packCode(err) != CodeInvalid {
			t.Errorf("Pack(%q) = %v", name, err)
		}
	}
	// 只在靠前目录存在且无 SKILL.md 时仍是 SKILL_INVALID。
	if _, err := Pack([]string{agents, legacy}, "x"); packCode(err) != CodeNotFound {
		t.Errorf("missing = %v", err)
	}
	onlyBroken := t.TempDir()
	write(t, onlyBroken, "nomd/readme", "x")
	if _, err := Pack([]string{onlyBroken, legacy}, "nomd"); packCode(err) != CodeInvalid {
		t.Errorf("nomd = %v", err)
	}
}

func TestScanResultStaysWithinJSONBBudgetForCJK(t *testing.T) {
	skills := t.TempDir()
	for i := 0; i < MaxScanSkills; i++ {
		write(t, skills, fmt.Sprintf("s%03d/SKILL.md", i), "---\ndescription: "+strings.Repeat("技", 160)+"\n---\n")
	}
	items, err := Scan([]string{skills})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(map[string]any{"supported": true, "runtime": "codex", "skills": items})
	if len(encoded) > 57344 || len(items) != MaxScanSkills {
		t.Fatalf("encoded = %d bytes, items = %d", len(encoded), len(items))
	}
}
