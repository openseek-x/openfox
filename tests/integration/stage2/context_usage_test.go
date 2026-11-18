package stage2

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestContextUsageDeviceHTTPPersistenceAndScope(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	executor := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	bystander := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	foreign := e.registerDevice(e.issueTokenIn(e.client(agOutsiderID), e.otherOrg, "").InstallToken)
	_, _, runID := e.devRunFixture(executor, nil)
	path := "/v1/device/organizations/" + e.org + "/runs/" + runID + "/events"
	makeEvent := func(sequence int, current any) map[string]any {
		return map[string]any{"id": agNewID(t), "generation": 1, "sequence": sequence, "type": "context_usage",
			"occurred_at": "2026-11-13T08:00:00Z", "safe_payload": map[string]any{"data": map[string]any{
				"schema": 1, "runtime": "codex", "requested_model": "gpt-5", "model": "gpt-5", "current_tokens": current, "window_tokens": 124518,
			}}}
	}
	second := makeEvent(2, 0)
	body := map[string]any{"events": []any{second}}
	expectCode(t, e.deviceCall(bystander, http.MethodPost, path, body), http.StatusForbidden, "")
	expectCode(t, e.deviceCall(foreign, http.MethodPost, path, body), http.StatusNotFound, "")
	expectCode(t, e.anonymous(http.MethodPost, path, "", body), http.StatusUnauthorized, "")
	expectCode(t, e.deviceCall(executor, http.MethodPost, path, body), http.StatusOK, "")
	first := makeEvent(1, 60000)
	firstBody := map[string]any{"events": []any{first}}
	var results [4]agResponse
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i] = e.deviceCall(executor, http.MethodPost, path, firstBody) }(i)
	}
	wg.Wait()
	for _, r := range results {
		expectCode(t, r, http.StatusOK, "")
		var ack struct {
			Highest int `json:"highest_contiguous"`
		}
		r.decode(t, &ack)
		if ack.Highest != 2 {
			t.Fatalf("ACK=%d", ack.Highest)
		}
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.run_events WHERE organization_id=$1::uuid AND run_id=$2::uuid`, e.org, runID)
	if count != 2 {
		t.Fatalf("duplicate writes=%d", count)
	}
	first["safe_payload"] = map[string]any{"data": map[string]any{"schema": 1}}
	expectCode(t, e.deviceCall(executor, http.MethodPost, path, firstBody), http.StatusConflict, "")
	stale := makeEvent(3, nil)
	stale["generation"] = 2
	expectCode(t, e.deviceCall(executor, http.MethodPost, path, map[string]any{"events": []any{stale}}), http.StatusBadRequest, "INVALID_REQUEST")
	readPath := "runs/" + runID + "/events"
	var page struct {
		Events []struct {
			Sequence int `json:"sequence"`
			Payload  struct {
				Data struct {
					Current *int64 `json:"current_tokens"`
					Window  *int64 `json:"window_tokens"`
				} `json:"data"`
			} `json:"safe_payload"`
		} `json:"events"`
		Cursor int64 `json:"next_cursor"`
	}
	owner.must(http.StatusOK, http.MethodGet, readPath+"?limit=1", nil, &page)
	if len(page.Events) != 1 || page.Events[0].Sequence != 2 || page.Events[0].Payload.Data.Current == nil || *page.Events[0].Payload.Data.Current != 0 || page.Cursor == 0 {
		t.Fatalf("first page=%+v", page)
	}
	owner.must(http.StatusOK, http.MethodGet, fmt.Sprintf("%s?limit=1&after=%d", readPath, page.Cursor), nil, &page)
	if len(page.Events) != 1 || page.Events[0].Sequence != 1 || *page.Events[0].Payload.Data.Current != 60000 {
		t.Fatalf("second page=%+v", page)
	}
	e.client(agMemberID).expectError(http.StatusNotFound, "", http.MethodGet, readPath, nil)
	e.client(agOutsiderID).expectError(http.StatusNotFound, "", http.MethodGet, readPath, nil)
	// Reconnect reads the durable same event through the actual SSE handler.
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.server.URL+"/v1/organizations/"+e.org+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := owner.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 {
		t.Fatal(stream.StatusCode)
	}
	scanner := bufio.NewScanner(stream.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var item struct {
			RunID string `json:"run_id"`
			Type  string `json:"type"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &item) == nil && item.RunID == runID && item.Type == "context_usage" {
			return
		}
	}
	t.Fatalf("missing persisted SSE event: %v", scanner.Err())
}
