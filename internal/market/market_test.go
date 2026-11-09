package market

import (
	"strings"
	"testing"

	"openfox/internal/skills"
)

func TestCursorRoundTrip(t *testing.T) {
	want := cursorPayload{Sort: "default", A: 1, B: -500, T: "名称", ID: "skillhub.a.b"}
	got, err := DecodeCursor(EncodeCursor(want))
	if err != nil || got != want {
		t.Fatalf("got %+v err %v", got, err)
	}
	for _, bad := range []string{"", "!!!", "e30", EncodeCursor(cursorPayload{Sort: "x"})} {
		if _, err := DecodeCursor(bad); err != ErrInvalidCursor {
			t.Fatalf("DecodeCursor(%q) err = %v", bad, err)
		}
	}
}

func TestSearchText(t *testing.T) {
	text := SearchText(Text{"zh-Hans": "PDF 工具", "en": "PDF Tools"}, Text{"en": "Work With Files"}, []Text{{"en": "Sample"}}, "Extra")
	for _, part := range []string{"pdf 工具", "pdf tools", "work with files", "sample", "extra"} {
		if !strings.Contains(text, part) {
			t.Fatalf("search text %q lacks %q", text, part)
		}
	}
	if text != strings.ToLower(text) {
		t.Fatalf("search text must be lower case: %q", text)
	}
	long := SearchText(Text{"en": strings.Repeat("字", MaxSearchRunes+10)}, nil, nil)
	if n := len([]rune(long)); n != MaxSearchRunes {
		t.Fatalf("length = %d", n)
	}
}

func TestIsTextAndPreview(t *testing.T) {
	if !IsText([]byte("hello 世界")) || !IsText(nil) {
		t.Fatal("plain text must be text")
	}
	if IsText([]byte("a\x00b")) || IsText([]byte{0xff, 0xfe, 0x41}) {
		t.Fatal("NUL or invalid UTF-8 must be binary")
	}
	// 8 KB 边界落在多字节字符中间时仍判为文本。
	boundary := strings.Repeat("a", sniffBytes-1) + "世界"
	if !IsText([]byte(boundary)) {
		t.Fatal("a multi-byte character cut by the 8 KB window must not make the file binary")
	}
	// NUL 在 8 KB 之后不影响判断。
	if !IsText([]byte(strings.Repeat("a", sniffBytes+10) + "\x00")) {
		t.Fatal("only the first 8 KB is inspected")
	}

	small, truncated := Preview([]byte("abc"))
	if small != "abc" || truncated {
		t.Fatalf("small = %q %v", small, truncated)
	}
	big := []byte(strings.Repeat("a", PreviewBytes-1) + "世界")
	text, truncated := Preview(big)
	if !truncated || len(text) != PreviewBytes-1 {
		t.Fatalf("truncated = %v len = %d", truncated, len(text))
	}
	exact, truncated := Preview([]byte(strings.Repeat("b", PreviewBytes)))
	if truncated || len(exact) != PreviewBytes {
		t.Fatal("exactly 1 MB is not truncated")
	}
}

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"skillhub.ake.dev-god":                   "dev-god",
		"skillhub.user_1a.Wps_X":                 "wps-x",
		"plain":                                  "plain",
		"skillhub.x.-lead":                       "lead",
		"skillhub.x.___":                         "skill",
		"skillhub.x." + strings.Repeat("a", 100): strings.Repeat("a", 64),
	}
	for input, want := range cases {
		got := NormalizeName(input)
		if got != want || !skills.ValidName(got) {
			t.Errorf("NormalizeName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLoadDirSample(t *testing.T) {
	data, err := LoadDir("testdata/sample")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]importSkill{}
	for _, skill := range data.Skills {
		ids[skill.ID] = skill
	}
	for _, id := range []string{"skillhub.alpha.pdf-tools", "skillhub.beta.notes", "skillhub.gamma.csvtool"} {
		if _, ok := ids[id]; !ok {
			t.Fatalf("%s should be importable: skipped = %+v", id, data.SkippedSkills)
		}
	}
	reasons := map[string]string{}
	for _, skip := range data.SkippedSkills {
		reasons[skip.ID] = skip.Reason
	}
	want := map[string]string{
		"skillhub.bad.missing": ReasonFileContentMiss, "skillhub.bad.digest": ReasonFileDigestMismach,
		"skillhub.bad.nomd": ReasonNoSkillMD, "skillhub.ghost.absent": ReasonDetailMissing,
	}
	if len(reasons) != len(want) {
		t.Fatalf("skipped = %v", reasons)
	}
	for id, reason := range want {
		if reasons[id] != reason {
			t.Errorf("%s skipped with %q, want %q", id, reasons[id], reason)
		}
	}

	alpha := ids["skillhub.alpha.pdf-tools"]
	if alpha.Version != "1.0.0" || len(alpha.Files) != 3 || alpha.Downloads != 500 || !alpha.Featured || alpha.Publisher.Name != "Sample Org" {
		t.Fatalf("alpha = %+v", alpha)
	}
	if alpha.Icon == nil || !strings.HasPrefix(*alpha.Icon, "https://") {
		t.Fatalf("icon = %v", alpha.Icon)
	}
	paths := []string{}
	for _, file := range alpha.Files {
		paths = append(paths, file.Path)
	}
	if strings.Join(paths, ",") != "SKILL.md,data/blob.bin,scripts/run.py" {
		t.Fatalf("paths = %v", paths)
	}
	for _, part := range []string{"pdf 工具", "pdf tools", "work with pdf files", "示例"} {
		if !strings.Contains(alpha.SearchText, part) {
			t.Errorf("search text %q lacks %q", alpha.SearchText, part)
		}
	}

	if len(data.Templates) != 2 || len(data.SkippedTemplate) != 0 || len(data.SelectedIDs) != 7 {
		t.Fatalf("templates = %d skipped = %v selected = %d", len(data.Templates), data.SkippedTemplate, len(data.SelectedIDs))
	}
	template := data.Templates[0]
	if template.ID != "skillhub.tpl-writer" || len(template.SkillRefs) != 4 || template.Instructions["instructions.zh-Hans.md"] != "你是写手。" ||
		template.PublishedAt == nil || template.Position != 1 || template.Origin != "skillhub" {
		t.Fatalf("template = %+v", template)
	}
}

func TestSpacePackage(t *testing.T) {
	data, err := LoadDir("testdata/sample")
	if err != nil {
		t.Fatal(err)
	}
	read := func(skill importSkill) (Source, []skills.File) {
		var files []skills.File
		for _, file := range skill.Files {
			content, err := readAll(file)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, skills.File{Path: file.Path, Content: content})
		}
		return Source{ID: skill.ID, Version: skill.Version, Description: skill.Description}, files
	}
	for _, skill := range data.Skills {
		src, files := read(skill)
		pkg := SpacePackage(src, files)
		switch skill.ID {
		case "skillhub.alpha.pdf-tools":
			if pkg.Name != "pdf-tools" || pkg.Description != "Work with PDF files" || len(pkg.Files) != 3 {
				t.Errorf("alpha = %+v", pkg)
			}
		case "skillhub.beta.notes":
			// frontmatter 的 name 不合规：由市场 ID 末段规范化，描述取市场目录。
			if pkg.Name != "notes" || pkg.Description != "Take notes quickly" {
				t.Errorf("beta = %+v", pkg)
			}
		}
		if !skills.ValidName(pkg.Name) {
			t.Errorf("%s: invalid name %q", skill.ID, pkg.Name)
		}
	}
}
