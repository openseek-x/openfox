package jiraapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestCreateCommentAcknowledgment(t *testing.T) {
	for _, test := range []struct {
		response string
		ok       bool
	}{{`{"id":"11","body":"reply"}`, true}, {`{"body":"reply"}`, false}, {`{"id":"11","body":"different"}`, false}} {
		c := newTestClient(t, DeploymentServer, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" || r.URL.Path != "/jira/rest/api/2/issue/100/comment" || r.Header.Get("Authorization") != "Bearer tok" {
				t.Error("wrong request")
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["body"] != "reply" {
				t.Error(body)
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(test.response))
		})
		_, e := c.CreateComment(context.Background(), "100", "reply")
		if (e == nil) != test.ok {
			t.Fatal(e, test)
		}
	}
}

func TestCreateCommentDoesNotRetryUnknownDelivery(t *testing.T) {
	for _, mode := range []string{"lost response", "server error", "rate limit"} {
		t.Run(mode, func(t *testing.T) {
			var posts atomic.Int32
			client := newTestClient(t, DeploymentServer, func(writer http.ResponseWriter, request *http.Request) {
				posts.Add(1)
				switch mode {
				case "lost response":
					connection, _, err := writer.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = connection.Close()
				case "server error":
					writer.WriteHeader(http.StatusInternalServerError)
				case "rate limit":
					writer.Header().Set("Retry-After", "1")
					writer.WriteHeader(http.StatusTooManyRequests)
				}
			})
			remoteID, err := client.CreateComment(context.Background(), "100", "reply")
			if err == nil || remoteID != "" || posts.Load() != 1 {
				t.Fatalf("uncertain delivery: id=%q err=%v requests=%d", remoteID, err, posts.Load())
			}
		})
	}
}
