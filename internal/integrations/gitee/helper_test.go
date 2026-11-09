package gitee_test

import (
	"net/http"
	"testing"
)

// obtainCode 走一遍模拟 Gitee 的授权页，取回重定向里的授权码。
func obtainCode(t *testing.T, authorizeURL string) string {
	t.Helper()
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := plain.Get(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	location, err := response.Location()
	if err != nil || location.Query().Get("code") == "" {
		t.Fatalf("authorize = %d %v %v", response.StatusCode, location, err)
	}
	return location.Query().Get("code")
}
