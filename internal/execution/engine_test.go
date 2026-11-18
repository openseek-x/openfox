package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

type testIDs struct {
	mu sync.Mutex
	n  int
}

func (g *testIDs) NewID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return fmt.Sprintf("id-%03d", g.n)
}

func newTestEngine() *Engine {
	return NewEngine(Options{
		Clock:       &testClock{now: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)},
		IDGenerator: &testIDs{},
	})
}

func validCreateRequest() CreateRequest {
	return CreateRequest{
		OrganizationID: "org-a",
		SpaceID:        "space-a",
		ConversationID: "conversation-a",
		AgentID:        "agent-a",
		ComputerID:     "computer-a",
		GrantID:        "grant-a",
		ConfigVersion:  2,
		GrantVersion:   3,
		ActorID:        "user-a",
		IdempotencyKey: "create-a",
		TaskCategory:   TaskTodo,
		Permissions: PermissionSnapshot{
			ActorID:             "user-a",
			OrganizationID:      "org-a",
			SpaceID:             "space-a",
			AgentOrganizationID: "org-a",
			AgentSpaceID:        "space-a",
			AgentOwnerID:        "user-a",
			ComputerID:          "computer-a",
			GrantID:             "grant-a",
			ConfigVersion:       2,
			GrantVersion:        3,
			ActorAuthorized:     true,
			OwnerAuthorized:     true,
			AgentActive:         true,
			DeviceAuthorized:    true,
		},
	}
}

func TestCreateDeduplicatesSourceAndIdempotency(t *testing.T) {
	engine := newTestEngine()
	request := validCreateRequest()
	request.SourceEventID = "source-1"
	request.SourceKind = "message.created"
	first, err := engine.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	if !first.Created || first.Run.State != StateQueued {
		t.Fatalf("first Create() = %#v", first)
	}

	replay := request
	replay.IdempotencyKey = "create-replay"
	second, err := engine.Create(context.Background(), replay)
	if err != nil {
		t.Fatalf("source replay error = %v", err)
	}
	if second.Run.ID != first.Run.ID || second.Created {
		t.Fatalf("source replay = %#v, want the existing Run without creation", second)
	}

	idempotent, err := engine.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("idempotent replay error = %v", err)
	}
	if idempotent.Run.ID != first.Run.ID || !idempotent.Created {
		t.Fatalf("idempotent replay = %#v", idempotent)
	}

	conflict := request
	conflict.InputBody = "different parameters"
	if _, err := engine.Create(context.Background(), conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
	newSourceKey := request
	newSourceKey.IdempotencyKey = "create-new-key"
	newSourceKey.InputBody = "different parameters"
	if _, err := engine.Create(context.Background(), newSourceKey); !errors.Is(err, ErrSourceDuplicate) {
		t.Fatalf("source conflict error = %v", err)
	}
}

func TestTaskGateAndExplicitDoneWake(t *testing.T) {
	for _, category := range []TaskCategory{TaskTriage, TaskBacklog, TaskCancelled, TaskDuplicate} {
		engine := newTestEngine()
		request := validCreateRequest()
		request.TaskCategory = category
		if _, err := engine.Create(context.Background(), request); !errors.Is(err, ErrTaskNotRunnable) {
			t.Errorf("category %s error = %v, want task gate", category, err)
		}
	}
	engine := newTestEngine()
	request := validCreateRequest()
	request.TaskCategory = TaskDone
	if _, err := engine.Create(context.Background(), request); !errors.Is(err, ErrTaskNotRunnable) {
		t.Fatalf("DONE without explicit wake error = %v", err)
	}
	request.ExplicitWake = true
	if result, err := engine.Create(context.Background(), request); err != nil || !result.Created {
		t.Fatalf("DONE explicit wake = %#v, error = %v", result, err)
	}
}

func TestActiveConversationQueuesInput(t *testing.T) {
	engine := newTestEngine()
	firstRequest := validCreateRequest()
	firstRequest.InputID = "input-1"
	firstRequest.InputBody = "first"
	first, err := engine.Create(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	if first.Input.Status != InputAssigned || first.Input.RunID != first.Run.ID {
		t.Fatalf("first input = %#v", first.Input)
	}
	if _, err := engine.Get(context.Background(), "org-b", first.Run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-organization Get() error = %v", err)
	}

	secondRequest := firstRequest
	secondRequest.IdempotencyKey = "create-2"
	secondRequest.InputID = "input-2"
	secondRequest.MessageID = "message-2"
	secondRequest.InputBody = "second"
	second, err := engine.Create(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	if !second.InputOnly || second.Created || second.Run.ID != first.Run.ID || second.Input.Status != InputPending {
		t.Fatalf("queued input result = %#v", second)
	}
	inputs, err := engine.ListInputs(context.Background(), "org-a", "conversation-a")
	if err != nil || len(inputs) != 2 {
		t.Fatalf("ListInputs() = %#v, error = %v", inputs, err)
	}
	assigned, err := engine.AssignInput(context.Background(), "org-a", "input-2", first.Run.ID)
	if err != nil || assigned.Status != InputAssigned || assigned.AssignedAt == nil {
		t.Fatalf("AssignInput() = %#v, error = %v", assigned, err)
	}
	consumed, err := engine.ConsumeInput(context.Background(), "org-a", "input-2", first.Run.ID)
	if err != nil || consumed.Status != InputConsumed || consumed.ConsumedAt == nil {
		t.Fatalf("ConsumeInput() = %#v, error = %v", consumed, err)
	}
	replay, err := engine.ConsumeInput(context.Background(), "org-a", "input-2", first.Run.ID)
	if err != nil || replay.Status != InputConsumed {
		t.Fatalf("ConsumeInput() replay = %#v, error = %v", replay, err)
	}
}

func TestRunCASCancellationAndTerminalNoRevival(t *testing.T) {
	engine := newTestEngine()
	created, err := engine.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	preparing, err := engine.StartPreparing(context.Background(), StartRequest{
		OrganizationID:  "org-a",
		RunID:           created.Run.ID,
		ExpectedVersion: created.Run.Version,
	}, "reservation-1", "offer-1")
	if err != nil || preparing.State != StatePreparing || preparing.Version != 2 {
		t.Fatalf("StartPreparing() = %#v, error = %v", preparing, err)
	}
	if _, err := engine.StartRunning(context.Background(), StartRequest{OrganizationID: "org-a", RunID: created.Run.ID, ExpectedVersion: 1}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale StartRunning() error = %v", err)
	}
	running, err := engine.StartRunning(context.Background(), StartRequest{OrganizationID: "org-a", RunID: created.Run.ID, ExpectedVersion: preparing.Version})
	if err != nil || running.State != StateRunning {
		t.Fatalf("StartRunning() = %#v, error = %v", running, err)
	}
	cancelRequested, err := engine.Cancel(context.Background(), CancelRequest{
		OrganizationID:  "org-a",
		RunID:           created.Run.ID,
		ExpectedVersion: running.Version,
		CommandID:       "cancel-1",
	})
	if err != nil || cancelRequested.State != StateCancelRequested {
		t.Fatalf("Cancel() = %#v, error = %v", cancelRequested, err)
	}
	completed, err := engine.Complete(context.Background(), CompleteRequest{
		OrganizationID:  "org-a",
		RunID:           created.Run.ID,
		ExpectedVersion: cancelRequested.Version,
		Succeeded:       false,
		Reason:          "runtime stopped",
	})
	if err != nil || completed.State != StateFailed || completed.FinishedAt == nil {
		t.Fatalf("Complete() = %#v, error = %v", completed, err)
	}
	if _, err := engine.Transition(context.Background(), TransitionRequest{
		OrganizationID:  "org-a",
		RunID:           created.Run.ID,
		ExpectedVersion: completed.Version,
		To:              StateRunning,
	}); !errors.Is(err, ErrTerminalRun) {
		t.Fatalf("terminal revival error = %v", err)
	}
	if replay, err := engine.Cancel(context.Background(), CancelRequest{OrganizationID: "org-a", RunID: created.Run.ID, ExpectedVersion: running.Version, CommandID: "cancel-1"}); err != nil || replay.State != StateCancelRequested {
		t.Fatalf("idempotent cancellation replay = %#v, error = %v", replay, err)
	}
}

func TestCreateRejectsPermissionMismatch(t *testing.T) {
	engine := newTestEngine()
	request := validCreateRequest()
	request.Permissions.ComputerID = "other-computer"
	if _, err := engine.Create(context.Background(), request); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("permission mismatch error = %v", err)
	}
}

func TestConcurrentConversationCreationProducesOneRun(t *testing.T) {
	engine := newTestEngine()
	const count = 20
	results := make(chan CreateResult, count)
	errorsCh := make(chan error, count)
	var wait sync.WaitGroup
	for i := 0; i < count; i++ {
		request := validCreateRequest()
		request.IdempotencyKey = fmt.Sprintf("create-%d", i)
		request.InputID = fmt.Sprintf("input-%d", i)
		request.InputBody = fmt.Sprintf("input %d", i)
		wait.Add(1)
		go func(request CreateRequest) {
			defer wait.Done()
			result, err := engine.Create(context.Background(), request)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- result
		}(request)
	}
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatalf("concurrent Create() error = %v", err)
	}
	created := 0
	var runID string
	for result := range results {
		if result.Created {
			created++
		}
		if runID == "" {
			runID = result.Run.ID
		} else if result.Run.ID != runID {
			t.Fatalf("concurrent Create() produced multiple Runs: %q and %q", runID, result.Run.ID)
		}
	}
	if created != 1 {
		t.Fatalf("created Runs = %d, want 1", created)
	}
	inputs, err := engine.ListInputs(context.Background(), "org-a", "conversation-a")
	if err != nil || len(inputs) != count {
		t.Fatalf("concurrent inputs = %d, error = %v", len(inputs), err)
	}
}
