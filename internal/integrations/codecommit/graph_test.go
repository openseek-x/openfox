package codecommit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type graphFixture struct {
	mu    sync.Mutex
	reads map[string]int
	calls int
}

func graphClient(t *testing.T, nodes map[string]Commit, mode string) (*Client, *graphFixture) {
	f := &graphFixture{reads: map[string]int{}}
	c, _ := tlsClient(t, "us-east-1", func(w http.ResponseWriter, r *http.Request, b []byte) {
		var in struct {
			Repository string   `json:"repositoryName"`
			ID         string   `json:"commitId"`
			IDs        []string `json:"commitIds"`
		}
		if json.Unmarshal(b, &in) != nil || in.Repository != "Repo" {
			t.Error("bad graph request")
		}
		action := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "CodeCommit_20150413.")
		ids := in.IDs
		if action == "GetCommit" {
			ids = []string{in.ID}
		} else if action != "BatchGetCommits" {
			t.Error("unexpected graph operation")
		}
		if len(ids) == 0 || len(ids) > 100 {
			t.Error("batch size")
		}
		f.mu.Lock()
		f.calls++
		for _, id := range ids {
			f.reads[id]++
		}
		f.mu.Unlock()
		if mode == "slow" {
			<-r.Context().Done()
			return
		}
		items := []Commit{}
		for _, id := range ids {
			node, ok := nodes[id]
			if !ok {
				w.WriteHeader(400)
				writeJSON(w, map[string]any{"__type": "CommitDoesNotExistException"})
				return
			}
			items = append(items, node)
		}
		if action == "GetCommit" {
			writeJSON(w, map[string]any{"commit": items[0]})
			return
		}
		switch mode {
		case "item-error":
			writeJSON(w, map[string]any{"commits": items, "errors": []any{map[string]any{"commitId": ids[0], "errorCode": "CommitDoesNotExistException"}}})
			return
		case "omission":
			items = items[:len(items)-1]
		case "duplicate":
			if len(items) > 1 {
				items[1] = items[0]
			} else {
				items = append(items, items[0])
			}
		case "extra":
			items = append(items, commit(999888))
		case "nil-parents":
			items[0].Parents = nil
		}
		writeJSON(w, map[string]any{"commits": items, "errors": []any{}})
	})
	return c, f
}
func graphMap(nodes ...Commit) map[string]Commit {
	out := map[string]Commit{}
	for _, n := range nodes {
		out[n.ID] = n
	}
	return out
}
func idsOf(nodes []Commit) []string {
	out := []string{}
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	sort.Strings(out)
	return out
}
func assertGraph(t *testing.T, nodes map[string]Commit, old, new int, want []int, wantErr error, mode string) *graphFixture {
	t.Helper()
	c, f := graphClient(t, nodes, mode)
	oldSHA := ""
	if old != 0 {
		oldSHA = sha(old)
	}
	got, e := c.PushCommits(context.Background(), fixtureCredentials, "Repo", oldSHA, sha(new))
	if wantErr != nil {
		if !errors.Is(e, wantErr) || got != nil {
			t.Fatalf("got %d nodes / %v, want nil / %v", len(got), e, wantErr)
		}
	} else {
		if e != nil {
			t.Fatal(e)
		}
		expected := []string{}
		for _, id := range want {
			expected = append(expected, sha(id))
		}
		sort.Strings(expected)
		if !reflect.DeepEqual(idsOf(got), expected) {
			t.Fatalf("got %v want %v", idsOf(got), expected)
		}
	}
	return f
}
func TestPushLinearBoundary(t *testing.T) {
	for _, n := range []int{100, 101} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			nodes := graphMap(commit(1))
			want := []int{}
			for i := 2; i <= n+1; i++ {
				nodes[sha(i)] = commit(i, i-1)
				want = append(want, i)
			}
			var expected error
			if n == 101 {
				expected = ErrCommitLimit
			}
			f := assertGraph(t, nodes, 1, n+1, want, expected, "")
			if f.reads[sha(1)] != 0 || f.calls != n {
				t.Fatalf("single-parent proof calls=%d old=%d", f.calls, f.reads[sha(1)])
			}
		})
	}
}
func TestPushCompleteDAG(t *testing.T) {
	t.Run("old-side-branch-excluded", func(t *testing.T) {
		nodes := graphMap(commit(1), commit(2, 1), commit(3, 1), commit(4, 2, 3), commit(5, 3), commit(6, 4, 5))
		f := assertGraph(t, nodes, 4, 6, []int{5, 6}, nil, "")
		for id, n := range f.reads {
			if n != 1 {
				t.Fatalf("duplicate fetch %s=%d", id, n)
			}
		}
		if f.reads[sha(3)] != 1 {
			t.Fatal("did not prove old side branch")
		}
	})
	t.Run("shared-new-parent", func(t *testing.T) {
		nodes := graphMap(commit(1), commit(2, 1), commit(3, 2), commit(4, 2), commit(5, 3, 4))
		f := assertGraph(t, nodes, 1, 5, []int{2, 3, 4, 5}, nil, "")
		if f.reads[sha(2)] != 1 {
			t.Fatal("shared ancestor fetched repeatedly")
		}
	})
	t.Run("disjoint-force-push", func(t *testing.T) {
		nodes := graphMap(commit(1), commit(2, 1), commit(10), commit(11, 10))
		assertGraph(t, nodes, 2, 11, []int{10, 11}, nil, "")
	})
	t.Run("rewind-is-empty", func(t *testing.T) {
		nodes := graphMap(commit(1), commit(2, 1), commit(3, 2))
		assertGraph(t, nodes, 3, 1, []int{}, nil, "")
	})
	t.Run("new-branch-tip-only", func(t *testing.T) {
		f := assertGraph(t, graphMap(commit(9, 8)), 0, 9, []int{9}, nil, "")
		if f.calls != 1 {
			t.Fatal("walked new branch history")
		}
	})
	t.Run("same-tip-zero-requests", func(t *testing.T) {
		f := assertGraph(t, nil, 1, 1, []int{}, nil, "")
		if f.calls != 0 {
			t.Fatal("same tip requested network")
		}
	})
}
func TestPushFailsWholeBatch(t *testing.T) {
	for _, mode := range []string{"item-error", "omission", "duplicate", "extra", "nil-parents"} {
		t.Run(mode, func(t *testing.T) {
			assertGraph(t, graphMap(commit(1), commit(2, 1), commit(3, 1), commit(4, 2, 3)), 1, 4, nil, ErrIncomplete, mode)
		})
	}
	t.Run("missing-old-parent", func(t *testing.T) {
		assertGraph(t, graphMap(commit(2, 1), commit(3), commit(4, 2, 3)), 2, 4, nil, ErrNotFound, "")
	})
	t.Run("missing-new-parent", func(t *testing.T) { assertGraph(t, graphMap(commit(1), commit(4, 2, 3)), 1, 4, nil, ErrNotFound, "") })
	t.Run("linear-cycle", func(t *testing.T) {
		assertGraph(t, graphMap(commit(1), commit(2, 3), commit(3, 2)), 1, 3, nil, ErrGraphInvalid, "")
	})
	t.Run("old-DAG-cycle", func(t *testing.T) {
		assertGraph(t, graphMap(commit(1, 2), commit(2, 1), commit(3), commit(4, 1, 3)), 1, 4, nil, ErrGraphInvalid, "")
	})
	t.Run("new-DAG-cycle", func(t *testing.T) {
		assertGraph(t, graphMap(commit(1), commit(2, 3), commit(3, 2), commit(4, 1, 3)), 1, 4, nil, ErrGraphInvalid, "")
	})
	t.Run("self-cycle", func(t *testing.T) {
		node := commit(2)
		node.Parents = []string{sha(2)}
		assertGraph(t, graphMap(commit(1), node), 1, 2, nil, ErrIncomplete, "")
	})
	t.Run("merge-101", func(t *testing.T) {
		nodes := graphMap(commit(1))
		parents := []int{}
		for i := 2; i <= 102; i++ {
			nodes[sha(i)] = commit(i, 1)
			parents = append(parents, i)
		}
		nodes[sha(103)] = commit(103, parents...)
		assertGraph(t, nodes, 1, 103, nil, ErrCommitLimit, "")
	})
}
func TestPushObjectTimeAndByteBudgets(t *testing.T) {
	t.Run("exact-2000", func(t *testing.T) {
		nodes := map[string]Commit{}
		parents := []int{}
		for i := 1; i <= 1997; i++ {
			nodes[sha(i)] = commit(i)
			parents = append(parents, i)
		}
		nodes[sha(1998)] = commit(1998, parents...)
		nodes[sha(1999)] = commit(1999)
		nodes[sha(2000)] = commit(2000, 1998, 1999)
		f := assertGraph(t, nodes, 1998, 2000, []int{1999, 2000}, nil, "")
		if len(f.reads) != 2000 {
			t.Fatalf("objects=%d", len(f.reads))
		}
	})
	t.Run("old-graph-budget", func(t *testing.T) {
		nodes := map[string]Commit{}
		parents := []int{}
		for i := 1; i <= 1999; i++ {
			nodes[sha(i)] = commit(i)
			parents = append(parents, i)
		}
		nodes[sha(2000)] = commit(2000, parents...)
		nodes[sha(2001)] = commit(2001, 2000, 1)
		f := assertGraph(t, nodes, 2000, 2001, nil, ErrGraphBudget, "")
		if len(f.reads) > 2000 {
			t.Fatal("object bound exceeded")
		}
	})
	t.Run("aggregate-bytes", func(t *testing.T) {
		nodes := graphMap(commit(1), commit(2, 1), commit(3, 2), commit(4, 3))
		for _, id := range []int{2, 3, 4} {
			n := nodes[sha(id)]
			n.Message = strings.Repeat("x", 3<<20)
			nodes[sha(id)] = n
		}
		assertGraph(t, nodes, 1, 4, nil, ErrGraphBudget, "")
	})
	t.Run("parent-deadline", func(t *testing.T) {
		c, _ := graphClient(t, graphMap(commit(2, 1)), "slow")
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancel()
		start := time.Now()
		nodes, e := c.PushCommits(ctx, fixtureCredentials, "Repo", sha(1), sha(2))
		if !errors.Is(e, context.DeadlineExceeded) || nodes != nil || time.Since(start) > time.Second {
			t.Fatalf("nodes=%d err=%v", len(nodes), e)
		}
	})
}
