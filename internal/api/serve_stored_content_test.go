package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// OPE-19：产物与附件下载支持 HTTP Range，视频才能边下边播、随意拖动。

const rangeFixture = "0123456789"

// plainReader 只暴露 Read，模拟不可定位的存储对象（例如对象存储的流）。
type plainReader struct{ io.Reader }

func serveFixture(t *testing.T, file io.Reader, rangeHeader string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/download", nil)
	if rangeHeader != "" {
		request.Header.Set("Range", rangeHeader)
	}
	response := httptest.NewRecorder()
	response.Header().Set("Content-Type", "video/webm")
	// 调用方可能按完整大小预先设置了 Content-Length，这里故意设成错误值，验证由 ServeContent 按实际范围重算。
	response.Header().Set("Content-Length", "999")
	serveStoredContent(response, request, file)
	return response
}

func TestServeStoredContentRange(t *testing.T) {
	cases := []struct {
		name        string
		rangeHeader string
		wantStatus  int
		wantBody    string
		wantRange   string
		wantLength  string
	}{
		{"完整内容", "", http.StatusOK, rangeFixture, "", "10"},
		{"起止区间", "bytes=2-5", http.StatusPartialContent, "2345", "bytes 2-5/10", "4"},
		{"后缀区间", "bytes=-3", http.StatusPartialContent, "789", "bytes 7-9/10", "3"},
		{"开放区间", "bytes=8-", http.StatusPartialContent, "89", "bytes 8-9/10", "2"},
		{"越界返回 416", "bytes=20-30", http.StatusRequestedRangeNotSatisfiable, "", "bytes */10", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := serveFixture(t, strings.NewReader(rangeFixture), tc.rangeHeader)
			if response.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tc.wantStatus)
			}
			if got := response.Header().Get("Content-Range"); got != tc.wantRange {
				t.Fatalf("Content-Range = %q, want %q", got, tc.wantRange)
			}
			if tc.wantStatus >= 300 {
				return
			}
			if got := response.Header().Get("Accept-Ranges"); got != "bytes" {
				t.Fatalf("Accept-Ranges = %q, want bytes", got)
			}
			if got := response.Header().Get("Content-Length"); got != tc.wantLength {
				t.Fatalf("Content-Length = %q, want %q", got, tc.wantLength)
			}
			if got := response.Header().Get("Content-Type"); got != "video/webm" {
				t.Fatalf("Content-Type = %q, want video/webm", got)
			}
			if got := response.Body.String(); got != tc.wantBody {
				t.Fatalf("body = %q, want %q", got, tc.wantBody)
			}
		})
	}
}

// 本地存储交给 serveStoredContent 的是 *os.File，同样要支持 Range。
func TestServeStoredContentLocalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.webm")
	if err := os.WriteFile(path, []byte(rangeFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	response := serveFixture(t, file, "bytes=0-0")
	if response.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusPartialContent)
	}
	if got := response.Body.String(); got != "0" {
		t.Fatalf("body = %q, want %q", got, "0")
	}
	if got := response.Header().Get("Content-Range"); got != "bytes 0-0/10" {
		t.Fatalf("Content-Range = %q, want bytes 0-0/10", got)
	}
}

// 不可定位的对象无法分段：忽略 Range，整段写出，且不声明 Accept-Ranges。
func TestServeStoredContentNonSeekableIgnoresRange(t *testing.T) {
	response := serveFixture(t, plainReader{strings.NewReader(rangeFixture)}, "bytes=2-5")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Body.String(); got != rangeFixture {
		t.Fatalf("body = %q, want %q", got, rangeFixture)
	}
	if got := response.Header().Get("Content-Range"); got != "" {
		t.Fatalf("Content-Range = %q, want empty", got)
	}
	if got := response.Header().Get("Accept-Ranges"); got != "" {
		t.Fatalf("Accept-Ranges = %q, want empty", got)
	}
}
