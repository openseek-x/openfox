package zohobugtracker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestReadOnlyResources(t *testing.T) {
	c, calls := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Zoho-oauthtoken test-token" {
			t.Error("read-only authorization")
		}
		switch r.URL.Path {
		case "/restapi/portals/":
			io.WriteString(w, `{"login_id":2060758,"portals":[{"id":2063927,"name":"portal","role":"admin","link":{"self":{"url":"https://evil.invalid"}}}]}`)
		case "/restapi/portal/2063927/users/":
			if r.URL.Query().Get("user_type") != "active" {
				t.Error("wrong user scope")
			}
			io.WriteString(w, `{"users":[{"id":"2060758","name":"Tester","email":"tester@example.test","active":true,"role":"admin"}]}`)
		case "/restapi/portal/2063927/projects/", "/restapi/portal/2063927/projects/170876000003152069/":
			io.WriteString(w, `{"projects":[{"id":170876000003152069,"id_string":"170876000003152069","name":"project","status":"active"}]}`)
		case "/restapi/portal/2063927/projects/170876000003152069/bugs/":
			if r.URL.Query().Get("statustype") != "open" || r.URL.Query().Get("range") != "100" || r.URL.Query().Get("index") != "1" {
				t.Error("pagination/partition")
			}
			io.WriteString(w, `{"bugs":[{"id":170876000001834047,"title":"<script>safe source</script>","status":{"id":170876000000065048,"type":"InProgress"},"severity":{"id":170876000000065005,"type":"Major"},"reporter_id":"2060758","reported_person":"Tester","closed":false,"created_time_long":1400012857000,"assignee_id":"UNVERIFIED","description":"UNVERIFIED","link":{"self":{"url":"https://evil.invalid"}}}]}`)
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	p, err := c.PortalsPage(ctx, "test-token", 1)
	if err != nil || p.LoginID != "2060758" || p.NextIndex != 0 {
		t.Fatal(p, err)
	}
	users, err := c.UsersPage(ctx, "test-token", "2063927", 1)
	if err != nil || len(users.Users) != 1 || !users.Users[0].Active {
		t.Fatal(users, err)
	}
	projects, err := c.ProjectsPage(ctx, "test-token", "2063927", 1)
	if err != nil || projects.Projects[0].ID != "170876000003152069" {
		t.Fatal(projects, err)
	}
	project, err := c.Project(ctx, "test-token", "2063927", "170876000003152069")
	if err != nil || project.Status != "active" {
		t.Fatal(project, err)
	}
	bugs, err := c.BugsPage(ctx, "test-token", "2063927", string(project.ID), "open", 1)
	if err != nil || len(bugs.Bugs) != 1 || bugs.Bugs[0].ID != "170876000001834047" || bugs.Bugs[0].Closed {
		t.Fatal(bugs, err)
	}
	b, _ := json.Marshal(bugs)
	if strings.Contains(string(b), "UNVERIFIED") || strings.Contains(string(b), "evil.invalid") {
		t.Fatal("unsupported fields leaked into normalized contract")
	}
	if calls.Load() != 5 {
		t.Fatal("resource links were followed")
	}
}
func TestPagesAndMalformedShapes(t *testing.T) {
	t.Run("next", func(t *testing.T) {
		c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
			items := []Portal{}
			for i := 0; i < PageSize; i++ {
				items = append(items, Portal{ID: ID(fmt.Sprint(i + 1)), Name: "test", Role: "admin"})
			}
			json.NewEncoder(w).Encode(PortalPage{LoginID: "1000", Portals: items})
		})
		p, err := c.PortalsPage(context.Background(), "token", 101)
		if err != nil || p.NextIndex != 201 {
			t.Fatal(p, err)
		}
	})
	for _, body := range []string{`{}`, `{"bugs":null}`, `{"bugs":[{"id":1,"title":"x","status":{"id":2,"type":"open"}}]}`, `{"bugs":[{"id":1,"title":"x","status":{"id":2,"type":"open"},"closed":true}]}`, `{"bugs":[{"id":1,"title":"x","status":{"id":2,"type":"open"},"closed":false},{"id":1,"title":"x","status":{"id":2,"type":"open"},"closed":false}]}`, `{"bugs":[{"id":1,"title":"x","status":{"id":2,"type":"open"},"closed":false,"reporter_id":"not-id"}]}`} {
		t.Run(body[:min(25, len(body))], func(t *testing.T) {
			c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
			_, err := c.BugsPage(context.Background(), "token", "1", "2", "open", 1)
			assertCode(t, err, CodeProtocol)
		})
	}
	t.Run("empty", func(t *testing.T) {
		c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"bugs":[]}`) })
		p, err := c.BugsPage(context.Background(), "token", "1", "2", "closed", 1)
		if err != nil || p.NextIndex != 0 || p.Bugs == nil {
			t.Fatal(p, err)
		}
	})
	t.Run("invalid-input-no-network", func(t *testing.T) {
		c, calls := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected network") })
		for _, id := range []string{"../1", "1?x", "1%2f2", ""} {
			_, err := c.UsersPage(context.Background(), "token", id, 1)
			assertCode(t, err, CodeInvalidRequest)
		}
		_, err := c.BugsPage(context.Background(), "token", "1", "2", "all", 1)
		assertCode(t, err, CodeInvalidRequest)
		_, err = c.PortalsPage(context.Background(), "token", 0)
		assertCode(t, err, CodeInvalidRequest)
		if calls.Load() != 0 {
			t.Fatal("invalid request sent")
		}
	})
}
