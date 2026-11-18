package azuredevops

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestPushTruncatedCommitNeedsVerifiedFullMessage(t *testing.T) {
	for _, mode := range []string{"complete", "still_truncated", "wrong_sha", "oversized", "rate_after_detail"} {
		t.Run(mode, func(t *testing.T) {
			sha := strings.Repeat("a", 40)
			calls := 0
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if strings.HasSuffix(r.URL.Path, "/commits") {
					rows := []Commit{{ID: sha, Comment: "prefix", CommentTruncated: true}}
					if mode == "rate_after_detail" {
						rows = append(rows, Commit{ID: strings.Repeat("b", 40), CommentTruncated: true})
					}
					jsonReply(w, map[string]any{"value": rows})
					return
				}
				if r.URL.Path != repoPath("org", projectID, repoID)+"/commits/"+sha || r.URL.Query().Get("changeCount") != "0" {
					t.Error("unverified commit URL")
				}
				v := Commit{ID: sha, Comment: "prefix\nFix TASK-12"}
				switch mode {
				case "still_truncated":
					v.CommentTruncated = true
				case "wrong_sha":
					v.ID = strings.Repeat("b", 40)
				case "oversized":
					v.Comment = strings.Repeat("x", MaxCellBytes+1)
				case "rate_after_detail":
					w.Header().Set("Retry-After", "30")
				}
				jsonReply(w, v)
			})
			got, e := c.PushCommits(context.Background(), "token", "org", projectID, repoID, 15)
			if mode == "complete" {
				if e != nil || len(got) != 1 || !strings.Contains(got[0].Comment, "TASK-12") {
					t.Fatal(got, e)
				}
			} else {
				code := "invalid_response"
				if mode == "rate_after_detail" {
					code = "rate_limited"
				}
				errorCode(t, e, code, false)
				if got != nil {
					t.Fatal("partial result")
				}
			}
			if calls != 2 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}
