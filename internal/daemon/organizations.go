package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// 一台电脑加入多个组织（阶段 12e，D-09）的本机记录。
//
// config.json 的格式不变（organization_id 仍是注册时的组织，读取时 DisallowUnknownFields，
// 加字段会让旧版本 daemon 读不了）。其他组织记在旁路文件 organizations.json：
//   - 新版本 daemon 启动时合并「config.json 的组织 + 旁路文件」，并定期向中心刷新（GET /v1/device/organizations）；
//   - 旧版本 daemon（回退）不读旁路文件，只认 config.json 的第一个组织；
//   - 旁路文件缺失、损坏或中心不支持该端点时，视为只有 config.json 的单个组织（旧行为）。

const (
	OrganizationsFileName    = "organizations.json"
	organizationsFileVersion = 1
	maxDeviceOrganizations   = 100
)

type organizationsFile struct {
	Version         int       `json:"version"`
	OrganizationIDs []string  `json:"organization_ids"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// OrganizationsPath 返回与 config.json 同目录的旁路文件路径。
func OrganizationsPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), OrganizationsFileName)
}

// LoadOrganizations 返回本机已知的组织：config.json 的组织在前，其余按旁路文件顺序，去重。
// 旁路文件读不了时只返回 config.json 的组织。
func LoadOrganizations(configPath string, config DeviceConfig) []string {
	// 注册组织原样保留（config.json 已校验过），旁路文件里的条目才按 UUID 形状过滤。
	primary := strings.TrimSpace(config.OrganizationID)
	result := []string{}
	if primary != "" {
		result = append(result, primary)
	}
	contents, err := readPrivateFile(OrganizationsPath(configPath), 64*1024)
	if err != nil {
		return result
	}
	var file organizationsFile
	if json.Unmarshal(contents, &file) != nil || file.Version != organizationsFileVersion {
		return result
	}
	for _, id := range normalizeOrganizationIDs(file.OrganizationIDs) {
		if !strings.EqualFold(id, primary) && len(result) < maxDeviceOrganizations {
			result = append(result, id)
		}
	}
	return result
}

// WriteOrganizations 以 0600 原子写入旁路文件。
func WriteOrganizations(configPath string, organizationIDs []string) error {
	encoded, err := json.MarshalIndent(organizationsFile{
		Version: organizationsFileVersion, OrganizationIDs: normalizeOrganizationIDs(organizationIDs), UpdatedAt: time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivateFile(OrganizationsPath(configPath), append(encoded, '\n'))
}

// OrganizationsContain 报告 id 是否在本机已知的组织里（CLI / 桌面端判定“这台电脑属于该组织”）。
func OrganizationsContain(organizationIDs []string, id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, candidate := range organizationIDs {
		if strings.ToLower(candidate) == id && id != "" {
			return true
		}
	}
	return false
}

func normalizeOrganizationIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if len(id) != 36 || seen[id] || len(result) >= maxDeviceOrganizations {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	return result
}

// ErrOrganizationsUnsupported 表示中心不支持设备侧组织列表（旧版中心）。
var ErrOrganizationsUnsupported = errors.New("daemon: center does not list device organizations")

// DeviceOrganizations 返回中心上这台电脑 ACTIVE 授权的组织（GET /v1/device/organizations）。
func (c AuthClient) DeviceOrganizations(ctx context.Context, token string) ([]string, error) {
	var response struct {
		OrganizationIDs []string `json:"organization_ids"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/device/organizations", token, nil, &response); err != nil {
		var centerErr *CenterError
		if errors.As(err, &centerErr) && (centerErr.Status == http.StatusNotFound || centerErr.Status == http.StatusMethodNotAllowed ||
			centerErr.Status == http.StatusNotImplemented) {
			return nil, ErrOrganizationsUnsupported
		}
		return nil, err
	}
	if response.OrganizationIDs == nil {
		return nil, errors.New("daemon: center organization response is incomplete")
	}
	return normalizeOrganizationIDs(response.OrganizationIDs), nil
}

// OrganizationRefresher 定期向中心刷新本机的组织列表，并通知执行监督者与命令执行器。
type OrganizationRefresher struct {
	ConfigPath string
	Primary    string
	Client     interface {
		DeviceOrganizations(ctx context.Context, token string) ([]string, error)
	}
	Token    func(ctx context.Context) (string, error)
	Interval time.Duration
	// OnChange 收到新的组织列表时调用（列表非空；已去重）。
	OnChange func([]string)
	wake     chan struct{}
}

// Wake 请求尽快刷新一次（例如收到未知组织的唤醒）；非阻塞，多次合并。
func (r *OrganizationRefresher) Wake() {
	if r == nil || r.wake == nil {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *OrganizationRefresher) Init() {
	if r.wake == nil {
		r.wake = make(chan struct{}, 1)
	}
	if r.Interval <= 0 {
		r.Interval = 5 * time.Minute
	}
}

// Refresh 刷新一次：成功时写旁路文件并回调 OnChange。中心不支持时返回 ErrOrganizationsUnsupported，保持原列表。
func (r *OrganizationRefresher) Refresh(ctx context.Context) ([]string, error) {
	token, err := r.Token(ctx)
	if err != nil {
		return nil, err
	}
	organizations, err := r.Client.DeviceOrganizations(ctx, token)
	if err != nil {
		return nil, err
	}
	// 中心返回的是 ACTIVE 授权；注册组织被撤销后也不再轮询它（中心本来就会拒绝）。
	if err := WriteOrganizations(r.ConfigPath, organizations); err != nil {
		return nil, err
	}
	if r.OnChange != nil && len(organizations) > 0 {
		r.OnChange(organizations)
	}
	return organizations, nil
}

// Run 启动后立即刷新一次，之后按 Interval 或 Wake 刷新，直到 ctx 结束。中心不支持时停止刷新。
func (r *OrganizationRefresher) Run(ctx context.Context) {
	r.Init()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-r.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		if _, err := r.Refresh(ctx); errors.Is(err, ErrOrganizationsUnsupported) {
			return
		}
		timer.Reset(r.Interval)
	}
}
