package bitbucket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRawWebhookSignature(t *testing.T) {
	secret := strings.Repeat("a", 32)
	body := []byte(`{"ping":true}`)
	// Independently generated with Python's hmac/hashlib, not this implementation.
	signature := "sha256=5937f22d778140657ca06d86dfc2c78316a3cd2822ddd7f8ca6151904c0deab3"
	if !VerifySignature(secret, body, signature) {
		t.Fatal("known vector")
	}
	for _, bad := range []string{"", strings.Replace(signature, "sha256", "sha1", 1), signature + "0", "sha256=" + strings.Repeat("z", 64)} {
		if VerifySignature(secret, body, bad) {
			t.Fatal("accepted malformed signature")
		}
	}
	if VerifySignature(secret, append(body, ' '), signature) || VerifySignature(strings.Repeat("b", 32), body, signature) {
		t.Fatal("signature ignores raw body/key")
	}
}
func pushRef(n int, kind, name string) *PushRef {
	r := &PushRef{Type: kind, Name: name}
	r.Target.Hash = fmt.Sprintf("%040x", n)
	return r
}
func webhookFixture() map[string]any {
	return map[string]any{"repository": map[string]any{"uuid": repoUUID, "workspace": map[string]any{"uuid": fixtureUUID}}, "pullrequest": map[string]any{"id": 7}, "comment": map[string]any{"id": 9, "content": map[string]any{"raw": "must-not-persist"}}, "push": map[string]any{"changes": []PushChange{{Old: pushRef(1, "branch", "main"), New: pushRef(2, "branch", "main")}}}}
}
func TestWebhookRoutingStripsContentAndRejectsUnknownScope(t *testing.T) {
	for _, key := range WebhookEvents {
		body, _ := json.Marshal(webhookFixture())
		out, err := ParseEvent(key, body)
		if err != nil || out.RepositoryID != repoUUID || out.WorkspaceID != fixtureUUID {
			t.Fatalf("key=%s err=%v", key, err)
		}
		encoded, _ := json.Marshal(out)
		if strings.Contains(string(encoded), "must-not-persist") {
			t.Fatal("persisted comment content")
		}
		if key == "pullrequest:comment_deleted" && !out.CommentDeleted {
			t.Fatal("lost deletion state")
		}
	}
	for _, bad := range []string{"issue:created", "pullrequest:approved", "repo:fork", ""} {
		body, _ := json.Marshal(webhookFixture())
		if _, err := ParseEvent(bad, body); err == nil {
			t.Fatal("unapproved event")
		}
	}
	for _, field := range []string{"repository", "pullrequest", "comment"} {
		item := webhookFixture()
		delete(item, field)
		body, _ := json.Marshal(item)
		if _, err := ParseEvent("pullrequest:comment_created", body); err == nil {
			t.Fatalf("missing %s", field)
		}
	}
}
func TestPushNewBranchTipOnlyAndTagsDeletionSkipped(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/commit/"+fmt.Sprintf("%040x", 2)) {
			t.Error("new branch traversed history")
		}
		respond(w, commitFixture(2))
	})
	event := Event{Kind: "PUSH", WorkspaceID: fixtureUUID, RepositoryID: repoUUID, Changes: []PushChange{
		{New: pushRef(2, "branch", "new"), Created: true},
		{New: pushRef(2, "branch", "same-tip"), Created: true},
		{New: pushRef(3, "tag", "release"), Created: true},
		{Old: pushRef(4, "branch", "deleted"), Closed: true},
	}}
	out, err := c.PushCommits(context.Background(), "access-fixture", event)
	if err != nil || len(out) != 1 || calls.Load() != 1 {
		t.Fatalf("count=%d calls=%d err=%v", len(out), calls.Load(), err)
	}
}
func TestPushAggregateLimitAcrossBranchesAndDuplicates(t *testing.T) {
	for _, total := range []int{100, 101} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			var calls atomic.Int32
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				items := []any{}
				if n == 1 {
					for i := 1; i <= 60; i++ {
						items = append(items, commitFixture(i))
					}
				} else {
					for i := 50; i <= total; i++ {
						items = append(items, commitFixture(i))
					}
				}
				respond(w, map[string]any{"values": items})
			})
			event := Event{Kind: "PUSH", WorkspaceID: fixtureUUID, RepositoryID: repoUUID, Changes: []PushChange{{Old: pushRef(1, "branch", "a"), New: pushRef(60, "branch", "a")}, {Old: pushRef(49, "branch", "b"), New: pushRef(total, "branch", "b"), Forced: true}}}
			out, err := c.PushCommits(context.Background(), "access-fixture", event)
			if total == 100 {
				if err != nil || len(out) != 100 {
					t.Fatalf("count=%d err=%v", len(out), err)
				}
			} else if !errors.Is(err, ErrCommitLimit) || out != nil {
				t.Fatalf("accepted aggregate101: %v", err)
			}
			if calls.Load() != 2 {
				t.Fatalf("requests=%d", calls.Load())
			}
		})
	}
}
func TestPushInvalidChangesFailBeforeHTTP(t *testing.T) {
	c, tr := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") })
	for _, changes := range [][]PushChange{nil, make([]PushChange, 11), {{}}, {{New: pushRef(1, "branch", "new")}}, {{Old: pushRef(1, "branch", "a"), New: pushRef(2, "branch", "b")}}} {
		_, err := c.PushCommits(context.Background(), "access-fixture", Event{Kind: "PUSH", WorkspaceID: fixtureUUID, RepositoryID: repoUUID, Changes: changes})
		if err == nil {
			t.Fatal("accepted malformed push")
		}
	}
	if tr.calls.Load() != 0 {
		t.Fatal("invalid changes performed HTTP")
	}
}
