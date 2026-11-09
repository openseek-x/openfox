// Package execution contains the durable execution-state contract and the
// scheduler boundary. Conversation, Runtime, and device transports remain
// separate packages so none can bypass the Run state machine.
package execution

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type State string

const (
	StateQueued          State = "QUEUED"
	StatePreparing       State = "PREPARING"
	StateRunning         State = "RUNNING"
	StateCancelRequested State = "CANCEL_REQUESTED"
	StateSucceeded       State = "SUCCEEDED"
	StateFailed          State = "FAILED"
	StateCanceled        State = "CANCELED"
)

func (s State) Terminal() bool {
	return s == StateSucceeded || s == StateFailed || s == StateCanceled
}

func (s State) valid() bool {
	switch s {
	case StateQueued, StatePreparing, StateRunning, StateCancelRequested,
		StateSucceeded, StateFailed, StateCanceled:
		return true
	default:
		return false
	}
}

func canTransition(from, to State) bool {
	switch from {
	case StateQueued:
		return to == StatePreparing || to == StateCanceled || to == StateFailed
	case StatePreparing:
		return to == StateRunning || to == StateCancelRequested || to == StateFailed || to == StateCanceled
	case StateRunning:
		return to == StateSucceeded || to == StateFailed || to == StateCancelRequested
	case StateCancelRequested:
		return to == StateSucceeded || to == StateFailed || to == StateCanceled
	default:
		return false
	}
}

type TaskCategory string

const (
	TaskTriage     TaskCategory = "TRIAGE"
	TaskBacklog    TaskCategory = "BACKLOG"
	TaskTodo       TaskCategory = "TODO"
	TaskInProgress TaskCategory = "IN_PROGRESS"
	TaskDone       TaskCategory = "DONE"
	TaskCancelled  TaskCategory = "CANCELLED"
	TaskDuplicate  TaskCategory = "DUPLICATE"
)

func (c TaskCategory) valid() bool {
	switch c {
	case "", TaskTriage, TaskBacklog, TaskTodo, TaskInProgress, TaskDone, TaskCancelled, TaskDuplicate:
		return true
	default:
		return false
	}
}

type WaitReason string

const (
	WaitNone                 WaitReason = ""
	WaitOrganizationCapacity WaitReason = "ORGANIZATION_CAPACITY"
	WaitAgentConcurrency     WaitReason = "AGENT_CONCURRENCY"
	// WaitAgentChatConcurrency 是会话 Run（无任务）等待 Agent 聊天并发名额。
	WaitAgentChatConcurrency WaitReason = "AGENT_CHAT_CONCURRENCY"
	WaitComputerCapacity     WaitReason = "COMPUTER_CONCURRENCY"
	WaitConversationBusy     WaitReason = "CONVERSATION_BUSY"
	WaitWorkdirBusy          WaitReason = "WORKDIR_BUSY"
	WaitAwaitingComputer     WaitReason = "AWAITING_COMPUTER"
	WaitComputerOffline      WaitReason = "COMPUTER_OFFLINE"
	WaitTaskGate             WaitReason = "TASK_GATE"
	WaitOfferExpired         WaitReason = "OFFER_EXPIRED"
	// WaitRuntimeUnavailable 是电脑在线，但本次 Run 选定的 Runtime 在最近一次心跳里不存在或不可运行。
	WaitRuntimeUnavailable WaitReason = "RUNTIME_UNAVAILABLE"
)

type InputStatus string

const (
	InputPending  InputStatus = "PENDING"
	InputAssigned InputStatus = "ASSIGNED"
	InputConsumed InputStatus = "CONSUMED"
	InputRejected InputStatus = "REJECTED"
)

type TriggerOutcome string

const (
	TriggerCreated   TriggerOutcome = "CREATED"
	TriggerDuplicate TriggerOutcome = "DUPLICATE"
	TriggerMerged    TriggerOutcome = "MERGED"
	TriggerRejected  TriggerOutcome = "REJECTED"
)

type PermissionSnapshot struct {
	ActorID             string
	OrganizationID      string
	SpaceID             string
	AgentOrganizationID string
	AgentSpaceID        string
	AgentOwnerID        string
	ComputerID          string
	GrantID             string
	ConfigVersion       int64
	GrantVersion        int64
	ActorAuthorized     bool
	OwnerAuthorized     bool
	AgentActive         bool
	DeviceAuthorized    bool
}

func (p PermissionSnapshot) validate(request CreateRequest) error {
	if strings.TrimSpace(p.ActorID) == "" || strings.TrimSpace(p.OrganizationID) == "" ||
		strings.TrimSpace(p.SpaceID) == "" || strings.TrimSpace(p.AgentOrganizationID) == "" ||
		strings.TrimSpace(p.AgentSpaceID) == "" || strings.TrimSpace(p.AgentOwnerID) == "" ||
		strings.TrimSpace(p.ComputerID) == "" || strings.TrimSpace(p.GrantID) == "" {
		return fmt.Errorf("%w: incomplete execution permission snapshot", ErrPermissionDenied)
	}
	if p.OrganizationID != request.OrganizationID || p.SpaceID != request.SpaceID ||
		p.AgentOrganizationID != request.OrganizationID || p.AgentSpaceID != request.SpaceID ||
		p.ActorID != request.ActorID || p.ComputerID != request.ComputerID ||
		p.GrantID != request.GrantID || p.ConfigVersion <= 0 || p.GrantVersion <= 0 {
		return fmt.Errorf("%w: permission snapshot does not match the request", ErrPermissionDenied)
	}
	if !p.ActorAuthorized || !p.OwnerAuthorized || !p.AgentActive || !p.DeviceAuthorized {
		return ErrPermissionDenied
	}
	return nil
}

type Run struct {
	ID                     string
	OrganizationID         string
	SpaceID                string
	ConversationID         string
	TaskID                 string
	AgentID                string
	ComputerID             string
	GrantID                string
	ConfigVersion          int64
	GrantVersion           int64
	ActorID                string
	State                  State
	Generation             int64
	ReconciliationRequired bool
	RetryOf                string
	Reason                 string
	WaitReason             WaitReason
	ReservationID          string
	OfferID                string
	Version                int64
	CreatedAt              time.Time
	UpdatedAt              time.Time
	CancelRequestedAt      *time.Time
	FinishedAt             *time.Time
}

type Input struct {
	ID             string
	OrganizationID string
	ConversationID string
	MessageID      string
	Body           string
	Status         InputStatus
	RunID          string
	CreatedAt      time.Time
	AssignedAt     *time.Time
	ConsumedAt     *time.Time
}

type Trigger struct {
	ID             string
	OrganizationID string
	SourceEventID  string
	SourceKind     string
	ConversationID string
	RunID          string
	Outcome        TriggerOutcome
	CreatedAt      time.Time
}

type OutboxEvent struct {
	ID             string
	OrganizationID string
	Kind           string
	AggregateID    string
	Payload        []byte
	Attempts       int
	NextAttemptAt  time.Time
	DeliveredAt    *time.Time
	CreatedAt      time.Time
}

type CreateRequest struct {
	OrganizationID string
	SpaceID        string
	ConversationID string
	TaskID         string
	AgentID        string
	ComputerID     string
	GrantID        string
	ConfigVersion  int64
	GrantVersion   int64
	ActorID        string
	SourceEventID  string
	SourceKind     string
	IdempotencyKey string
	InputID        string
	MessageID      string
	InputBody      string
	TaskCategory   TaskCategory
	ExplicitWake   bool
	RetryOf        string
	Permissions    PermissionSnapshot
}

type CreateResult struct {
	Run       Run
	Input     Input
	Trigger   Trigger
	Created   bool
	InputOnly bool
}

type TransitionRequest struct {
	OrganizationID  string
	RunID           string
	ExpectedVersion int64
	To              State
	Reason          string
	WaitReason      WaitReason
	Now             time.Time
}

type CancelRequest struct {
	OrganizationID  string
	RunID           string
	ExpectedVersion int64
	CommandID       string
	Now             time.Time
}

type StartRequest struct {
	OrganizationID  string
	RunID           string
	ExpectedVersion int64
	Now             time.Time
}

type CompleteRequest struct {
	OrganizationID  string
	RunID           string
	ExpectedVersion int64
	Succeeded       bool
	Reason          string
	Now             time.Time
}

type SchedulerResult struct {
	Run           Run
	ReservationID string
	OfferID       string
	Changed       bool
	Reserved      bool
}

var (
	ErrInvalidRequest         = errors.New("execution: invalid request")
	ErrNotFound               = errors.New("execution: run not found")
	ErrVersionConflict        = errors.New("execution: run version conflict")
	ErrInvalidTransition      = errors.New("execution: invalid state transition")
	ErrTerminalRun            = errors.New("execution: run is terminal")
	ErrIdempotencyConflict    = errors.New("execution: idempotency key parameter conflict")
	ErrSourceDuplicate        = errors.New("execution: source event already consumed")
	ErrConversationBusy       = errors.New("execution: conversation already has an active run")
	ErrTaskNotRunnable        = errors.New("execution: task category does not permit execution")
	ErrPermissionDenied       = errors.New("execution: execution permission denied")
	ErrMissingCapacity        = errors.New("execution: capacity broker is required")
	ErrReservationMissing     = errors.New("execution: capacity reservation is missing")
	ErrReconciliationRequired = errors.New("execution: reconciliation is required")
)

func validateCreateRequest(request CreateRequest) error {
	if strings.TrimSpace(request.OrganizationID) == "" || strings.TrimSpace(request.SpaceID) == "" ||
		strings.TrimSpace(request.ConversationID) == "" || strings.TrimSpace(request.AgentID) == "" ||
		strings.TrimSpace(request.ComputerID) == "" || strings.TrimSpace(request.GrantID) == "" ||
		strings.TrimSpace(request.ActorID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" ||
		request.ConfigVersion <= 0 || request.GrantVersion <= 0 {
		return fmt.Errorf("%w: incomplete run request", ErrInvalidRequest)
	}
	if !request.TaskCategory.valid() {
		return fmt.Errorf("%w: unknown task category %q", ErrInvalidRequest, request.TaskCategory)
	}
	if strings.TrimSpace(request.SourceEventID) != "" && strings.TrimSpace(request.SourceKind) == "" {
		return fmt.Errorf("%w: source kind is required with source event", ErrInvalidRequest)
	}
	if (request.InputID != "" || request.MessageID != "") && strings.TrimSpace(request.InputBody) == "" {
		return fmt.Errorf("%w: input body is required when an input is supplied", ErrInvalidRequest)
	}
	if len(request.InputBody) > 64*1024 {
		return fmt.Errorf("%w: input body exceeds 64 KiB", ErrInvalidRequest)
	}
	return request.Permissions.validate(request)
}

func validateTransitionRequest(request TransitionRequest) error {
	if strings.TrimSpace(request.OrganizationID) == "" || strings.TrimSpace(request.RunID) == "" || request.ExpectedVersion <= 0 || !request.To.valid() {
		return fmt.Errorf("%w: invalid transition request", ErrInvalidRequest)
	}
	return nil
}
