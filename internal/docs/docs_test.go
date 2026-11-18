package docs

import (
	"errors"
	"testing"
)

func TestCleanFileName(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"plain", "report.md", "report.md", false},
		{"unix path", "../../etc/report.md", "report.md", false},
		{"windows path", `C:\Users\me\notes.csv`, "notes.csv", false},
		{"trims spaces", "  a.md  ", "a.md", false},
		{"empty", "", "", true},
		{"spaces only", "   ", "", true},
		{"dot", ".", "", true},
		{"dot dot", "..", "", true},
		{"trailing separator leaves no name", "dir/", "", true},
		{"control character", "bad\nname.md", "", true},
		{"too long", repeatRune('a', 256), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CleanFileName(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("CleanFileName(%q) err = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("CleanFileName(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if tc.wantErr && !errors.Is(err, ErrFileName) {
				t.Fatalf("CleanFileName(%q) error = %v, want ErrFileName", tc.raw, err)
			}
		})
	}
}

func repeatRune(r rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}

func TestEscapeLike(t *testing.T) {
	cases := map[string]string{
		"plain":   "plain",
		"50%":     `50\%`,
		"a_b":     `a\_b`,
		`c:\dir`:  `c:\\dir`,
		"%_\\":    `\%\_\\`,
		"中文 100%": `中文 100\%`,
	}
	for input, want := range cases {
		if got := EscapeLike(input); got != want {
			t.Errorf("EscapeLike(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestUploadKind(t *testing.T) {
	cases := []struct {
		ext      string
		content  []byte
		wantKind string
		wantText bool
	}{
		{".md", []byte("# 标题"), KindDoc, true},
		{".MD", []byte("hello"), KindDoc, true},
		{".csv", []byte("a,b\n1,2"), KindSheet, true},
		{".md", []byte{0xff, 0xfe, 'x'}, KindDoc, false},
		{".md", make([]byte, MaxBodyBytes+1), KindDoc, false},
		{".png", []byte("hello"), "", false},
		{"", []byte("hello"), "", false},
	}
	for _, tc := range cases {
		kind := TextKind(tc.ext)
		if kind != tc.wantKind {
			t.Errorf("TextKind(%q) = %q, want %q", tc.ext, kind, tc.wantKind)
		}
		// 扩展名决定候选类型；正文必须是合法 UTF-8 且不超过 2 MB，否则按 file 处理。
		if isText := kind != "" && IsTextBody(tc.content); isText != tc.wantText {
			t.Errorf("ext %q with %d bytes: text = %v, want %v", tc.ext, len(tc.content), isText, tc.wantText)
		}
	}
}

func TestSplitExtension(t *testing.T) {
	cases := map[string][2]string{
		"report.md": {"report", ".md"},
		"a.b.csv":   {"a.b", ".csv"},
		".bashrc":   {".bashrc", ""},
		"README":    {"README", ""},
		"trailing.": {"trailing", "."},
	}
	for name, want := range cases {
		title, ext := SplitExtension(name)
		if title != want[0] || ext != want[1] {
			t.Errorf("SplitExtension(%q) = (%q, %q), want (%q, %q)", name, title, ext, want[0], want[1])
		}
	}
}

func TestNormalizeTitleAndSetName(t *testing.T) {
	if got, err := NormalizeSetName("  产品  "); err != nil || got != "产品" {
		t.Fatalf("NormalizeSetName trims = (%q, %v)", got, err)
	}
	if _, err := NormalizeSetName("   "); !errors.Is(err, ErrSetNameInvalid) {
		t.Fatalf("NormalizeSetName blank err = %v", err)
	}
	if _, err := NormalizeSetName(repeatRune('名', MaxSetNameRunes+1)); !errors.Is(err, ErrSetNameInvalid) {
		t.Fatalf("NormalizeSetName 65 runes err = %v", err)
	}
	if got, err := NormalizeSetName(repeatRune('名', MaxSetNameRunes)); err != nil || len([]rune(got)) != MaxSetNameRunes {
		t.Fatalf("NormalizeSetName 64 runes = (%d, %v)", len([]rune(got)), err)
	}
	if _, err := NormalizeTitle(repeatRune('x', MaxTitleRunes+1)); !errors.Is(err, ErrTitleInvalid) {
		t.Fatalf("NormalizeTitle 201 runes err = %v", err)
	}
}

func TestStoredExt(t *testing.T) {
	cases := map[string]string{
		".PNG":                         "png",
		"jpeg":                         "jpeg",
		"tar.gz":                       "",
		"":                             "",
		"中文":                           "",
		"a b":                          "",
		"averylongextension1234567890": "",
	}
	for input, want := range cases {
		if got := StoredExt(input); got != want {
			t.Errorf("StoredExt(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDownloadNameAndDisposition(t *testing.T) {
	cases := []struct {
		kind, title, ext, contentType, want string
	}{
		{KindDoc, "产品需求", "", "", "产品需求.md"},
		{KindSheet, "数据", "", "", "数据.csv"},
		{KindDoc, "", "", "", "未命名文档.md"},
		{KindFile, "photo", "png", "image/png", "photo.png"},
		{KindFile, "photo", "", "image/png", "photo.png"},
		{KindFile, "photo", "jpg", "image/jpeg", "photo.jpg"},
		{KindFile, "blob", "", "application/x-unknown-thing", "blob"},
	}
	for _, tc := range cases {
		if got := DownloadName(tc.kind, tc.title, tc.ext, tc.contentType); got != tc.want {
			t.Errorf("DownloadName(%q, %q, %q, %q) = %q, want %q", tc.kind, tc.title, tc.ext, tc.contentType, got, tc.want)
		}
	}
	got := ContentDisposition("attachment", "报告 1.md")
	want := `attachment; filename="__ 1.md"; filename*=UTF-8''%E6%8A%A5%E5%91%8A%201.md`
	if got != want {
		t.Fatalf("ContentDisposition = %q, want %q", got, want)
	}
	if got := ContentDisposition("attachment", `a"b\c.md`); got != `attachment; filename="a_b_c.md"; filename*=UTF-8''a%22b%5Cc.md` {
		t.Fatalf("ContentDisposition quotes = %q", got)
	}
}

func TestInlineTypes(t *testing.T) {
	for _, ct := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		if !IsInlineType(ct) {
			t.Errorf("IsInlineType(%q) = false, want true", ct)
		}
	}
	for _, ct := range []string{"image/svg+xml", "text/html; charset=utf-8", "text/xml; charset=utf-8", "application/pdf", ""} {
		if IsInlineType(ct) {
			t.Errorf("IsInlineType(%q) = true, want false", ct)
		}
	}
}
