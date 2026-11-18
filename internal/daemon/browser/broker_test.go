package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	scope "openfox/internal/browser"
	"openfox/internal/runtime"
)

const org = "10000000-0000-4000-8000-000000000001"
const owner = "10000000-0000-4000-8000-000000000002"
const device = "10000000-0000-4000-8000-000000000003"
const conversation = "10000000-0000-4000-8000-000000000004"
const bindingID = "10000000-0000-4000-8000-000000000005"
const hostID = "10000000-0000-4000-8000-000000000006"
const runID = "10000000-0000-4000-8000-000000000007"
const messageID = "10000000-0000-4000-8000-000000000008"

type fakeCenter struct {
	binding  scope.Binding
	reject   atomic.Bool
	readonly atomic.Bool
	calls    atomic.Int32
}

func (c *fakeCenter) ClaimBrowser(context.Context, string, string, string, scope.Identity) (scope.Binding, error) {
	return c.binding, nil
}
func (c *fakeCenter) HeartbeatBrowser(context.Context, string, string, scope.Identity) (scope.Binding, error) {
	if c.reject.Load() {
		return scope.Binding{}, errors.New("offline")
	}
	return c.binding, nil
}
func (c *fakeCenter) AuthorizeBrowser(_ context.Context, _ string, id string, in scope.AuthorizationInput) (scope.Authorization, error) {
	c.calls.Add(1)
	if c.reject.Load() {
		return scope.Authorization{}, errors.New("offline")
	}
	if in.ClientMessageID != "" && in.ClientMessageID != messageID {
		return scope.Authorization{}, errors.New("wrong message")
	}
	return scope.Authorization{Binding: c.binding, RunID: id, Generation: in.Generation, ReadOnly: c.readonly.Load()}, nil
}

type fakeHost struct {
	calls   atomic.Int32
	closed  atomic.Bool
	execute func(context.Context, Request) runtime.ToolResult
}

func (h *fakeHost) Execute(ctx context.Context, request Request) runtime.ToolResult {
	h.calls.Add(1)
	if h.execute != nil {
		return h.execute(ctx, request)
	}
	return runtime.TextResult("completed", true)
}
func (h *fakeHost) Close() { h.closed.Store(true) }

func fixture(t *testing.T) (*Broker, *fakeCenter, *fakeHost, Run) {
	t.Helper()
	c := &fakeCenter{binding: scope.Binding{ID: bindingID, OrganizationID: org, ConversationID: conversation, OwnerUserID: owner, DeviceID: device, HostInstanceID: hostID, HostEpoch: 1, Revision: 1, LeaseExpiresAt: time.Now().Add(time.Minute)}}
	h := &fakeHost{}
	b := New(c, device)
	_, err := b.Attach(context.Background(), org, bindingID, "claim", scope.Identity{HostInstanceID: hostID, HostEpoch: 1, Revision: 1}, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b, c, h, Run{OrganizationID: org, ConversationID: conversation, ActorID: owner, ID: runID, Generation: 1}
}
func allow(t *testing.T, b *Broker, h Host, epoch int64) {
	t.Helper()
	if err := b.SetGrant(bindingID, h, Grant{RunID: runID, Generation: 1, Epoch: epoch, Origins: []string{"https://example.test"}}); err != nil {
		t.Fatal(err)
	}
}
func act(b *Broker, run Run, id string) runtime.ToolResult {
	return b.Call(context.Background(), run, "openfox_browser_act", id, json.RawMessage(`{"action":"click","tab_id":"one","ref":"one"}`))
}

func TestNoGrantWrongScopeReadOnlyAndCenterFailure(t *testing.T) {
	b, c, h, run := fixture(t)
	if result := act(b, run, "no-grant"); result.OK || result.Text != "CONTROL_REVOKED" {
		t.Fatal(result)
	}
	allow(t, b, h, 1)
	for _, other := range []Run{
		{OrganizationID: owner, ConversationID: conversation, ActorID: owner, ID: runID, Generation: 1},
		{OrganizationID: org, ConversationID: owner, ActorID: owner, ID: runID, Generation: 1},
		{OrganizationID: org, ConversationID: conversation, ActorID: device, ID: runID, Generation: 1},
		{OrganizationID: org, ConversationID: conversation, ActorID: owner, ID: runID, Generation: 2},
	} {
		if result := act(b, other, "wrong"); result.OK {
			t.Fatal("foreign scope accepted")
		}
	}
	c.readonly.Store(true)
	if result := act(b, run, "read-only"); result.OK {
		t.Fatal("read-only mutation accepted")
	}
	if result := b.Call(context.Background(), run, "openfox_browser_observe", "read", json.RawMessage(`{"action":"snapshot"}`)); !result.OK {
		t.Fatal(result)
	}
	c.reject.Store(true)
	if result := act(b, run, "offline"); result.OK {
		t.Fatal("center failure allowed action")
	}
	if h.calls.Load() != 1 {
		t.Fatalf("unexpected host calls: %d", h.calls.Load())
	}
}

func TestMutationDeduplicationPayloadConflictAndTakeover(t *testing.T) {
	b, _, h, run := fixture(t)
	allow(t, b, h, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	h.execute = func(ctx context.Context, _ Request) runtime.ToolResult {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return runtime.TextResult("submitted", true)
	}
	first := make(chan runtime.ToolResult, 1)
	go func() { first <- act(b, run, "same") }()
	<-started
	duplicate := make(chan runtime.ToolResult, 1)
	go func() { duplicate <- act(b, run, "same") }()
	conflict := b.Call(context.Background(), run, "openfox_browser_act", "same", json.RawMessage(`{"action":"fill","text":"different"}`))
	if conflict.Text != "CALL_CONFLICT" {
		t.Fatal(conflict)
	}
	close(release)
	if !(<-first).OK || !(<-duplicate).OK || h.calls.Load() != 1 {
		t.Fatal("duplicate mutation executed")
	}
	if result := act(b, run, "same"); !result.OK || h.calls.Load() != 1 {
		t.Fatal("completed mutation replayed")
	}
	if err := b.SetGrant(bindingID, h, Grant{Epoch: 2}); err != nil {
		t.Fatal(err)
	}
	if result := act(b, run, "later"); result.Text != "CONTROL_REVOKED" {
		t.Fatal(result)
	}
	allow(t, b, h, 3)
	if result := act(b, run, "same"); !result.OK || h.calls.Load() != 1 {
		t.Fatal("regrant replayed old mutation")
	}
}

func TestQueueLimitRevocationCancelsActiveAndQueuedActions(t *testing.T) {
	b, _, h, run := fixture(t)
	allow(t, b, h, 1)
	started := make(chan struct{})
	h.execute = func(ctx context.Context, _ Request) runtime.ToolResult {
		close(started)
		<-ctx.Done()
		return runtime.TextResult("late success", true)
	}
	results := make(chan runtime.ToolResult, MaxQueue+1)
	go func() { results <- act(b, run, "active") }()
	<-started
	for i := range MaxQueue {
		go func() { results <- act(b, run, fmt.Sprintf("queued-%d", i)) }()
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		b.mu.Lock()
		n := len(b.hosts[bindingID].queue)
		b.mu.Unlock()
		if n == MaxQueue+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queue did not fill")
		}
		time.Sleep(time.Millisecond)
	}
	if result := act(b, run, "overflow"); result.Text != "LIMIT_EXCEEDED" {
		t.Fatal(result)
	}
	if err := b.SetGrant(bindingID, h, Grant{Epoch: 2}); err != nil {
		t.Fatal(err)
	}
	for range MaxQueue + 1 {
		select {
		case result := <-results:
			if result.OK {
				t.Fatal("cancelled action reported success")
			}
		case <-time.After(time.Second):
			t.Fatal("takeover did not cancel work")
		}
	}
	if h.calls.Load() != 1 {
		t.Fatal("queued action reached host after takeover")
	}
}

func TestPendingMessageResolvesOnceAndRunEndRevokes(t *testing.T) {
	b, _, h, run := fixture(t)
	if err := b.SetGrant(bindingID, h, Grant{ClientMessageID: messageID, Epoch: 1, Origins: []string{"https://example.test"}}); err != nil {
		t.Fatal(err)
	}
	if result := act(b, run, "first"); !result.OK {
		t.Fatal(result)
	}
	other := run
	other.ID = messageID
	if result := act(b, other, "different-run"); result.Text != "CONTROL_REVOKED" {
		t.Fatal(result)
	}
	b.EndRun(run)
	if result := act(b, run, "after-end"); result.Text != "CONTROL_REVOKED" {
		t.Fatal(result)
	}
	if h.calls.Load() != 1 {
		t.Fatal("run boundary crossed")
	}
}

func TestLostCenterHeartbeatDisconnectsHost(t *testing.T) {
	b, c, h, run := fixture(t)
	allow(t, b, h, 1)
	c.reject.Store(true)
	if err := b.Heartbeat(context.Background(), bindingID, h); err == nil {
		t.Fatal("offline heartbeat succeeded")
	}
	if !h.closed.Load() {
		t.Fatal("failed binding kept host connection")
	}
	if result := act(b, run, "offline"); result.Text != "UNAVAILABLE" {
		t.Fatal(result)
	}
}

func TestRunEndRevokesUnusedMessageGrantWithoutTouchingAnotherMessage(t *testing.T) {
	b, _, host, run := fixture(t)
	if err := b.SetGrant(bindingID, host, Grant{Epoch: 1, ClientMessageID: messageID, Origins: []string{"https://example.test"}}); err != nil {
		t.Fatal(err)
	}
	run.ClientMessageIDs = []string{runID}
	b.EndRun(run)
	if b.hosts[bindingID].grant.ClientMessageID != messageID {
		t.Fatal("another pending message was revoked")
	}
	run.ClientMessageIDs = []string{messageID}
	wrongOwner := run
	wrongOwner.ActorID = device
	b.EndRun(wrongOwner)
	if b.hosts[bindingID].grant.ClientMessageID != messageID {
		t.Fatal("another owner revoked the grant")
	}
	b.EndRun(run)
	if b.hosts[bindingID].grant.ClientMessageID != "" || b.hosts[bindingID].grant.Epoch != 2 {
		t.Fatal("unused grant survived Run completion")
	}
	if host.calls.Load() != 0 {
		t.Fatal("revocation executed a page action")
	}
}
