// Package executiondelivery owns the durable WP-12 event, result, and
// artifact delivery boundary around the WP-09 Run tables.
package executiondelivery

import (
	"encoding/json"
	"errors"
	"time"

	"openfox/internal/execution"
)

const (
	MaxEventBatch   = 100
	MaxEventBytes   = 256 * 1024
	MaxEventPayload = 64 * 1024
	MaxSummarySize  = 16 * 1024
	MaxOutputSize   = 256 * 1024
	MaxResultBytes  = 512 * 1024
	MaxPageSize     = 100
)

var (
	ErrInvalidRequest         = errors.New("executiondelivery: invalid request")
	ErrNotFound               = errors.New("executiondelivery: resource not found")
	ErrForbidden              = errors.New("executiondelivery: action forbidden")
	ErrEventConflict          = errors.New("executiondelivery: event conflict")
	ErrResultConflict         = errors.New("executiondelivery: result conflict")
	ErrGenerationConflict     = errors.New("executiondelivery: stale generation")
	ErrReconciliationRequired = errors.New("executiondelivery: reconciliation is required before result acceptance")
	ErrCursorExpired          = errors.New("executiondelivery: event cursor expired")
	ErrStorageUnavailable     = errors.New("executiondelivery: storage unavailable")
	ErrResourceLimit          = errors.New("executiondelivery: resource limit exceeded")
)

type EventInput struct {
	ID         string
	Generation int64
	Sequence   int64
	Type       string
	Payload    json.RawMessage
	OccurredAt time.Time
}

type Event struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	RunID          string          `json:"run_id"`
	Generation     int64           `json:"generation"`
	Sequence       int64           `json:"sequence"`
	StreamSequence int64           `json:"stream_seq"`
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"safe_payload"`
	PayloadHash    string          `json:"-"`
	OccurredAt     time.Time       `json:"occurred_at"`
	ReceivedAt     time.Time       `json:"received_at"`
}

type EventBatchResult struct {
	Events            []Event `json:"events"`
	HighestContiguous int64   `json:"highest_contiguous"`
	Generation        int64   `json:"generation"`
}

type ResultInput struct {
	ID          string
	Generation  int64
	Status      string
	Summary     string
	Output      string
	Checks      json.RawMessage
	Unverified  bool
	ArtifactIDs []string
	BranchRef   string
	PayloadHash string
	// Usage 为空表示设备未上报（旧 daemon），run_results.usage 写 NULL。
	Usage *Usage
}

// Usage 是 Run 的 token 用量，存于 run_results.usage（jsonb）。各字段非负，上限 MaxUsageTokens。
type Usage struct {
	Input           int64 `json:"input"`
	CachedInput     int64 `json:"cached_input"`
	CacheWriteInput int64 `json:"cache_write_input"`
	Output          int64 `json:"output"`
	ReasoningOutput int64 `json:"reasoning_output"`
}

// MaxUsageTokens 是单个用量字段的上限（远超任何真实值，只用于拒绝明显错误的输入）。
const MaxUsageTokens = int64(1) << 50

func (u Usage) valid() bool {
	for _, value := range []int64{u.Input, u.CachedInput, u.CacheWriteInput, u.Output, u.ReasoningOutput} {
		if value < 0 || value > MaxUsageTokens {
			return false
		}
	}
	return true
}

// RunDetail 是读接口返回的 Run 补充事实：启动时刻、失败码与结果用量。
type RunDetail struct {
	StartedAt   *time.Time
	FailureCode string
	Usage       *Usage
}

type Result struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	RunID          string          `json:"run_id"`
	Generation     int64           `json:"generation"`
	Status         string          `json:"status"`
	Summary        string          `json:"summary"`
	Output         string          `json:"output"`
	Checks         json.RawMessage `json:"checks"`
	Unverified     bool            `json:"unverified"`
	ArtifactIDs    []string        `json:"artifact_ids"`
	BranchRef      string          `json:"branch_ref,omitempty"`
	PayloadHash    string          `json:"payload_hash"`
	CreatedAt      time.Time       `json:"created_at"`
	Usage          *Usage          `json:"usage"`
}

type Artifact struct {
	OrganizationID string     `json:"organization_id"`
	ID             string     `json:"id"`
	RunID          string     `json:"run_id"`
	ResultID       string     `json:"result_id,omitempty"`
	Name           string     `json:"name"`
	SizeBytes      int64      `json:"size_bytes"`
	SHA256         string     `json:"sha256"`
	MIMEType       string     `json:"mime_type"`
	State          string     `json:"state"`
	RetentionUntil *time.Time `json:"retention_until,omitempty"`
	Pinned         bool       `json:"pinned"`
	Version        int64      `json:"version"`
	CreatedAt      time.Time  `json:"created_at"`
}

type RunPage struct {
	Items      []execution.Run `json:"runs"`
	NextCursor int64           `json:"next_cursor,omitempty"`
}

type EventPage struct {
	Items      []Event `json:"events"`
	NextCursor int64   `json:"next_cursor,omitempty"`
}

// StreamPage 是统一事件流的一次轮询结果。Cursor 是扫描到的位置（含因不可见而跳过的行），
// 每条事件的 SSE id 见 RunCursors / OrgCursors（与 RunEvents / OrgEvents 一一对应）。
type StreamPage struct {
	RunEvents  []Event
	RunCursors []StreamCursor
	OrgEvents  []OrganizationEvent
	OrgCursors []StreamCursor
	Cursor     StreamCursor
}

// OrganizationEvent 是组织事件流（organization_events）里的一条失效通知。
type OrganizationEvent struct {
	Type           string          `json:"type"`
	Entity         string          `json:"entity"`
	ID             *string         `json:"id"`
	Version        *int64          `json:"version"`
	SpaceID        *string         `json:"space_id"`
	ConversationID *string         `json:"conversation_id"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      time.Time       `json:"created_at"`
}
