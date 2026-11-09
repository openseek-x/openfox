package zentao

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCommentActionsRequireStableScopedIDs(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"id":1,"actions":[{"id":9,"actor":"QA","comment":"body"},{"id":10,"comment":""}]}`, true},
		{`{"id":2,"actions":[]}`, false},
		{`{"id":1,"actions":[{"id":9,"comment":"body"},{"id":9,"comment":"again"}]}`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/bugs/1" {
				t.Error(r.URL.Path)
			}
			fmt.Fprint(w, tc.body)
		}))
		c := Client{base: srv.URL, prefix: "site/", http: srv.Client()}
		v, e := c.Comments(context.Background(), "site/bugs/1")
		if (e == nil) != tc.valid || (tc.valid && len(v) != 1) {
			t.Fatal(v, e)
		}
		if _, e = c.Comments(context.Background(), "other/bugs/1"); e == nil {
			t.Fatal("foreign site accepted")
		}
		srv.Close()
	}
}
