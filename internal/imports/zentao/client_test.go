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

func TestZenTaoReadProductWithTemporaryCredentials(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /zentao/api.php/v1/tokens", func(w http.ResponseWriter, r *http.Request) {
		var v map[string]string
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil || v["account"] != "fixture" || v["password"] != "temporary" {
			t.Error("invalid login body")
		}
		_, _ = w.Write([]byte(`{"token":"test-token"}`))
	})
	for path, body := range map[string]string{"/products": `{"total":1,"products":[{"id":1,"name":"A"}]}`, "/projects": `{"total":0,"projects":[]}`, "/products/1/stories": `{"total":1,"stories":[{"id":2,"title":"Story","assignedTo":{"account":"owner"}}]}`, "/products/1/bugs": `{"total":1,"bugs":[{"id":2,"title":"Bug"}]}`} {
		mux.HandleFunc("GET /zentao/api.php/v1"+path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Token") != "test-token" || r.URL.Query().Get("page") != "1" || r.URL.Query().Get("limit") != "100" {
				t.Error("invalid scoped read")
			}
			_, _ = w.Write([]byte(body))
		})
	}
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	input := Credentials{Site: srv.URL + "/zentao/", Account: "fixture", Password: "temporary"}
	if _, err := Connect(context.Background(), input); err == nil {
		t.Fatal("default transport accepted loopback")
	}
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c, err := connect(context.Background(), input, safehttp.Options{RootCAs: pool, AllowAddr: func(ip net.IP) bool { return ip.IsLoopback() }})
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Projects(context.Background())
	if err != nil || len(p) != 1 || p[0].ID != "products/1" {
		t.Fatal(p, err)
	}
	rows, err := c.Rows(context.Background(), p[0].ID)
	if err != nil || len(rows) != 3 || rows[1][0] == rows[2][0] || !strings.HasSuffix(rows[1][0], "/stories/2") || rows[1][4] != "owner" {
		t.Fatal(rows, err)
	}
}
