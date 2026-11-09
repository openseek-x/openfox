package executiondelivery

import "testing"

func TestCleanArtifactName(t *testing.T) {
	cases := map[string]string{
		"report.md":               "report.md",
		"  report.md  ":           "report.md",
		"../../etc/passwd":        "passwd",
		`C:\Users\me\secret.txt`:  "secret.txt",
		"dir/sub/":                "artifact.bin",
		"..":                      "artifact.bin",
		"":                        "artifact.bin",
		"a\x00b\nc.txt":           "a_b_c.txt",
		"bad\xffname.txt":         "bad_name.txt",
		"结果 报告.csv":               "结果 报告.csv",
		"/abs/path/to/image.png":  "image.png",
		"name-with-\u202eRTL.txt": "name-with-_RTL.txt",
	}
	for input, want := range cases {
		if got := CleanArtifactName(input); got != want {
			t.Errorf("CleanArtifactName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizeArtifactMIME(t *testing.T) {
	cases := map[string]string{
		"text/plain":                "text/plain",
		"Text/Markdown":             "text/markdown",
		"text/csv; charset=utf-8":   "text/csv",
		"image/png":                 "image/png",
		"image/jpeg":                "image/jpeg",
		"image/gif":                 "image/gif",
		"image/webp":                "image/webp",
		"image/svg+xml":             "application/octet-stream",
		"text/html":                 "application/octet-stream",
		"application/javascript":    "application/octet-stream",
		"application/pdf":           "application/octet-stream",
		"":                          "application/octet-stream",
		"application/octet-stream":  "application/octet-stream",
		"text/plain;charset=gbk":    "text/plain",
		"multipart/form-data; x=1":  "application/octet-stream",
		"application/xhtml+xml":     "application/octet-stream",
		"text/javascript":           "application/octet-stream",
		"image/png; something=else": "image/png",
	}
	for input, want := range cases {
		if got := NormalizeArtifactMIME(input); got != want {
			t.Errorf("NormalizeArtifactMIME(%q) = %q, want %q", input, got, want)
		}
	}
}
