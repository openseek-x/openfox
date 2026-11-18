package integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gitee"
	"openfox/internal/integrations/github"
	"openfox/internal/integrations/gitlab"
	"testing"
)

func TestReviewCommentClients(t *testing.T) {
	for _, provider := range []string{"github", "gitlab", "gitee"} {
		t.Run(provider, func(t *testing.T) {
			mode := "found"
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				items := []map[string]any{}
				if mode == "found" && r.URL.Query().Get("page") == "2" {
					items = append(items, map[string]any{"id": 19, "body": "body\n" + CommentMarker("x")})
				} else if mode != "empty" {
					w.Header().Set("Link", `<https://example.test/next>; rel="next"`)
					w.Header().Set("X-Next-Page", "2")
					for range 100 {
						items = append(items, map[string]any{"id": 1, "body": "other"})
					}
				}
				json.NewEncoder(w).Encode(items)
			}))
			defer server.Close()
			var c any
			switch provider {
			case "github":
				c = githubAdapter{c: github.New("", "", server.URL, server.URL)}
			case "gitlab":
				c = gitlab.New(server.URL, "", "", "", gitlab.Options{AllowLocal: true})
			case "gitee":
				c = gitee.New("", "", server.URL, server.URL)
			}
			finder, ok := c.(gitapi.CommentFinder)
			if !ok {
				t.Fatal("缺少评论对账接口")
			}
			id, err := finder.FindIssueComment(context.Background(), "synthetic", "o", "r", "1", CommentMarker("x"))
			if err != nil || id != "19" || calls != 2 {
				t.Fatalf("分页对账: %q %v calls=%d", id, err, calls)
			}
			mode = "empty"
			id, err = finder.FindIssueComment(context.Background(), "synthetic", "o", "r", "1", CommentMarker("x"))
			if err != nil || id != "" {
				t.Fatal(id, err)
			}
			mode = "incomplete"
			calls = 0
			_, err = finder.FindIssueComment(context.Background(), "synthetic", "o", "r", "1", CommentMarker("x"))
			if err == nil || calls > 5 {
				t.Fatalf("不完整扫描被当成未找到: %v calls=%d", err, calls)
			}
		})
	}
}
