// Package devices contains device management and heartbeat rules.
// Authentication remains in deviceauth; this package owns the device's
// lifecycle metadata and organization grants.
package devices

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"openfox/internal/deviceauth"
	"openfox/internal/dispatch"
	"openfox/internal/tenancy"
)

var (
	ErrInvalidRequest       = errors.New("devices: invalid request")
	ErrRegistrationConflict = errors.New("devices: registration token is not active")
	ErrOwnerRequired        = errors.New("devices: device owner is required")
	ErrGrantNotFound        = errors.New("devices: device grant not found")
	ErrVersionConflict      = errors.New("devices: device version conflict")
	ErrEpochConflict        = errors.New("devices: center epoch conflict")
	ErrDeviceNotFound       = errors.New("devices: device not found")
	ErrForbidden            = errors.New("devices: action forbidden")
	ErrHasActiveAgents      = errors.New("devices: device still has active agents")
	ErrInvalidCursor        = errors.New("devices: invalid cursor")
	// 替换电脑的冲突类错误。
	ErrDeviceOnline       = errors.New("devices: replaced device is still online")
	ErrGrantScopeMismatch = errors.New("devices: grant scopes differ")
	ErrTooManyAgents      = errors.New("devices: too many agents to move")
)

// OnlineAfter 是判定电脑在线的心跳窗口，与派发 worker 共用。
const OnlineAfter = dispatch.DefaultDeviceOnlineAfter

type Scope string

const (
	ScopePersonal     Scope = "PERSONAL"
	ScopeSpace        Scope = "SPACE"
	ScopeOrganization Scope = "ORGANIZATION"
)

func (scope Scope) Valid() bool {
	switch scope {
	case ScopePersonal, ScopeSpace, ScopeOrganization:
		return true
	default:
		return false
	}
}

type RegistrationTokenInput struct {
	Name      string
	InstallID string
	MaxSlots  int
	// Scope 是兑换后写入的授权范围（PERSONAL / ORGANIZATION）；空串按 ORGANIZATION（旧行为）。
	// 签发权限在 API 层判断，兑换时由 register_device_with_token 按签发人当前角色复核。
	Scope Scope
}

type RegistrationTokenResult struct {
	ID        string
	Token     string
	Scope     Scope
	ExpiresAt time.Time
}

type RegistrationExchange struct {
	Token     string
	KeyID     string
	PublicKey []byte
}

type RegistrationResult struct {
	DeviceID       string
	OrganizationID string
	OwnerUserID    string
	KeyGeneration  int64
	CenterEpoch    int64
}

// Device 是电脑在当前组织里的视图（电脑本身 + 本组织授权）。可空字段输出 null，见 MarshalJSON。
// 前端契约见 contracts/openapi.yaml 的 Device / DeviceDetail。
type Device struct {
	ID                string          `json:"id"`
	GrantID           string          `json:"grant_id"`
	OwnerUserID       string          `json:"owner_user_id"`
	OwnerEmail        string          `json:"owner_email"`
	Name              string          `json:"name"`
	InstallID         string          `json:"install_id"`
	Status            string          `json:"status"`
	GrantStatus       string          `json:"grant_status"`
	Online            bool            `json:"online"`
	Generation        int64           `json:"generation"`
	MaxSlots          int             `json:"max_slots"`
	ReservedSlots     int             `json:"reserved_slots"`
	LastSeenAt        *time.Time      `json:"last_seen_at"`
	ProtocolMajor     int             `json:"protocol_major"`
	ProtocolMinor     int             `json:"protocol_minor"`
	DaemonVersion     string          `json:"daemon_version"`
	OperatingSystem   string          `json:"operating_system"`
	Architecture      string          `json:"architecture"`
	Capabilities      json.RawMessage `json:"capabilities"`
	CenterEpoch       int64           `json:"center_epoch"`
	Version           int64           `json:"version"`
	SettingsVersion   int64           `json:"settings_version"`
	GrantScope        Scope           `json:"grant_scope"`
	GrantUserID       string          `json:"grant_user_id"`
	GrantSpaceID      string          `json:"grant_space_id"`
	GrantSpaceName    string          `json:"grant_space_name"`
	PermissionVersion int64           `json:"permission_version"`
	Runtimes          []Runtime       `json:"runtimes"`
	AgentCount        int             `json:"agent_count"`
	ActiveAgentCount  int             `json:"active_agent_count"`
	RunningTasks      int             `json:"running_tasks"`
	// Storage / RuntimesCheckedAt 从 capabilities 解析，未上报为 nil。
	Storage           *Storage   `json:"storage"`
	RuntimesCheckedAt *time.Time `json:"runtimes_checked_at"`
}

// deviceJSON 与 Device 字段一致，只把「空串 = 没有」的字段换成可空指针，保证输出 null。
type deviceJSON struct {
	ID                string          `json:"id"`
	GrantID           string          `json:"grant_id"`
	OwnerUserID       *string         `json:"owner_user_id"`
	OwnerEmail        *string         `json:"owner_email"`
	Name              string          `json:"name"`
	InstallID         *string         `json:"install_id"`
	Status            string          `json:"status"`
	GrantStatus       string          `json:"grant_status"`
	Online            bool            `json:"online"`
	Generation        int64           `json:"generation"`
	MaxSlots          int             `json:"max_slots"`
	ReservedSlots     int             `json:"reserved_slots"`
	LastSeenAt        *time.Time      `json:"last_seen_at"`
	ProtocolMajor     int             `json:"protocol_major"`
	ProtocolMinor     int             `json:"protocol_minor"`
	DaemonVersion     string          `json:"daemon_version"`
	OperatingSystem   string          `json:"operating_system"`
	Architecture      string          `json:"architecture"`
	Capabilities      json.RawMessage `json:"capabilities"`
	CenterEpoch       int64           `json:"center_epoch"`
	Version           int64           `json:"version"`
	SettingsVersion   int64           `json:"settings_version"`
	GrantScope        Scope           `json:"grant_scope"`
	GrantUserID       *string         `json:"grant_user_id"`
	GrantSpaceID      *string         `json:"grant_space_id"`
	GrantSpaceName    *string         `json:"grant_space_name"`
	PermissionVersion int64           `json:"permission_version"`
	Runtimes          []Runtime       `json:"runtimes"`
	AgentCount        int             `json:"agent_count"`
	ActiveAgentCount  int             `json:"active_agent_count"`
	RunningTasks      int             `json:"running_tasks"`
	Storage           *Storage        `json:"storage"`
	RuntimesCheckedAt *time.Time      `json:"runtimes_checked_at"`
}

func (device Device) MarshalJSON() ([]byte, error) {
	capabilities := device.Capabilities
	if len(capabilities) == 0 {
		capabilities = json.RawMessage("{}")
	}
	runtimes := device.Runtimes
	if runtimes == nil {
		runtimes = []Runtime{}
	}
	return json.Marshal(deviceJSON{
		ID: device.ID, GrantID: device.GrantID, OwnerUserID: nullable(device.OwnerUserID),
		OwnerEmail: nullable(device.OwnerEmail), Name: device.Name, InstallID: nullable(device.InstallID),
		Status: device.Status, GrantStatus: device.GrantStatus, Online: device.Online,
		Generation: device.Generation, MaxSlots: device.MaxSlots, ReservedSlots: device.ReservedSlots,
		LastSeenAt: device.LastSeenAt, ProtocolMajor: device.ProtocolMajor, ProtocolMinor: device.ProtocolMinor,
		DaemonVersion: device.DaemonVersion, OperatingSystem: device.OperatingSystem,
		Architecture: device.Architecture, Capabilities: capabilities, CenterEpoch: device.CenterEpoch,
		Version: device.Version, SettingsVersion: device.SettingsVersion, GrantScope: device.GrantScope,
		GrantUserID: nullable(device.GrantUserID), GrantSpaceID: nullable(device.GrantSpaceID),
		GrantSpaceName: nullable(device.GrantSpaceName), PermissionVersion: device.PermissionVersion,
		Runtimes: runtimes, AgentCount: device.AgentCount, ActiveAgentCount: device.ActiveAgentCount,
		RunningTasks: device.RunningTasks, Storage: device.Storage, RuntimesCheckedAt: device.RuntimesCheckedAt,
	})
}

func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// Runtime 是从心跳 capabilities.runtimes 解析出的一个 Runtime（目前只有 codex）。
type Runtime struct {
	Provider    string         `json:"provider"`
	Version     *string        `json:"version"`
	Status      string         `json:"status"`
	Runnable    bool           `json:"runnable"`
	Reason      *string        `json:"reason"`
	LoginStatus *string        `json:"login_status"`
	Models      []RuntimeModel `json:"models"`
	// ApprovalModes 是该 Runtime 适配器支持的权限档位；一定包含 FOLLOW_RUNTIME，旧版后台服务只有它。
	ApprovalModes    []string `json:"approval_modes"`
	ReasoningEfforts []string `json:"reasoning_efforts"`
	// CollaborationModes 是该 Runtime 适配器支持的协作模式；一定包含 FOLLOW_RUNTIME，旧版后台服务只有它。
	CollaborationModes []string `json:"collaboration_modes"`
}

type RuntimeModel struct {
	ID                     string   `json:"id"`
	DisplayName            string   `json:"display_name"`
	IsDefault              bool     `json:"is_default"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
}

const (
	RuntimeReady       = "READY"
	RuntimeNeedsLogin  = "NEEDS_LOGIN"
	RuntimeUnavailable = "UNAVAILABLE"
)

// BoundAgent 是绑定这台电脑的未归档 Agent（电脑详情用）。
type BoundAgent struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Title      *string `json:"title"`
	SpaceID    string  `json:"space_id"`
	Online     bool    `json:"online"`
	OwnerEmail *string `json:"owner_email"`
	Visibility string  `json:"visibility"`
}

// DeviceDetail 是电脑详情：列表字段 + 绑定的 Agent + 注册时间 + 时区。
type DeviceDetail struct {
	Device
	BoundAgents []BoundAgent
	CreatedAt   time.Time
	Timezone    string
}

func (detail DeviceDetail) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(detail.Device)
	if err != nil {
		return nil, err
	}
	agents := detail.BoundAgents
	if agents == nil {
		agents = []BoundAgent{}
	}
	extra, err := json.Marshal(struct {
		BoundAgents []BoundAgent `json:"bound_agents"`
		CreatedAt   time.Time    `json:"created_at"`
		Timezone    *string      `json:"timezone"`
	}{agents, detail.CreatedAt, nullable(detail.Timezone)})
	if err != nil {
		return nil, err
	}
	// 两个 JSON 对象拼接：去掉 base 的 '}' 与 extra 的 '{'。
	return append(append(base[:len(base)-1], ','), extra[1:]...), nil
}

// Viewer 是查看或管理电脑的当前成员；Role 为组织角色（大小写不敏感）。
type Viewer struct {
	Role string
}

func (viewer Viewer) role() string { return strings.ToUpper(strings.TrimSpace(viewer.Role)) }

func (viewer Viewer) manager() bool {
	role := viewer.role()
	return role == "OWNER" || role == "ADMIN"
}

type ListOptions struct {
	Limit  int
	Cursor string
}

type Page struct {
	Devices    []Device `json:"devices"`
	NextCursor *string  `json:"next_cursor"`
}

type SettingsInput struct {
	Name            *string
	MaxSlots        *int
	ExpectedVersion int64
}

type BlockingAgent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type RemovalBlockers struct {
	Agents []BlockingAgent `json:"agents"`
	Count  int             `json:"count"`
}

// RemovalResult 汇总移除或解绑的影响；CancelNotices 是提交后要唤醒电脑的取消通知，不输出。
type RemovalResult struct {
	ArchivedAgents int                     `json:"archived_agents"`
	CanceledRuns   int                     `json:"canceled_runs"`
	AffectedTasks  int                     `json:"affected_tasks"`
	CancelNotices  []dispatch.CancelNotice `json:"-"`
}

// ReplacementResult 是替换电脑的结果；CancelNotices 是提交后要唤醒旧电脑的取消通知，不输出。
type ReplacementResult struct {
	DeviceID         string                  `json:"device_id"`
	ReplacedDeviceID string                  `json:"replaced_device_id"`
	MovedAgents      int                     `json:"moved_agents"`
	CanceledRuns     int                     `json:"canceled_runs"`
	CancelNotices    []dispatch.CancelNotice `json:"-"`
}

type GrantScopeInput struct {
	Scope           Scope
	UserID          string
	SpaceID         string
	ExpectedVersion int64
}

type Heartbeat struct {
	CenterEpoch     int64
	ProtocolMajor   int
	ProtocolMinor   int
	DaemonVersion   string
	OperatingSystem string
	Architecture    string
	Capabilities    json.RawMessage
}

type HeartbeatResult struct {
	DeviceID    string
	CenterEpoch int64
	Version     int64
	ReceivedAt  time.Time
}

type KeyRotationInput struct {
	OldKeyID        string
	NewKeyID        string
	PublicKey       []byte
	ExpectedVersion int64
}

type KeyRotationResult struct {
	DeviceID      string `json:"device_id"`
	KeyID         string `json:"key_id"`
	KeyGeneration int64  `json:"key_generation"`
	DeviceVersion int64  `json:"device_version"`
}

type Store interface {
	IssueRegistrationToken(context.Context, tenancy.OrganizationSelection, RegistrationTokenInput, time.Time) (RegistrationTokenResult, error)
	RegisterWithToken(context.Context, RegistrationExchange) (RegistrationResult, error)
	// List 返回组织内全部授权（含已吊销），只供内部与测试使用；用户接口用 ListVisible。
	List(context.Context, tenancy.OrganizationSelection) ([]Device, error)
	ListVisible(context.Context, tenancy.OrganizationSelection, Viewer, ListOptions) (Page, error)
	Get(context.Context, tenancy.OrganizationSelection, Viewer, string) (DeviceDetail, error)
	UpdateSettings(context.Context, tenancy.OrganizationSelection, Viewer, string, SettingsInput) (Device, error)
	RemovalBlockers(context.Context, tenancy.OrganizationSelection, Viewer, string) (RemovalBlockers, error)
	Remove(context.Context, tenancy.OrganizationSelection, Viewer, string, time.Time) error
	RemoveFromOrganization(context.Context, tenancy.OrganizationSelection, Viewer, string) (RemovalResult, error)
	UnbindAgents(context.Context, tenancy.OrganizationSelection, Viewer, string, []string) (RemovalResult, error)
	ReplaceDevice(context.Context, tenancy.OrganizationSelection, Viewer, string, string) (ReplacementResult, error)
	UpdateGrantScope(context.Context, tenancy.OrganizationSelection, string, GrantScopeInput) (Device, error)
	RotateKey(context.Context, tenancy.OrganizationSelection, string, KeyRotationInput, time.Time) (KeyRotationResult, error)
	RecordHeartbeat(context.Context, deviceauth.Principal, Heartbeat, time.Time) (HeartbeatResult, error)
	CenterEpoch(context.Context) (int64, error)
}

func validateSelection(selection tenancy.OrganizationSelection) error {
	_, err := tenancy.NewTenantContext(selection, tenancy.SourceUserRequest)
	return err
}
