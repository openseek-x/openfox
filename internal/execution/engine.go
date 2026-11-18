package execution

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	createOperation      = "create_run"
	maxOutboxPayloadSize = 64 * 1024
)

// Clock and IDGenerator are seams for deterministic state-machine tests. The
// production durable store will use the same contract with database-generated
// UUIDs and transaction timestamps.
type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewID() string
}

type Options struct {
	Clock       Clock
	IDGenerator IDGenerator
}

// Engine is a concurrency-safe execution state-machine implementation. It is
// intentionally an in-process seam for unit and scheduler tests; PostgreSQL is
// the source of truth for the production queue and outbox.
type Engine struct {
	mu                 sync.RWMutex
	clock              Clock
	ids                IDGenerator
	runs               map[string]Run
	triggers           map[string]Trigger
	sources            map[string]sourceRecord
	idempotency        map[string]createRecord
	inputs             map[string]Input
	conversationInputs map[string][]string
	cancelCommands     map[string]Run
	outbox             []OutboxEvent
	queue              []string
}

type sourceRecord struct {
	Fingerprint string
	Trigger     Trigger
	Result      CreateResult
	Error       error
}

type createRecord struct {
	Fingerprint string
	RunID       string
	InputID     string
	TriggerID   string
	Created     bool
	InputOnly   bool
	Error       error
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

type randomIDGenerator struct{}

func (randomIDGenerator) NewID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(fmt.Sprintf("execution: generate ID: %v", err))
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func NewEngine(options Options) *Engine {
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	if options.IDGenerator == nil {
		options.IDGenerator = randomIDGenerator{}
	}
	return &Engine{
		clock:              options.Clock,
		ids:                options.IDGenerator,
		runs:               make(map[string]Run),
		triggers:           make(map[string]Trigger),
		sources:            make(map[string]sourceRecord),
		idempotency:        make(map[string]createRecord),
		inputs:             make(map[string]Input),
		conversationInputs: make(map[string][]string),
		cancelCommands:     make(map[string]Run),
	}
}

func (e *Engine) Create(ctx context.Context, request CreateRequest) (CreateResult, error) {
	if err := contextError(ctx); err != nil {
		return CreateResult{}, err
	}
	if err := validateCreateRequest(request); err != nil {
		return CreateResult{}, err
	}
	fingerprint, err := requestFingerprint(request)
	if err != nil {
		return CreateResult{}, err
	}
	sourceFingerprint, err := sourceRequestFingerprint(request)
	if err != nil {
		return CreateResult{}, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	key := idempotencyKey(request.OrganizationID, createOperation, request.IdempotencyKey)
	if record, found := e.idempotency[key]; found {
		if record.Fingerprint != fingerprint {
			return CreateResult{}, ErrIdempotencyConflict
		}
		return e.restoreCreateLocked(record)
	}

	if request.SourceEventID != "" {
		sourceKey := sourceKey(request.OrganizationID, request.SourceEventID)
		if record, found := e.sources[sourceKey]; found {
			if record.Fingerprint != sourceFingerprint {
				return CreateResult{}, ErrSourceDuplicate
			}
			return cloneCreateResult(record.Result), record.Error
		}
	}

	if err := validateTaskGate(request.TaskCategory, request.ExplicitWake); err != nil {
		result := CreateResult{}
		if request.SourceEventID != "" {
			trigger := Trigger{
				ID:             e.ids.NewID(),
				OrganizationID: request.OrganizationID,
				SourceEventID:  request.SourceEventID,
				SourceKind:     request.SourceKind,
				ConversationID: request.ConversationID,
				Outcome:        TriggerRejected,
				CreatedAt:      e.clock.Now(),
			}
			result.Trigger = cloneTrigger(trigger)
			e.triggers[trigger.ID] = trigger
			e.sources[sourceKey(request.OrganizationID, request.SourceEventID)] = sourceRecord{
				Fingerprint: sourceFingerprint,
				Trigger:     trigger,
				Result:      result,
				Error:       err,
			}
			e.idempotency[key] = createRecord{
				Fingerprint: fingerprint,
				TriggerID:   trigger.ID,
				Error:       err,
			}
		}
		return result, err
	}

	now := e.clock.Now()
	if active, found := e.activeRunLocked(request.OrganizationID, request.ConversationID); found {
		input, err := e.enqueueInputLocked(request, active, now, true)
		if err != nil {
			return CreateResult{}, err
		}
		trigger := Trigger{}
		if request.SourceEventID != "" {
			trigger = e.newTriggerLocked(request, active.ID, TriggerMerged, now)
		}
		result := CreateResult{Run: cloneRun(active), Input: cloneInput(input), Trigger: cloneTrigger(trigger), InputOnly: input.ID != ""}
		if request.SourceEventID != "" {
			e.recordTriggerLocked(request, sourceFingerprint, trigger)
		}
		e.idempotency[key] = createRecord{
			Fingerprint: fingerprint,
			RunID:       active.ID,
			InputID:     input.ID,
			TriggerID:   trigger.ID,
			InputOnly:   result.InputOnly,
		}
		return result, nil
	}

	run := Run{
		ID:             e.ids.NewID(),
		OrganizationID: request.OrganizationID,
		SpaceID:        request.SpaceID,
		ConversationID: request.ConversationID,
		TaskID:         request.TaskID,
		AgentID:        request.AgentID,
		ComputerID:     request.ComputerID,
		GrantID:        request.GrantID,
		ConfigVersion:  request.ConfigVersion,
		GrantVersion:   request.GrantVersion,
		ActorID:        request.ActorID,
		State:          StateQueued,
		Generation:     1,
		RetryOf:        request.RetryOf,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	e.runs[run.ID] = run
	e.queue = append(e.queue, run.ID)

	input, err := e.enqueueInputLocked(request, run, now, false)
	if err != nil {
		delete(e.runs, run.ID)
		e.queue = e.queue[:len(e.queue)-1]
		return CreateResult{}, err
	}
	trigger := Trigger{}
	if request.SourceEventID != "" {
		trigger = e.newTriggerLocked(request, run.ID, TriggerCreated, now)
	}
	result := CreateResult{Run: cloneRun(run), Input: cloneInput(input), Trigger: cloneTrigger(trigger), Created: true}
	if request.SourceEventID != "" {
		e.recordTriggerLocked(request, sourceFingerprint, trigger)
	}
	if err := e.appendOutboxLocked(run.OrganizationID, "run.queued", run.ID, map[string]any{
		"run_id":  run.ID,
		"version": run.Version,
	}); err != nil {
		return CreateResult{}, err
	}
	e.idempotency[key] = createRecord{
		Fingerprint: fingerprint,
		RunID:       run.ID,
		InputID:     input.ID,
		TriggerID:   trigger.ID,
		Created:     true,
	}
	return result, nil
}

func (e *Engine) Get(ctx context.Context, organizationID, runID string) (Run, error) {
	if err := contextError(ctx); err != nil {
		return Run{}, err
	}
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(runID) == "" {
		return Run{}, fmt.Errorf("%w: organization and run are required", ErrInvalidRequest)
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	run, found := e.runs[runID]
	if !found || run.OrganizationID != organizationID {
		return Run{}, ErrNotFound
	}
	return cloneRun(run), nil
}

func (e *Engine) ListQueued(ctx context.Context, organizationID string) ([]Run, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(organizationID) == "" {
		return nil, fmt.Errorf("%w: organization is required", ErrInvalidRequest)
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]Run, 0, len(e.queue))
	for _, runID := range e.queue {
		run, found := e.runs[runID]
		if found && run.OrganizationID == organizationID && run.State == StateQueued {
			result = append(result, cloneRun(run))
		}
	}
	return result, nil
}

func (e *Engine) ListInputs(ctx context.Context, organizationID, conversationID string) ([]Input, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(conversationID) == "" {
		return nil, fmt.Errorf("%w: organization and conversation are required", ErrInvalidRequest)
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	ids := append([]string(nil), e.conversationInputs[conversationKey(organizationID, conversationID)]...)
	result := make([]Input, 0, len(ids))
	for _, id := range ids {
		if input, found := e.inputs[inputKey(organizationID, id)]; found {
			result = append(result, cloneInput(input))
		}
	}
	return result, nil
}

func (e *Engine) Outbox(ctx context.Context, organizationID string) ([]OutboxEvent, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(organizationID) == "" {
		return nil, fmt.Errorf("%w: organization is required", ErrInvalidRequest)
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]OutboxEvent, 0, len(e.outbox))
	for _, event := range e.outbox {
		if event.OrganizationID == organizationID && event.DeliveredAt == nil {
			result = append(result, cloneOutboxEvent(event))
		}
	}
	return result, nil
}

// AssignInput atomically claims a pending conversation input for its Run.
// The durable implementation must use the same status predicate in SQL so a
// retrying worker cannot consume an input twice.
func (e *Engine) AssignInput(ctx context.Context, organizationID, inputID, runID string) (Input, error) {
	if err := contextError(ctx); err != nil {
		return Input{}, err
	}
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(inputID) == "" || strings.TrimSpace(runID) == "" {
		return Input{}, fmt.Errorf("%w: organization, input and run are required", ErrInvalidRequest)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	key := inputKey(organizationID, inputID)
	input, found := e.inputs[key]
	if !found {
		return Input{}, ErrNotFound
	}
	if input.RunID != runID {
		return Input{}, ErrConversationBusy
	}
	if input.Status == InputAssigned {
		return cloneInput(input), nil
	}
	if input.Status != InputPending {
		return Input{}, fmt.Errorf("%w: input is already %s", ErrInvalidTransition, input.Status)
	}
	now := e.clock.Now()
	input.Status = InputAssigned
	input.AssignedAt = timePointer(now)
	e.inputs[key] = input
	if err := e.appendOutboxLocked(organizationID, "input.assigned", inputID, map[string]any{"input_id": inputID, "run_id": runID}); err != nil {
		return Input{}, err
	}
	return cloneInput(input), nil
}

func (e *Engine) ConsumeInput(ctx context.Context, organizationID, inputID, runID string) (Input, error) {
	if err := contextError(ctx); err != nil {
		return Input{}, err
	}
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(inputID) == "" || strings.TrimSpace(runID) == "" {
		return Input{}, fmt.Errorf("%w: organization, input and run are required", ErrInvalidRequest)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	key := inputKey(organizationID, inputID)
	input, found := e.inputs[key]
	if !found {
		return Input{}, ErrNotFound
	}
	if input.RunID != runID {
		return Input{}, ErrConversationBusy
	}
	if input.Status == InputConsumed {
		return cloneInput(input), nil
	}
	if input.Status != InputAssigned {
		return Input{}, fmt.Errorf("%w: input must be assigned before consume", ErrInvalidTransition)
	}
	now := e.clock.Now()
	input.Status = InputConsumed
	input.ConsumedAt = timePointer(now)
	e.inputs[key] = input
	if err := e.appendOutboxLocked(organizationID, "input.consumed", inputID, map[string]any{"input_id": inputID, "run_id": runID}); err != nil {
		return Input{}, err
	}
	return cloneInput(input), nil
}

func (e *Engine) RejectInput(ctx context.Context, organizationID, inputID, runID string) (Input, error) {
	if err := contextError(ctx); err != nil {
		return Input{}, err
	}
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(inputID) == "" || strings.TrimSpace(runID) == "" {
		return Input{}, fmt.Errorf("%w: organization, input and run are required", ErrInvalidRequest)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	key := inputKey(organizationID, inputID)
	input, found := e.inputs[key]
	if !found {
		return Input{}, ErrNotFound
	}
	if input.RunID != runID {
		return Input{}, ErrConversationBusy
	}
	if input.Status == InputRejected {
		return cloneInput(input), nil
	}
	if input.Status != InputPending && input.Status != InputAssigned {
		return Input{}, fmt.Errorf("%w: input is already %s", ErrInvalidTransition, input.Status)
	}
	input.Status = InputRejected
	e.inputs[key] = input
	if err := e.appendOutboxLocked(organizationID, "input.rejected", inputID, map[string]any{"input_id": inputID, "run_id": runID}); err != nil {
		return Input{}, err
	}
	return cloneInput(input), nil
}

func (e *Engine) Transition(ctx context.Context, request TransitionRequest) (Run, error) {
	if err := contextError(ctx); err != nil {
		return Run{}, err
	}
	if err := validateTransitionRequest(request); err != nil {
		return Run{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	run, err := e.lockRunForUpdateLocked(request.OrganizationID, request.RunID, request.ExpectedVersion)
	if err != nil {
		return Run{}, err
	}
	if run.State.Terminal() {
		return Run{}, ErrTerminalRun
	}
	if !canTransition(run.State, request.To) {
		return Run{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, run.State, request.To)
	}
	now := e.requestTime(request.Now)
	e.applyTransitionLocked(&run, request.To, request.Reason, request.WaitReason, now)
	if err := e.appendStateEventLocked(run, "run.state_changed"); err != nil {
		return Run{}, err
	}
	e.runs[run.ID] = run
	return cloneRun(run), nil
}

func (e *Engine) Cancel(ctx context.Context, request CancelRequest) (Run, error) {
	if err := contextError(ctx); err != nil {
		return Run{}, err
	}
	if strings.TrimSpace(request.OrganizationID) == "" || strings.TrimSpace(request.RunID) == "" || request.ExpectedVersion <= 0 || strings.TrimSpace(request.CommandID) == "" {
		return Run{}, fmt.Errorf("%w: invalid cancellation request", ErrInvalidRequest)
	}
	commandKey := idempotencyKey(request.OrganizationID, "cancel_run", request.RunID+":"+request.CommandID)
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, found := e.cancelCommands[commandKey]; found {
		return cloneRun(existing), nil
	}
	run, err := e.lockRunForUpdateLocked(request.OrganizationID, request.RunID, request.ExpectedVersion)
	if err != nil {
		return Run{}, err
	}
	if !run.State.Terminal() {
		now := e.requestTime(request.Now)
		switch run.State {
		case StateQueued:
			e.applyTransitionLocked(&run, StateCanceled, "canceled before dispatch", WaitNone, now)
		case StatePreparing, StateRunning:
			e.applyTransitionLocked(&run, StateCancelRequested, "cancellation requested", run.WaitReason, now)
		case StateCancelRequested:
			// Repeating the command is a successful no-op.
		}
		if run.State != StateCancelRequested && run.State != StateCanceled {
			return Run{}, fmt.Errorf("%w: cancellation from %s", ErrInvalidTransition, run.State)
		}
		if run.Version != request.ExpectedVersion {
			if err := e.appendStateEventLocked(run, "run.cancel_requested"); err != nil {
				return Run{}, err
			}
			e.runs[run.ID] = run
		}
	}
	e.cancelCommands[commandKey] = run
	return cloneRun(run), nil
}

func (e *Engine) AttachOffer(ctx context.Context, organizationID, runID string, expectedVersion int64, reservationID, offerID string) (Run, error) {
	if err := contextError(ctx); err != nil {
		return Run{}, err
	}
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(runID) == "" || expectedVersion <= 0 || strings.TrimSpace(reservationID) == "" || strings.TrimSpace(offerID) == "" {
		return Run{}, fmt.Errorf("%w: invalid capacity offer", ErrInvalidRequest)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	run, err := e.lockRunForUpdateLocked(organizationID, runID, expectedVersion)
	if err != nil {
		return Run{}, err
	}
	if run.State != StateQueued {
		return Run{}, fmt.Errorf("%w: offer requires queued run", ErrInvalidTransition)
	}
	run.ReservationID = reservationID
	run.OfferID = offerID
	run.WaitReason = WaitNone
	run.Version++
	run.UpdatedAt = e.clock.Now()
	e.runs[run.ID] = run
	if err := e.appendStateEventLocked(run, "run.offer_attached"); err != nil {
		return Run{}, err
	}
	return cloneRun(run), nil
}

func (e *Engine) ClearOffer(ctx context.Context, organizationID, runID string, expectedVersion int64, waitReason WaitReason, reason string) (Run, error) {
	if err := contextError(ctx); err != nil {
		return Run{}, err
	}
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(runID) == "" || expectedVersion <= 0 {
		return Run{}, fmt.Errorf("%w: invalid capacity offer cleanup", ErrInvalidRequest)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	run, err := e.lockRunForUpdateLocked(organizationID, runID, expectedVersion)
	if err != nil {
		return Run{}, err
	}
	run.ReservationID = ""
	run.OfferID = ""
	run.WaitReason = waitReason
	if reason != "" {
		run.Reason = reason
	}
	run.Version++
	run.UpdatedAt = e.clock.Now()
	e.runs[run.ID] = run
	if err := e.appendStateEventLocked(run, "run.offer_cleared"); err != nil {
		return Run{}, err
	}
	return cloneRun(run), nil
}

func (e *Engine) StartPreparing(ctx context.Context, request StartRequest, reservationID, offerID string) (Run, error) {
	if strings.TrimSpace(reservationID) == "" || strings.TrimSpace(offerID) == "" {
		return Run{}, fmt.Errorf("%w: reservation and offer are required", ErrInvalidRequest)
	}
	return e.transitionWithCapacity(ctx, request, StatePreparing, reservationID, offerID)
}

func (e *Engine) StartRunning(ctx context.Context, request StartRequest) (Run, error) {
	return e.Transition(ctx, TransitionRequest{
		OrganizationID:  request.OrganizationID,
		RunID:           request.RunID,
		ExpectedVersion: request.ExpectedVersion,
		To:              StateRunning,
		Now:             request.Now,
	})
}

func (e *Engine) Complete(ctx context.Context, request CompleteRequest) (Run, error) {
	if err := contextError(ctx); err != nil {
		return Run{}, err
	}
	if strings.TrimSpace(request.OrganizationID) == "" || strings.TrimSpace(request.RunID) == "" || request.ExpectedVersion <= 0 {
		return Run{}, fmt.Errorf("%w: invalid completion request", ErrInvalidRequest)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	run, err := e.lockRunForUpdateLocked(request.OrganizationID, request.RunID, request.ExpectedVersion)
	if err != nil {
		return Run{}, err
	}
	if run.State.Terminal() {
		return Run{}, ErrTerminalRun
	}
	if run.State != StatePreparing && run.State != StateRunning && run.State != StateCancelRequested {
		return Run{}, fmt.Errorf("%w: completion from %s", ErrInvalidTransition, run.State)
	}
	to := StateFailed
	if request.Succeeded {
		to = StateSucceeded
	}
	now := e.requestTime(request.Now)
	e.applyTransitionLocked(&run, to, request.Reason, WaitNone, now)
	e.runs[run.ID] = run
	if err := e.appendStateEventLocked(run, "run.completed"); err != nil {
		return Run{}, err
	}
	return cloneRun(run), nil
}

func (e *Engine) SetWaitReason(ctx context.Context, organizationID, runID string, expectedVersion int64, waitReason WaitReason, reason string) (Run, error) {
	if err := contextError(ctx); err != nil {
		return Run{}, err
	}
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(runID) == "" || expectedVersion <= 0 {
		return Run{}, fmt.Errorf("%w: invalid wait update", ErrInvalidRequest)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	run, err := e.lockRunForUpdateLocked(organizationID, runID, expectedVersion)
	if err != nil {
		return Run{}, err
	}
	if run.State.Terminal() {
		return Run{}, ErrTerminalRun
	}
	run.WaitReason = waitReason
	if reason != "" {
		run.Reason = reason
	}
	run.Version++
	run.UpdatedAt = e.clock.Now()
	e.runs[run.ID] = run
	if err := e.appendStateEventLocked(run, "run.wait_changed"); err != nil {
		return Run{}, err
	}
	return cloneRun(run), nil
}

func (e *Engine) MarkReconciliation(ctx context.Context, organizationID, runID string, expectedVersion int64, reason string) (Run, error) {
	if err := contextError(ctx); err != nil {
		return Run{}, err
	}
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(runID) == "" || expectedVersion <= 0 {
		return Run{}, fmt.Errorf("%w: invalid reconciliation update", ErrInvalidRequest)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	run, err := e.lockRunForUpdateLocked(organizationID, runID, expectedVersion)
	if err != nil {
		return Run{}, err
	}
	run.ReconciliationRequired = true
	if reason != "" {
		run.Reason = reason
	}
	run.Version++
	run.UpdatedAt = e.clock.Now()
	e.runs[run.ID] = run
	if err := e.appendStateEventLocked(run, "run.reconciliation_required"); err != nil {
		return Run{}, err
	}
	return cloneRun(run), nil
}

func (e *Engine) transitionWithCapacity(ctx context.Context, request StartRequest, to State, reservationID, offerID string) (Run, error) {
	if err := contextError(ctx); err != nil {
		return Run{}, err
	}
	if request.OrganizationID == "" || request.RunID == "" || request.ExpectedVersion <= 0 {
		return Run{}, fmt.Errorf("%w: invalid start request", ErrInvalidRequest)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	run, err := e.lockRunForUpdateLocked(request.OrganizationID, request.RunID, request.ExpectedVersion)
	if err != nil {
		return Run{}, err
	}
	if run.State.Terminal() {
		return Run{}, ErrTerminalRun
	}
	if !canTransition(run.State, to) {
		return Run{}, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, run.State, to)
	}
	run.ReservationID = reservationID
	run.OfferID = offerID
	e.applyTransitionLocked(&run, to, "", WaitNone, e.requestTime(request.Now))
	e.runs[run.ID] = run
	if err := e.appendStateEventLocked(run, "run.preparing"); err != nil {
		return Run{}, err
	}
	return cloneRun(run), nil
}

func (e *Engine) activeRunLocked(organizationID, conversationID string) (Run, bool) {
	var active Run
	found := false
	for _, run := range e.runs {
		if run.OrganizationID == organizationID && run.ConversationID == conversationID && !run.State.Terminal() {
			if !found || run.CreatedAt.Before(active.CreatedAt) || (run.CreatedAt.Equal(active.CreatedAt) && run.ID < active.ID) {
				active = run
				found = true
			}
		}
	}
	return active, found
}

func (e *Engine) enqueueInputLocked(request CreateRequest, run Run, now time.Time, pending bool) (Input, error) {
	wantsInput := request.InputID != "" || request.MessageID != "" || request.InputBody != ""
	if !wantsInput {
		return Input{}, nil
	}
	inputID := request.InputID
	if inputID == "" {
		inputID = e.ids.NewID()
	}
	key := inputKey(request.OrganizationID, inputID)
	if existing, found := e.inputs[key]; found {
		if existing.ConversationID != request.ConversationID || existing.MessageID != request.MessageID || existing.Body != request.InputBody {
			return Input{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	input := Input{
		ID:             inputID,
		OrganizationID: request.OrganizationID,
		ConversationID: request.ConversationID,
		MessageID:      request.MessageID,
		Body:           request.InputBody,
		Status:         InputAssigned,
		RunID:          run.ID,
		CreatedAt:      now,
		AssignedAt:     timePointer(now),
	}
	if pending {
		input.Status = InputPending
		input.AssignedAt = nil
	}
	e.inputs[key] = input
	conversationKey := conversationKey(request.OrganizationID, request.ConversationID)
	e.conversationInputs[conversationKey] = append(e.conversationInputs[conversationKey], input.ID)
	return input, nil
}

func (e *Engine) newTriggerLocked(request CreateRequest, runID string, outcome TriggerOutcome, now time.Time) Trigger {
	trigger := Trigger{
		ID:             e.ids.NewID(),
		OrganizationID: request.OrganizationID,
		SourceEventID:  request.SourceEventID,
		SourceKind:     request.SourceKind,
		ConversationID: request.ConversationID,
		RunID:          runID,
		Outcome:        outcome,
		CreatedAt:      now,
	}
	e.triggers[trigger.ID] = trigger
	return trigger
}

func (e *Engine) recordTriggerLocked(request CreateRequest, fingerprint string, trigger Trigger) {
	if request.SourceEventID == "" {
		return
	}
	result := CreateResult{Trigger: cloneTrigger(trigger)}
	if trigger.RunID != "" {
		result.Run = cloneRun(e.runs[trigger.RunID])
	}
	if inputIDs := e.conversationInputs[conversationKey(request.OrganizationID, request.ConversationID)]; len(inputIDs) > 0 {
		input := e.inputs[inputKey(request.OrganizationID, inputIDs[len(inputIDs)-1])]
		if input.RunID == trigger.RunID && input.CreatedAt.Equal(trigger.CreatedAt) {
			result.Input = cloneInput(input)
		}
	}
	e.sources[sourceKey(request.OrganizationID, request.SourceEventID)] = sourceRecord{
		Fingerprint: fingerprint,
		Trigger:     trigger,
		Result:      result,
	}
}

func (e *Engine) restoreCreateLocked(record createRecord) (CreateResult, error) {
	result := CreateResult{Created: record.Created, InputOnly: record.InputOnly}
	if record.RunID != "" {
		run, found := e.runs[record.RunID]
		if !found {
			return CreateResult{}, fmt.Errorf("%w: idempotency run missing", ErrNotFound)
		}
		result.Run = cloneRun(run)
	}
	if record.InputID != "" {
		input, found := e.inputs[record.InputID]
		if !found {
			input, found = e.inputs[inputKey(result.Run.OrganizationID, record.InputID)]
		}
		if found {
			result.Input = cloneInput(input)
		}
	}
	if record.TriggerID != "" {
		trigger, found := e.triggers[record.TriggerID]
		if !found {
			return CreateResult{}, fmt.Errorf("%w: idempotency trigger missing", ErrNotFound)
		}
		result.Trigger = cloneTrigger(trigger)
	}
	return result, record.Error
}

func (e *Engine) lockRunForUpdateLocked(organizationID, runID string, expectedVersion int64) (Run, error) {
	run, found := e.runs[runID]
	if !found || run.OrganizationID != organizationID {
		return Run{}, ErrNotFound
	}
	if run.Version != expectedVersion {
		return Run{}, ErrVersionConflict
	}
	return run, nil
}

func (e *Engine) applyTransitionLocked(run *Run, to State, reason string, waitReason WaitReason, now time.Time) {
	run.State = to
	if reason != "" {
		run.Reason = reason
	}
	run.WaitReason = waitReason
	if (to == StateCancelRequested || to == StateCanceled) && run.CancelRequestedAt == nil {
		run.CancelRequestedAt = timePointer(now)
	}
	if to.Terminal() {
		run.FinishedAt = timePointer(now)
	}
	run.Version++
	run.UpdatedAt = now
}

func (e *Engine) appendStateEventLocked(run Run, kind string) error {
	return e.appendOutboxLocked(run.OrganizationID, kind, run.ID, map[string]any{
		"run_id":  run.ID,
		"state":   run.State,
		"version": run.Version,
	})
}

func (e *Engine) appendOutboxLocked(organizationID, kind, aggregateID string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: marshal outbox payload: %v", ErrInvalidRequest, err)
	}
	if len(encoded) > maxOutboxPayloadSize {
		return fmt.Errorf("%w: outbox payload exceeds limit", ErrInvalidRequest)
	}
	now := e.clock.Now()
	e.outbox = append(e.outbox, OutboxEvent{
		ID:             e.ids.NewID(),
		OrganizationID: organizationID,
		Kind:           kind,
		AggregateID:    aggregateID,
		Payload:        append([]byte(nil), encoded...),
		NextAttemptAt:  now,
		CreatedAt:      now,
	})
	return nil
}

func (e *Engine) requestTime(value time.Time) time.Time {
	if value.IsZero() {
		return e.clock.Now()
	}
	return value.UTC()
}

func validateTaskGate(category TaskCategory, explicitWake bool) error {
	switch category {
	case TaskTriage, TaskBacklog, TaskCancelled, TaskDuplicate:
		return fmt.Errorf("%w: task category %s is not runnable", ErrTaskNotRunnable, category)
	case TaskDone:
		if !explicitWake {
			return fmt.Errorf("%w: DONE requires an explicit wake", ErrTaskNotRunnable)
		}
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func requestFingerprint(request CreateRequest) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("%w: fingerprint request: %v", ErrInvalidRequest, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func sourceRequestFingerprint(request CreateRequest) (string, error) {
	request.IdempotencyKey = ""
	return requestFingerprint(request)
}

func idempotencyKey(organizationID, operation, value string) string {
	return organizationID + "\x00" + operation + "\x00" + value
}

func sourceKey(organizationID, sourceEventID string) string {
	return organizationID + "\x00" + sourceEventID
}

func conversationKey(organizationID, conversationID string) string {
	return organizationID + "\x00" + conversationID
}

func inputKey(organizationID, inputID string) string {
	return organizationID + "\x00" + inputID
}

func cloneCreateResult(result CreateResult) CreateResult {
	result.Run = cloneRun(result.Run)
	result.Input = cloneInput(result.Input)
	result.Trigger = cloneTrigger(result.Trigger)
	return result
}

func cloneRun(run Run) Run {
	run.CancelRequestedAt = cloneTime(run.CancelRequestedAt)
	run.FinishedAt = cloneTime(run.FinishedAt)
	return run
}

func cloneInput(input Input) Input {
	input.AssignedAt = cloneTime(input.AssignedAt)
	input.ConsumedAt = cloneTime(input.ConsumedAt)
	return input
}

func cloneTrigger(trigger Trigger) Trigger {
	return trigger
}

func cloneOutboxEvent(event OutboxEvent) OutboxEvent {
	event.Payload = append([]byte(nil), event.Payload...)
	event.DeliveredAt = cloneTime(event.DeliveredAt)
	return event
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func timePointer(value time.Time) *time.Time {
	copyValue := value
	return &copyValue
}
