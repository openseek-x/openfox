package skills

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestValidatePath(t *testing.T) {
	valid := []string{"SKILL.md", "scripts/run.sh", "a/b/c.txt", "中文/文件.md", ".hidden", "a.b/c", strings.Repeat("a", 256)}
	for _, path := range valid {
		if err := ValidatePath(path); err != nil {
			t.Errorf("%q should be valid: %v", path, err)
		}
	}
	invalid := []string{"", "/abs", "a//b", "a/./b", "./a", "../a", "a/..", "a/../b", "a/", `a\b`, "a\x00b", "a\nb", "a\x7fb",
		strings.Repeat("a", 257), "\xff\xfe", ".", ".."}
	for _, path := range invalid {
		if err := ValidatePath(path); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q should be rejected, got %v", path, err)
		}
	}
}

func TestParseSkillMD(t *testing.T) {
	cases := map[string]struct {
		input       string
		name, desc  string
		body        string
		wantErr     error
		descContain string
	}{
		"plain":       {input: "---\nname: my-skill\ndescription: Does things\n---\n\nUse it.\n", name: "my-skill", desc: "Does things", body: "Use it.\n"},
		"crlf":        {input: "---\r\nname: a1\r\ndescription: x\r\n---\r\nbody", name: "a1", desc: "x", body: "body"},
		"quoted":      {input: "---\nname: \"abc\"\ndescription: 'it''s ok'\n---\n", name: "abc", desc: "it's ok"},
		"json escape": {input: "---\nname: abc\ndescription: \"line\\nbreak \\u4e2d\"\n---\n", name: "abc", desc: "line\nbreak 中"},
		"folded":      {input: "---\nname: abc\ndescription: >\n  first line\n  second line\n\n  next para\n---\n", name: "abc", desc: "first line second line\nnext para"},
		"literal":     {input: "---\nname: abc\ndescription: |\n  one\n  two\n---\n", name: "abc", desc: "one\ntwo"},
		"continued":   {input: "---\nname: abc\ndescription: first\n  second\n---\n", name: "abc", desc: "first second"},
		"extra keys":  {input: "---\nname: abc\nlicense: MIT\nmetadata:\n  a: 1\ndescription: d # note\n---\n", name: "abc", desc: "d"},
		"bom":         {input: "\xef\xbb\xbf---\nname: abc\ndescription: \n---\n", name: "abc", desc: ""},
		"empty desc":  {input: "---\nname: abc\ndescription: \"\"\n---\n", name: "abc", desc: ""},
		"no front":    {input: "# Title\n", wantErr: ErrInvalid},
		"unclosed":    {input: "---\nname: abc\ndescription: x\n", wantErr: ErrInvalid},
		"no name":     {input: "---\ndescription: x\n---\n", wantErr: ErrInvalid},
		"no desc":     {input: "---\nname: abc\n---\n", wantErr: ErrInvalid},
		"empty name":  {input: "---\nname: \ndescription: x\n---\n", wantErr: ErrInvalid},
		"bad name":    {input: "---\nname: My Skill\ndescription: x\n---\n", wantErr: ErrNameInvalid},
		"upper name":  {input: "---\nname: Abc\ndescription: x\n---\n", wantErr: ErrNameInvalid},
		"long name":   {input: "---\nname: " + strings.Repeat("a", 65) + "\ndescription: x\n---\n", wantErr: ErrNameInvalid},
		"long desc":   {input: "---\nname: abc\ndescription: " + strings.Repeat("字", 1025) + "\n---\n", wantErr: ErrInvalid},
		"bad quote":   {input: "---\nname: abc\ndescription: \"oops\n---\n", wantErr: ErrInvalid},
		"bad utf8":    {input: "---\nname: abc\ndescription: \xff\n---\n", wantErr: ErrInvalid},
	}
	for label, item := range cases {
		parsed, err := ParseSkillMD([]byte(item.input))
		if item.wantErr != nil {
			if !errors.Is(err, item.wantErr) {
				t.Errorf("%s: err = %v, want %v", label, err, item.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", label, err)
			continue
		}
		if parsed.Name != item.name || parsed.Description != item.desc || (item.body != "" && parsed.Body != item.body) {
			t.Errorf("%s: got %+v", label, parsed)
		}
	}
	if _, err := ParseSkillMD([]byte("---\nname: abc\ndescription: " + strings.Repeat("字", 1024) + "\n---\n")); err != nil {
		t.Errorf("1024 runes should be accepted: %v", err)
	}
}

func TestBuildSkillMDRoundTrip(t *testing.T) {
	for _, description := range []string{"", "plain", "has: colon # and hash", "quote \" and 'single'", "多行\n描述", "123", "true"} {
		content := BuildSkillMD("my-skill-1", description, "  Do the thing.\n\nSecond paragraph.  ")
		parsed, err := ParseSkillMD(content)
		if err != nil {
			t.Fatalf("%q: %v", description, err)
		}
		if parsed.Name != "my-skill-1" || parsed.Description != description {
			t.Errorf("%q: got %+v", description, parsed)
		}
		if parsed.Body != "\nDo the thing.\n\nSecond paragraph.\n" && parsed.Body != "Do the thing.\n\nSecond paragraph.\n" {
			t.Errorf("%q: body = %q", description, parsed.Body)
		}
	}
	if got := string(BuildSkillMD("abc", "d", "")); got != "---\nname: \"abc\"\ndescription: \"d\"\n---\n" {
		t.Errorf("no instructions: %q", got)
	}
}

func skillMD(name string) []byte { return BuildSkillMD(name, "desc", "body") }

func TestNormalizeFiles(t *testing.T) {
	if _, err := NormalizeFiles([]File{{Path: "SKILL.md", Content: skillMD("a")}, {Path: "SKILL.md", Content: nil}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("duplicate path: %v", err)
	}
	if _, err := NormalizeFiles([]File{{Path: "SKILL.md", Content: skillMD("a")}, {Path: "x", Content: nil}, {Path: "x/y", Content: nil}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("file/dir clash: %v", err)
	}
	if _, err := NormalizeFiles([]File{{Path: "a.md", Content: nil}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing SKILL.md: %v", err)
	}
	if _, err := NormalizeFiles([]File{{Path: "../SKILL.md", Content: skillMD("a")}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("traversal: %v", err)
	}
	// 唯一一层子目录被去掉。
	files, err := NormalizeFiles([]File{{Path: "demo/SKILL.md", Content: skillMD("a")}, {Path: "demo/ref/x.md", Content: []byte("x")}})
	if err != nil || files[0].Path != "SKILL.md" || files[1].Path != "ref/x.md" {
		t.Errorf("strip root: %v %v", files, err)
	}
	// 根目录已有 SKILL.md 时不去前缀。
	files, err = NormalizeFiles([]File{{Path: "SKILL.md", Content: skillMD("a")}, {Path: "demo/x.md", Content: []byte("x")}})
	if err != nil || files[1].Path != "demo/x.md" {
		t.Errorf("keep root: %v %v", files, err)
	}
	// 两个顶层目录不去前缀 → 缺 SKILL.md。
	if _, err := NormalizeFiles([]File{{Path: "a/SKILL.md", Content: skillMD("a")}, {Path: "b/x", Content: nil}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("two roots: %v", err)
	}
	// 限额。
	many := make([]File, 101)
	for i := range many {
		many[i] = File{Path: "f" + strings.Repeat("a", i%50) + string(rune('A'+i/50)) + string(rune('a'+i%26)), Content: nil}
	}
	if _, err := NormalizeFiles(many); !errors.Is(err, ErrTooLarge) {
		t.Errorf("101 files: %v", err)
	}
	big := []File{{Path: "SKILL.md", Content: skillMD("a")}, {Path: "big.bin", Content: make([]byte, MaxTotalBytes)}}
	if _, err := NormalizeFiles(big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("5MB+: %v", err)
	}
	exact := []File{{Path: "SKILL.md", Content: skillMD("a")}}
	exact = append(exact, File{Path: "big.bin", Content: make([]byte, MaxTotalBytes-len(exact[0].Content))})
	if _, err := NormalizeFiles(exact); err != nil {
		t.Errorf("exactly 5MB: %v", err)
	}
}

func TestBuildPackage(t *testing.T) {
	pkg, err := BuildPackage([]File{{Path: "SKILL.md", Content: skillMD("demo-1")}})
	if err != nil || pkg.Name != "demo-1" || pkg.Description != "desc" || pkg.TotalBytes == 0 {
		t.Fatalf("pkg = %+v, %v", pkg, err)
	}
	if _, err := BuildPackage([]File{{Path: "SKILL.md", Content: []byte("no front matter")}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("no frontmatter: %v", err)
	}
}

func TestApplyChanges(t *testing.T) {
	existing := []Entry{{Path: "SKILL.md", Key: "k1", Size: 10}, {Path: "a.md", Key: "k2", Size: 5}, {Path: "z/b.md", Key: "k3", Size: 5}}
	result, err := ApplyChanges(existing, []Change{
		{Path: "a.md", Delete: true},
		{Path: "SKILL.md", Entry: Entry{Path: "SKILL.md", Key: "k9", Size: 20}},
		{Path: "new.txt", Entry: Entry{Path: "new.txt", Key: "k8", Size: 1}},
	})
	if err != nil || len(result) != 3 || result[0].Path != "SKILL.md" || result[0].Key != "k9" || result[1].Path != "new.txt" || result[2].Path != "z/b.md" {
		t.Fatalf("result = %+v, %v", result, err)
	}
	cases := map[string]struct {
		changes []Change
		want    error
	}{
		"delete SKILL.md": {[]Change{{Path: "SKILL.md", Delete: true}}, ErrDeleteSkillMD},
		"delete missing":  {[]Change{{Path: "nope", Delete: true}}, ErrFileNotFound},
		"bad path":        {[]Change{{Path: "../x", Entry: Entry{Path: "../x"}}}, ErrInvalid},
		"duplicate":       {[]Change{{Path: "x", Entry: Entry{Path: "x"}}, {Path: "x", Delete: true}}, ErrInvalid},
		"too big":         {[]Change{{Path: "x", Entry: Entry{Path: "x", Size: MaxTotalBytes}}}, ErrTooLarge},
		"dir clash":       {[]Change{{Path: "z", Entry: Entry{Path: "z", Size: 1}}}, ErrInvalid},
	}
	for label, item := range cases {
		if _, err := ApplyChanges(existing, item.changes); !errors.Is(err, item.want) {
			t.Errorf("%s: %v, want %v", label, err, item.want)
		}
	}
	var many []Change
	for i := 0; i < 99; i++ {
		many = append(many, Change{Path: "n" + string(rune('a'+i/26)) + string(rune('a'+i%26)), Entry: Entry{Size: 1}})
	}
	if _, err := ApplyChanges(existing, many); !errors.Is(err, ErrTooLarge) {
		t.Errorf("102 files: %v", err)
	}
}

func buildZip(t *testing.T, entries map[string]string, mutate func(*zip.FileHeader)) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range entries {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if mutate != nil {
			mutate(header)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte(content))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestUnzipFiles(t *testing.T) {
	data := buildZip(t, map[string]string{"demo/SKILL.md": string(skillMD("zipped")), "demo/ref/a.md": "a", "demo/": "", "__MACOSX/demo/._SKILL.md": "junk", "demo/.DS_Store": "junk"}, nil)
	files, err := UnzipFiles(data)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := BuildPackage(files)
	if err != nil || pkg.Name != "zipped" || len(pkg.Files) != 2 {
		t.Fatalf("pkg = %+v, %v", pkg, err)
	}

	for _, name := range []string{"../evil.md", "/abs.md", "a/../../b.md", `win\path.md`, "a//b.md"} {
		if _, err := UnzipFiles(buildZip(t, map[string]string{name: "x"}, nil)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q should be rejected: %v", name, err)
		}
	}
	link := buildZip(t, map[string]string{"link": "/etc/passwd"}, func(h *zip.FileHeader) { h.SetMode(os.ModeSymlink | 0o777) })
	if _, err := UnzipFiles(link); !errors.Is(err, ErrInvalid) {
		t.Errorf("symlink: %v", err)
	}
	if _, err := UnzipFiles([]byte("not a zip")); !errors.Is(err, ErrInvalid) {
		t.Errorf("not a zip: %v", err)
	}
	// zip 炸弹：声明很小（压缩后很小），实际解压超过 5 MB。
	bomb := buildZip(t, map[string]string{"SKILL.md": string(skillMD("a")), "zeros.bin": strings.Repeat("\x00", MaxTotalBytes+1)}, nil)
	if len(bomb) > 100<<10 {
		t.Fatalf("bomb should compress well, got %d bytes", len(bomb))
	}
	if _, err := UnzipFiles(bomb); !errors.Is(err, ErrTooLarge) {
		t.Errorf("bomb: %v", err)
	}
	// 头部声明的大小是假的：以实际读出的字节为准。
	lying := buildZip(t, map[string]string{"SKILL.md": string(skillMD("a")), "big.bin": strings.Repeat("\x00", MaxTotalBytes)}, func(h *zip.FileHeader) { h.UncompressedSize64 = 10 })
	if _, err := UnzipFiles(lying); !errors.Is(err, ErrTooLarge) && !errors.Is(err, ErrInvalid) {
		t.Errorf("lying header: %v", err)
	}
	// 条目数。
	tooMany := map[string]string{"SKILL.md": string(skillMD("a"))}
	for i := 0; i < 100; i++ {
		tooMany["f"+strings.Repeat("x", i%7)+string(rune('A'+i/26))+string(rune('a'+i%26))] = "x"
	}
	if _, err := UnzipFiles(buildZip(t, tooMany, nil)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("101 files: %v", err)
	}
	flood := map[string]string{}
	for i := 0; i < 250; i++ {
		flood["d"+strings.Repeat("a", i/26)+string(rune('a'+i%26))+"/"] = ""
	}
	if _, err := UnzipFiles(buildZip(t, flood, nil)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("entry flood: %v", err)
	}
}
