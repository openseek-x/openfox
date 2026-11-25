// Package execution 定义中心派发、结果投递和对账共用的 Run 类型。
package execution

import "time"

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

type WaitReason string

const WaitScheduled WaitReason = "SCHEDULED"

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

type Run struct {
	SchedulePending        bool       `json:"schedule_pending"`
	NotBefore              *time.Time `json:"not_before"`
	RequestedStartAt       *time.Time `json:"requested_start_at"`
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
