// Package dispatch 实现中心 Run 派发：把待处理的对话输入物化为 Run，
// 为排队 Run 预留容量并发出短期 offer，处理 offer 过期、排队超时与租约过期，
// 以及设备侧 claim → 上下文 → 启动许可 → 续租 → 准备失败和用户取消。
//
// 所有写入都在单个组织的租户事务内完成；WSS 只用于唤醒，事实以 HTTPS
// 幂等端点和数据库状态为准。锁顺序遵循 07 设计文档：组织容量 → 物理电脑 →
// Agent → Task → Conversation → Run → Offer。调用容量函数的路径一律先调用
// 容量函数，再按 version CAS 更新 Run，避免与 claim/reap 形成反向锁序。
package dispatch

import (
	"errors"
	"openfox/internal/reposource"
	"time"

	"openfox/internal/conversations"
	"openfox/internal/execution"
	"openfox/internal/mcpconnect"
)

var (
	ErrInvalidRequest         = errors.New("dispatch: invalid request")
	ErrNotFound               = errors.New("dispatch: resource not found")
	ErrForbidden              = errors.New("dispatch: action forbidden")
	ErrOfferUnavailable       = errors.New("dispatch: offer is no longer claimable")
	ErrStateConflict          = errors.New("dispatch: run state does not allow this operation")
	ErrGenerationConflict     = errors.New("dispatch: stale run generation")
	ErrReconciliationRequired = errors.New("dispatch: run requires reconciliation")
	ErrCancelRequested        = errors.New("dispatch: run cancellation was requested")
	ErrAuthorizationRevoked   = errors.New("dispatch: run authorization is no longer valid")
	ErrIdempotencyConflict    = errors.New("dispatch: idempotency key reused with different parameters")
)

const (
	DefaultOfferTTL          = 15 * time.Second
	DefaultLeaseTTL          = 90 * time.Second
	DefaultLeaseRenew        = 20 * time.Second
	DefaultPollInterval      = time.Second
	DefaultBatchSize         = 50
	DefaultDeviceOnlineAfter = 90 * time.Second

	// 单个 Run 一次最多合并的连续待处理输入数。
	MaxInputsPerRun = 20
	// 运行上下文中附带的历史消息上限。
	MaxHistoryMessages = 20
	MaxHistoryBytes    = 32 * 1024
	MaxBatchSize       = 500
	maxDetailBytes     = 1024
)

// Config 是派发 worker 与设备生命周期共享的时间参数。
type Config struct {
	OfferTTL          time.Duration
	LeaseTTL          time.Duration
	LeaseRenew        time.Duration
	PollInterval      time.Duration
	BatchSize         int
	DeviceOnlineAfter time.Duration
}

// DefaultConfig 返回设计文档中的默认期限：offer 15s、租约 90s、续租 20s。
func DefaultConfig() Config {
	return Config{
		OfferTTL: DefaultOfferTTL, LeaseTTL: DefaultLeaseTTL, LeaseRenew: DefaultLeaseRenew,
		PollInterval: DefaultPollInterval, BatchSize: DefaultBatchSize,
		DeviceOnlineAfter: DefaultDeviceOnlineAfter,
	}
}

// Validate 拒绝会让租约短于续租周期或批次无界的组合。
func (c Config) Validate() error {
	if c.OfferTTL < time.Second || c.OfferTTL > 5*time.Minute ||
		c.LeaseRenew < time.Second || c.LeaseTTL < 3*c.LeaseRenew || c.LeaseTTL > time.Hour ||
		c.PollInterval < 100*time.Millisecond || c.PollInterval > time.Minute ||
		c.BatchSize < 1 || c.BatchSize > MaxBatchSize ||
		c.DeviceOnlineAfter < 10*time.Second || c.DeviceOnlineAfter > time.Hour {
		return ErrInvalidRequest
	}
	return nil
}

func (c Config) normalized() Config {
	defaults := DefaultConfig()
	if c.OfferTTL == 0 {
		c.OfferTTL = defaults.OfferTTL
	}
	if c.LeaseTTL == 0 {
		c.LeaseTTL = defaults.LeaseTTL
	}
	if c.LeaseRenew == 0 {
		c.LeaseRenew = defaults.LeaseRenew
	}
	if c.PollInterval == 0 {
		c.PollInterval = defaults.PollInterval
	}
	if c.BatchSize == 0 {
		c.BatchSize = defaults.BatchSize
	}
	if c.DeviceOnlineAfter == 0 {
		c.DeviceOnlineAfter = defaults.DeviceOnlineAfter
	}
	return c
}

// Offer 是发给目标设备的短期派发提示；只有 claim 成功才代表领取。
type Offer struct {
	OrganizationID string    `json:"organization_id"`
	OfferID        string    `json:"offer_id"`
	RunID          string    `json:"run_id"`
	Generation     int64     `json:"generation"`
	DeviceID       string    `json:"computer_id"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// Lease 是 claim / start / renew 返回的租约事实。TTL 以服务端为准，
// Daemon 必须从“发送请求时”的单调时钟起算，而不是收到响应时。
type Lease struct {
	RunID           string          `json:"run_id"`
	Generation      int64           `json:"generation"`
	State           execution.State `json:"state"`
	CenterEpoch     int64           `json:"center_epoch"`
	LeaseExpiresAt  time.Time       `json:"lease_expires_at"`
	LeaseTTLMillis  int64           `json:"lease_ttl_ms"`
	RenewMillis     int64           `json:"renew_interval_ms"`
	CancelRequested bool            `json:"cancel_requested"`
	Challenge       string          `json:"challenge,omitempty"`
}

// RunContext 是设备准备执行所需的最小上下文快照。
type RunContext struct {
	Repositories       []reposource.Repository         `json:"repositories,omitempty"`
	ActorID            string                          `json:"actor_id,omitempty"`
	AutomationOrigin   bool                            `json:"automation_origin,omitempty"`
	SocialTools        bool                            `json:"social_tools,omitempty"`
	MCPServers         []mcpconnect.RunServer          `json:"mcp_servers,omitempty"`
	MCPActorUserID     string                          `json:"mcp_actor_user_id,omitempty"`
	MCPDeviceOwnerID   string                          `json:"mcp_device_owner_id,omitempty"`
	WorkspaceSelection *conversations.WorkspaceBinding `json:"workspace_selection,omitempty"`
	RunID              string                          `json:"run_id"`
	Generation         int64                           `json:"generation"`
	OrganizationID     string                          `json:"organization_id"`
	SpaceID            string                          `json:"space_id"`
	ConversationID     string                          `json:"conversation_id"`
	TaskID             string                          `json:"task_id,omitempty"`
	// SharedTaskCrewID 非空时，同一电脑按组织/空间/小队/任务复用目录。
	SharedTaskCrewID string `json:"shared_task_crew_id,omitempty"`
	AgentID          string `json:"agent_id"`
	ConfigVersion    int64  `json:"config_version"`
	Runtime          string `json:"runtime"`
	Model            string `json:"model"`
	// ApprovalMode 是本次 Run 要求的权限档位（FOLLOW_RUNTIME / READ_ONLY / WORKSPACE_WRITE / FULL_ACCESS）；
	// 旧版 daemon 不认识该字段，因此中心在派发前按设备上报的 approval_modes 核对，不支持就不派发。
	ApprovalMode string `json:"approval_mode"`
	// ReasoningEffort 是本次 Run 要求的模型思考深度；旧版 daemon 不认识时，中心会在派发前拒绝非 DEFAULT。
	ReasoningEffort string `json:"reasoning_effort"`
	// CollaborationMode 是本次 Run 要求的协作模式（FOLLOW_RUNTIME / DEFAULT / PLAN），取自 Run 固定的配置版本
	// （没有对话级覆盖；历史值 FOLLOW_AGENT 规范化为 FOLLOW_RUNTIME）。旧版 daemon 不认识该字段，
	// 因此中心在派发前按设备上报的 collaboration_modes 核对，不支持就不派发。
	CollaborationMode string           `json:"collaboration_mode"`
	Instructions      string           `json:"instructions"`
	RunTimeoutSeconds int64            `json:"run_timeout_seconds"`
	DirectoryMode     string           `json:"directory_mode"`
	DirectoryRoots    []string         `json:"directory_roots"`
	Prompt            string           `json:"prompt"`
	History           []HistoryMessage `json:"history"`
	// Environment 由 API 层在同一事务内调用 LoadRunEnvironment 填充（明文，不得记录）。
	Environment      []EnvironmentVariable `json:"environment"`
	CustomArgs       []string              `json:"custom_args"`
	GitAuthor        *GitAuthor            `json:"git_author"`
	WorkdirIsolation string                `json:"workdir_isolation"`
	// Agent 当前装配的技能清单；设备逐个下载后写进本次 Run 独立的 CODEX_HOME。
	Skills []RunSkill `json:"skills"`
	// 绑定任务时的任务快照（见 task_tools.go）；旧设备忽略该字段。
	Task *RunTask `json:"task,omitempty"`
	// Run 的 Agent 是飞书任务助手时下发的空间任务工具范围；旧版本 daemon 忽略该字段。
	SpaceTools *SpaceTools `json:"space_tools,omitempty"`
	// 本次输入消息携带的附件清单；设备逐个下载到工作目录。旧版本 daemon 忽略该字段。
	Attachments []RunAttachment `json:"attachments"`
}

// RunSkill 是 DeviceRunSkill。
type RunSkill struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    int64  `json:"version"`
	FileCount  int    `json:"file_count"`
	TotalBytes int64  `json:"total_bytes"`
}

type HistoryMessage struct {
	Seq        int64  `json:"seq"`
	AuthorKind string `json:"author_kind"`
	Body       string `json:"body"`
}

// PassResult 汇总一次组织派发扫描的结果；Offers/Cancels 在事务提交后
// 由 worker 通过 WSS 唤醒目标设备。
type PassResult struct {
	RunsCreated      int
	InputsRejected   int
	OffersCreated    int
	OffersReaped     int
	QueueExpired     int
	LeasesExpired    int
	OrphansReclaimed int
	Offers           []Offer
}

// OrphanReservation 是一次被回收的孤儿容量预留。Reason 为 RUN_MISSING、RUN_TERMINAL 或
// RUN_DETACHED；CountersConsistent=false 表示至少一个容量计数器回收前已是 0（未扣成负数）。
type OrphanReservation struct {
	ReservationID      string
	RunID              string
	Reason             string
	CountersConsistent bool
}

// CancelOutcome 描述一次取消请求的结果，以及是否需要通知设备停止进程。
type CancelOutcome struct {
	Run          execution.Run `json:"run"`
	NotifyDevice bool          `json:"-"`
	Replayed     bool          `json:"replayed"`
}

// prepareFailureCodes 是设备在未启动进程前可报告的失败码。
var prepareFailureCodes = map[string]struct{}{
	"WORKTREE_PREP_FAILED": {}, "REPO_UNAVAILABLE": {}, "RUNTIME_UNAVAILABLE": {},
	"PREPARE_FAILED": {}, "INTERNAL": {}, "CANCELED": {},
}
