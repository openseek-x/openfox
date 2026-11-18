package imports

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"openfox/internal/imports/jiraapi"
)

func TestSourceCommentsBoundedPagination(t *testing.T) {
	for _, mode := range []string{"two pages", "duplicate ID", "empty page", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/rest/api/2/issue/12/comment" {
					t.Error(r.URL.Path)
				}
				switch mode {
				case "empty page":
					fmt.Fprint(w, `{"comments":[],"total":1}`)
				case "oversize":
					fmt.Fprint(w, `{"comments":[],"total":2001}`)
				default:
					id := "1"
					if calls == 2 && mode == "two pages" {
						id = "2"
					}
					fmt.Fprintf(w, `{"comments":[{"id":%q,"body":"example","author":{"name":"qa"}}],"total":2}`, id)
				}
			}))
			defer srv.Close()
			client, e := jiraapi.New(jiraapi.Config{Deployment: "server", SiteURL: srv.URL, Auth: jiraapi.Auth{Kind: "pat", Secret: "fixture"}}, srv.Client())
			if e != nil {
				t.Fatal(e)
			}
			j := syncJob{jira: client}
			v, e := j.sourceComments(context.Background(), "12")
			if mode == "two pages" {
				if e != nil || len(v) != 2 || calls != 2 {
					t.Fatal(v, e, calls)
				}
			} else if e == nil {
				t.Fatal("invalid pagination accepted")
			}
		})
	}
}

func TestSourceCommentDatesAndIdentityKeys(t *testing.T) {
	for _, value := range []string{"2026-11-07 10:31:00", "2026-11-07T10:31:00+0800", "2026-11-07T10:31:00.000+0800"} {
		at, ok := ParseDate(value, defaultLocation)
		if !ok || at.UTC().Format(time.RFC3339) != "2026-11-07T02:31:00Z" {
			t.Fatal(value, at, ok)
		}
	}
	a := sourceCommentClientID("a", "12", "1")
	if a == sourceCommentClientID("b", "12", "1") || a == sourceCommentClientID("a", "13", "1") || a == sourceCommentClientID("a", "12", "2") {
		t.Fatal("source identity collision")
	}
}
