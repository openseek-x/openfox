package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"openfox/internal/resourcepolicy"
)

// 组织运行配置版本确认（阶段 12e，K-02）。
//
// daemon 对每个已授权组织拉取运行配置（GET /v1/device/organizations/{org}/runtime-config），
// 校验通过后调用既有确认接口（POST .../runtime-config/ack），并在心跳 capabilities.runtime_config
// 里按组织上报本机状态：applied_version / restart_required / error。
//
// 本机目前没有在运行中读取这些策略字段的组件（队列与活动上限由中心执行），因此“应用”的含义是：
// 本机已收到并校验该版本。管理员把某次修改标记为需要重启、且该版本是在本 daemon 启动之后才出现的，
// 本机上报 restart_required=true，并一直保持到 daemon 重启（之后即使再出现不需要重启的新版本也不清除）。
// 校验失败上报 error，不确认。

// RuntimeConfigStatus 是一台电脑对某个组织运行配置的应用状态（随心跳上报）。
type RuntimeConfigStatus struct {
	AppliedVersion  int64     `json:"applied_version"`
	RestartRequired bool      `json:"restart_required"`
	Error           string    `json:"error,omitempty"`
	CheckedAt       time.Time `json:"checked_at"`
}

type RuntimeConfigClient interface {
	RuntimeConfig(ctx context.Context, token, organizationID string) (resourcepolicy.Config, error)
	AckRuntimeConfig(ctx context.Context, token, organizationID string, appliedVersion int64, restartRequired bool) error
}

// RuntimeConfigSync 维护各组织的配置应用状态。
type RuntimeConfigSync struct {
	Client RuntimeConfigClient
	Token  func(ctx context.Context) (string, error)
	Logger *slog.Logger
	// Publish 在状态变化后调用（例如写入心跳 capabilities 并请求立即心跳）。
	Publish func(map[string]RuntimeConfigStatus)

	mu sync.Mutex
	// startup 记录本 daemon 启动后第一次看到的版本：之后出现的、标记需重启的新版本要求重启。
	startup map[string]int64
	// needsRestart 记录启动后见过「需重启」版本的组织；只有进程重启才会清空。
	needsRestart map[string]bool
	status       map[string]RuntimeConfigStatus
}

// Sync 对给定组织各同步一次；返回当前全部状态。单个组织失败不影响其他组织。
func (s *RuntimeConfigSync) Sync(ctx context.Context, organizations []string) map[string]RuntimeConfigStatus {
	s.mu.Lock()
	if s.startup == nil {
		s.startup = map[string]int64{}
		s.status = map[string]RuntimeConfigStatus{}
		s.needsRestart = map[string]bool{}
	}
	s.mu.Unlock()
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	token, err := s.Token(ctx)
	if err != nil {
		return s.snapshot()
	}
	known := map[string]bool{}
	for _, organizationID := range organizations {
		known[organizationID] = true
		config, err := s.Client.RuntimeConfig(ctx, token, organizationID)
		if err != nil {
			if errors.Is(err, ErrRuntimeConfigUnsupported) {
				// 旧版中心：不上报，保持旧行为。
				continue
			}
			logger.Warn("daemon_runtime_config_fetch_failed", "organization_id", organizationID, "error", err.Error())
			continue
		}
		next := RuntimeConfigStatus{CheckedAt: time.Now().UTC()}
		if err := config.Policy.Validate(); err != nil {
			next.Error = "INVALID_POLICY"
			s.mu.Lock()
			if previous, ok := s.status[organizationID]; ok {
				next.AppliedVersion = previous.AppliedVersion
			}
			s.status[organizationID] = next
			s.mu.Unlock()
			continue
		}
		s.mu.Lock()
		startup, seen := s.startup[organizationID]
		if !seen {
			startup = config.ConfigVersion
			s.startup[organizationID] = startup
		}
		if config.RestartRequired && config.ConfigVersion > startup {
			s.needsRestart[organizationID] = true
		}
		next.RestartRequired = s.needsRestart[organizationID]
		s.mu.Unlock()
		next.AppliedVersion = config.ConfigVersion
		if config.AppliedVersion < config.ConfigVersion {
			if err := s.Client.AckRuntimeConfig(ctx, token, organizationID, config.ConfigVersion, next.RestartRequired); err != nil {
				logger.Warn("daemon_runtime_config_ack_failed", "organization_id", organizationID, "error", err.Error())
				next.Error = "ACK_FAILED"
			}
		}
		s.mu.Lock()
		s.status[organizationID] = next
		s.mu.Unlock()
	}
	s.mu.Lock()
	for organizationID := range s.status {
		if !known[organizationID] {
			delete(s.status, organizationID)
		}
	}
	s.mu.Unlock()
	snapshot := s.snapshot()
	if s.Publish != nil {
		s.Publish(snapshot)
	}
	return snapshot
}

func (s *RuntimeConfigSync) snapshot() map[string]RuntimeConfigStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]RuntimeConfigStatus, len(s.status))
	for key, value := range s.status {
		result[key] = value
	}
	return result
}

// ErrRuntimeConfigUnsupported 表示中心没有设备侧运行配置端点（旧版中心）。
var ErrRuntimeConfigUnsupported = errors.New("daemon: center does not serve runtime config to devices")

// RuntimeConfig 拉取组织运行配置（GET /v1/device/organizations/{org}/runtime-config）。
func (c AuthClient) RuntimeConfig(ctx context.Context, token, organizationID string) (resourcepolicy.Config, error) {
	var config resourcepolicy.Config
	err := c.do(ctx, http.MethodGet, "/v1/device/organizations/"+url.PathEscape(organizationID)+"/runtime-config", token, nil, &config)
	if err != nil {
		var centerErr *CenterError
		if errors.As(err, &centerErr) && (centerErr.Status == http.StatusMethodNotAllowed ||
			(centerErr.Status == http.StatusNotFound && centerErr.Code == "")) {
			return resourcepolicy.Config{}, ErrRuntimeConfigUnsupported
		}
		return resourcepolicy.Config{}, err
	}
	if config.ConfigVersion < 1 {
		return resourcepolicy.Config{}, errors.New("daemon: center runtime config response is incomplete")
	}
	return config, nil
}

// AckRuntimeConfig 确认本机已应用的版本（POST /v1/device/organizations/{org}/runtime-config/ack）。
func (c AuthClient) AckRuntimeConfig(ctx context.Context, token, organizationID string, appliedVersion int64, restartRequired bool) error {
	var ignored json.RawMessage
	return c.do(ctx, http.MethodPost, "/v1/device/organizations/"+url.PathEscape(organizationID)+"/runtime-config/ack", token,
		map[string]any{"applied_version": appliedVersion, "restart_required": restartRequired}, &ignored)
}
