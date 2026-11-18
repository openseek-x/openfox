package zentao

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"openfox/internal/platform/safehttp"
	"strings"
	"testing"
)

func TestPersonalCommentSessionIsolation(t *testing.T) {
	posts := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zt/index.php" || r.URL.Query().Get("t") != "json" || r.Method != "POST" {
			t.Error("wrong API request")
		}
		if strings.Contains(r.URL.RawQuery, "password") {
			t.Error("password leaked into URL")
		}
		_ = r.ParseForm()
		if r.URL.Query().Get("f") == "login" {
			if r.Form.Get("account") != "alice" || r.Form.Get("password") != "test-only" {
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "failed"})
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "zentaosid", Value: "alice-session", Path: "/zt/", Secure: true, HttpOnly: true})
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "user": map[string]string{"account": "alice"}})
			return
		}
		cookie, e := r.Cookie("zentaosid")
		if e != nil || cookie.Value != "alice-session" {
			t.Error("personal session not used")
		}
		if r.URL.Query().Get("objectType") != "bug" || r.URL.Query().Get("objectID") != "11" || r.Form.Get("comment") != "native reply" {
			t.Error("wrong comment target")
		}
		posts++
		_, _ = w.Write([]byte(`{"status":"success","data":82}`))
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	opts := safehttp.Options{RootCAs: pool, AllowAddr: func(ip net.IP) bool { return ip.IsLoopback() }}
	c, e := ConnectPersonal(context.Background(), Credentials{Site: srv.URL + "/zt", Account: "alice", Password: "test-only"}, opts)
	if e != nil {
		t.Fatal(e)
	}
	id, e := c.CreateComment(context.Background(), "bugs", "11", "native reply")
	if e != nil || id != "82" {
		t.Fatal(id, e)
	}
	if _, e = ConnectPersonal(context.Background(), Credentials{Site: srv.URL + "/zt", Account: "bob", Password: "wrong"}, opts); e == nil {
		t.Fatal("second user inherited session")
	}
	if _, e = c.CreateComment(context.Background(), "users", "11", "native reply"); e == nil {
		t.Fatal("unsupported object accepted")
	}
	if posts != 1 {
		t.Fatal(posts)
	}
}
