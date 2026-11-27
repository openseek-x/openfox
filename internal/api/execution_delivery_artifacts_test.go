package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"openfox/internal/executiondelivery"
	"openfox/internal/identity"
)

// 阶段 10d：会话产物列表与设备上传接口已注册（未认证 401，而不是 404 / 405）。
func TestNewRegistersStage10dArtifactRoutes(t *testing.T) {
	handler := New(identity.Service{}, nil, false)
	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/organizations/00000000-0000-0000-0000-0000000000a1/conversations/00000000-0000-0000-0000-0000000000c1/artifacts"},
		{http.MethodPost, "/v1/device/organizations/00000000-0000-0000-0000-0000000000a1/runs/00000000-0000-0000-0000-0000000000d1/artifacts"},
	}
	for _, test := range cases {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusUnauthorized, response.Body.String())
			}
		})
	}
}

// 下载响应头：任何类型都按附件下载、nosniff、CSP sandbox、不缓存；文件名按 RFC 2231 编码。
func TestArtifactDownloadHeaders(t *testing.T) {
	for _, artifact := range []executiondelivery.Artifact{
		{Name: "report.md", MIMEType: "text/markdown", SizeBytes: 12},
		{Name: "chart.png", MIMEType: "image/png", SizeBytes: 3},
		{Name: "page.html", MIMEType: "application/octet-stream", SizeBytes: 7},
		{Name: "结果.csv", MIMEType: "text/csv", SizeBytes: 9},
	} {
		header := http.Header{}
		setArtifactDownloadHeaders(header, artifact)
		if got := header.Get("Content-Type"); got != artifact.MIMEType {
			t.Errorf("%s Content-Type = %q", artifact.Name, got)
		}
		if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s nosniff = %q", artifact.Name, got)
		}
		if got := header.Get("Content-Security-Policy"); got != "sandbox" {
			t.Errorf("%s CSP = %q", artifact.Name, got)
		}
		if got := header.Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("%s Cache-Control = %q", artifact.Name, got)
		}
		disposition := header.Get("Content-Disposition")
		if len(disposition) < len("attachment") || disposition[:len("attachment")] != "attachment" {
			t.Errorf("%s Content-Disposition = %q", artifact.Name, disposition)
		}
		if got := header.Get("Content-Length"); got == "" {
			t.Errorf("%s Content-Length missing", artifact.Name)
		}
	}
	header := http.Header{}
	setArtifactDownloadHeaders(header, executiondelivery.Artifact{Name: "结果.csv", MIMEType: "text/csv"})
	if got := header.Get("Content-Disposition"); got != "attachment; filename*=utf-8''%E7%BB%93%E6%9E%9C.csv" {
		t.Errorf("unicode Content-Disposition = %q", got)
	}
}

// 视频产物上传：文件头必须与声明的视频类型一致；通过时完整保留原始字节。
func TestVerifiedVideoReader(t *testing.T) {
	webm := append([]byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F, 0x42, 0x86, 0x81, 0x01, 0x42, 0xF7, 0x81, 0x01, 0x42, 0xF2, 0x81, 0x04,
		0x42, 0xF3, 0x81, 0x08, 0x42, 0x82, 0x84, 'w', 'e', 'b', 'm'}, bytes.Repeat([]byte{0x55}, 2048)...)
	mp4 := []byte{0, 0, 0, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0, 'i', 's', 'o', 'm', 'i', 's', 'o', '2',
		'a', 'v', 'c', '1', 'm', 'p', '4', '1'}
	cases := []struct {
		name    string
		content []byte
		mime    string
		ok      bool
	}{
		{"webm", webm, "video/webm", true},
		{"short mp4", mp4, "video/mp4", true},
		{"declared mp4 but webm", webm, "video/mp4", false},
		{"html disguised", []byte("<html><script>alert(1)</script>"), "video/webm", false},
		{"empty", nil, "video/webm", false},
	}
	for _, tc := range cases {
		reader, ok := verifiedVideoReader(bytes.NewReader(tc.content), tc.mime)
		if ok != tc.ok {
			t.Fatalf("%s: ok = %v, want %v", tc.name, ok, tc.ok)
		}
		if !ok {
			continue
		}
		got, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(got, tc.content) {
			t.Fatalf("%s: content changed (%d bytes, err %v)", tc.name, len(got), err)
		}
	}
}
