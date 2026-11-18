package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"openfox/internal/integrations"
	"strings"
	"testing"
	"time"
)

func TestBitbucketWebhookHTTPBodyBoundaries(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	h.registerBitbucketRepositoryRoutes(mux)
	for _, tc := range []struct {
		name, media, body string
		want              int
	}{{"media", "text/plain", "{}", 415}, {"over_limit", "application/json", strings.Repeat("x", integrations.MaxBitbucketWebhookBytes+1), 413}, {"valid_to_database", "application/json", "{}", 503}} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/hooks/git/bitbucket/"+strings.Repeat("t", 43), strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.media)
			out := httptest.NewRecorder()
			mux.ServeHTTP(out, req)
			if out.Code != tc.want {
				t.Fatalf("status=%d body=%s", out.Code, out.Body)
			}
		})
	}
}
func TestBitbucketWebhookSlowBodyCloseKeepsDeadline(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	h.registerBitbucketRepositoryRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	conn, e := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	_, e = fmt.Fprintf(conn, "POST /v1/hooks/git/bitbucket/%s HTTP/1.1\r\nHost: fixture\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{", strings.Repeat("t", 43))
	if e != nil {
		t.Fatal(e)
	}
	response, e := http.ReadResponse(bufio.NewReader(conn), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	if response.StatusCode != 400 || time.Since(start) > 4*time.Second {
		t.Fatalf("status=%d elapsed=%s", response.StatusCode, time.Since(start))
	}
}
