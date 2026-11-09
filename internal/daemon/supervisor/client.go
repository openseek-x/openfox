package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"openfox/internal/reposource"
	"strconv"
	"strings"
	"time"

	"openfox/internal/artifacts"
	"openfox/internal/mcpconnect"
	"openfox/internal/runtime"
	"openfox/internal/runtime/mcpconfig"
)

const (
	// 单次请求的总超时；中心端点都是短事务。
	defaultRequestTimeout = 15 * time.Second
	// 响应体上限：运行上下文最大约 512KiB prompt + 32KiB 历史。
	maxResponseBytes = 2 << 20
	// 技能包响应上限：单个技能 ≤ 5 MB，base64 后约 6.7 MB，再加路径与 JSON 开销。
	maxSkillResponseBytes = 8 << 20
)

// APIError 是中心返回的非 2xx 响应。
type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("center returned %d %s", e.Status, e.Code)
}

// errorCode 返回中心错误码；非 APIError 返回空串。
func errorCode(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

// permanent 表示重试不会改变结果（4xx，除 408/429）。
func permanent(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != http.StatusRequestTimeout && apiErr.Status != http.StatusTooManyRequests
}

// Client 调用中心的设备端点。BaseURL 形如 https://center.example.com。
type Client struct {
	OnMCPInventoryPrivacy func(organizationID string, supported bool)
	BaseURL               string
	Token                 func(context.Context) (string, error)
	HTTP                  *http.Client
	// Invalidate 在中心以 401 拒绝令牌时调用（触发续期），随后用新令牌重试一次；可为空。
	Invalidate func(token string)
}

type Offer struct {
	OrganizationID string    `json:"organization_id"`
	OfferID        string    `json:"offer_id"`
	RunID          string    `json:"run_id"`
	Generation     int64     `json:"generation"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type Lease struct {
	RunID           string    `json:"run_id"`
	Generation      int64     `json:"generation"`
	State           string    `json:"state"`
	CenterEpoch     int64     `json:"center_epoch"`
	LeaseExpiresAt  time.Time `json:"lease_expires_at"`
	LeaseTTLMillis  int64     `json:"lease_ttl_ms"`
	RenewMillis     int64     `json:"renew_interval_ms"`
	CancelRequested bool      `json:"cancel_requested"`
	Challenge       string    `json:"challenge,omitempty"`
}

type HistoryMessage struct {
	Seq        int64  `json:"seq"`
	AuthorKind string `json:"author_kind"`
	Body       string `json:"body"`
}

type WorkspaceSelection struct {
	ComputerID           string `json:"computer_id,omitempty"`
	Mode                 string `json:"mode"`
	Directory            string `json:"directory,omitempty"`
	SourceConversationID string `json:"source_conversation_id,omitempty"`
	SourceAgentID        string `json:"source_agent_id,omitempty"`
	SourceDirectory      string `json:"source_directory,omitempty"`
	BaseRef              string `json:"base_ref,omitempty"`
}

type RunContext struct {
	Repositories       []reposource.Repository `json:"repositories,omitempty"`
	ActorID            string                  `json:"actor_id,omitempty"`
	AutomationOrigin   bool                    `json:"automation_origin,omitempty"`
	SocialTools        bool                    `json:"social_tools,omitempty"`
	MCPServers         []mcpconnect.RunServer  `json:"mcp_servers,omitempty"`
	MCPActorUserID     string                  `json:"mcp_actor_user_id,omitempty"`
	MCPDeviceOwnerID   string                  `json:"mcp_device_owner_id,omitempty"`
	WorkspaceSelection *WorkspaceSelection     `json:"workspace_selection,omitempty"`
	RunID              string                  `json:"run_id"`
	Generation         int64                   `json:"generation"`
	OrganizationID     string                  `json:"organization_id"`
	SpaceID            string                  `json:"space_id"`
	ConversationID     string                  `json:"conversation_id"`
	TaskID             string                  `json:"task_id,omitempty"`
	SharedTaskCrewID   string                  `json:"shared_task_crew_id,omitempty"`
	AgentID            string                  `json:"agent_id"`
	ConfigVersion      int64                   `json:"config_version"`
	Runtime            string                  `json:"runtime"`
	Model              string                  `json:"model"`
	// ApprovalMode 是本次 Run 要求的权限档位；旧中心没有该字段（空串，按 FOLLOW_RUNTIME）。
	ApprovalMode    string `json:"approval_mode"`
	ReasoningEffort string `json:"reasoning_effort"`
	// CollaborationMode 是本次 Run 要求的协作模式（FOLLOW_RUNTIME / DEFAULT / PLAN）；
	// 旧中心没有该字段（空串，按 FOLLOW_RUNTIME），历史值 FOLLOW_AGENT 同样按 FOLLOW_RUNTIME。
	CollaborationMode string           `json:"collaboration_mode"`
	Instructions      string           `json:"instructions"`
	RunTimeoutSeconds int64            `json:"run_timeout_seconds"`
	DirectoryMode     string           `json:"directory_mode"`
	DirectoryRoots    []string         `json:"directory_roots"`
	Prompt            string           `json:"prompt"`
	History           []HistoryMessage `json:"history"`
	// Agent 环境变量（中心解密后下发，不得记录）、自定义参数、Git 提交人、目录隔离方式。
	Environment      []EnvironmentVariable `json:"environment"`
	CustomArgs       []string              `json:"custom_args"`
	GitAuthor        *GitAuthor            `json:"git_author"`
	WorkdirIsolation string                `json:"workdir_isolation"`
	// Agent 当前装配的技能清单；旧中心没有该字段（为空）。
	Skills []RunSkill `json:"skills"`
	// Task 仅在 Run 绑定任务时由中心下发；旧中心没有该字段，按无任务处理。
	Task *RunTask `json:"task,omitempty"`
	// SpaceTools 仅在 Run 的 Agent 是飞书任务助手时由中心下发；旧中心没有该字段，按没有空间工具处理。
	SpaceTools *RunSpaceTools `json:"space_tools,omitempty"`
	// Attachments 是本次输入消息携带的附件清单；旧中心没有该字段，按无附件处理。
	Attachments []RunAttachment `json:"attachments"`
}

// RunAttachment 是 RunContext.attachments 的一项；设备按 ID 单独下载。
type RunAttachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

// RunSpaceTools 是 RunContext.space_tools：任务助手可操作的空间与每次运行的建 / 改次数上限。
type RunSpaceTools struct {
	SpaceID     string `json:"space_id"`
	SpaceName   string `json:"space_name"`
	SpacePrefix string `json:"space_prefix"`
	PublicURL   string `json:"public_url"`
	Limits      struct {
		Create int `json:"create"`
		Update int `json:"update"`
	} `json:"limits"`
}

// RunTask 是 RunContext.task：Run 绑定的任务快照与最近评论。
type RunTask struct {
	ID             string           `json:"id"`
	DisplayID      string           `json:"display_id"`
	Title          string           `json:"title"`
	Description    string           `json:"description"`
	Acceptance     string           `json:"acceptance"`
	StatusName     string           `json:"status_name"`
	StatusCategory string           `json:"status_category"`
	Origin         string           `json:"origin"`
	Comments       []RunTaskComment `json:"comments"`
}

type RunTaskComment struct {
	AuthorName string `json:"author_name"`
	AuthorKind string `json:"author_kind"`
	CreatedAt  string `json:"created_at"`
	Body       string `json:"body"`
}

// RunSkill 是 RunContext.skills 中的一项（DeviceRunSkill）。
type RunSkill struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    int64  `json:"version"`
	FileCount  int    `json:"file_count"`
	TotalBytes int64  `json:"total_bytes"`
}

// SkillBundleFile / SkillBundle 是 DeviceSkillBundle：路径相对技能目录、用 / 分隔，内容 base64。
type SkillBundleFile struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
}

type SkillBundle struct {
	Name      string            `json:"name"`
	Files     []SkillBundleFile `json:"files"`
	ErrorCode string            `json:"error_code,omitempty"`
}

// SkillImportItem 是上传导入技能后中心返回的该项结果（SkillImportItem）。
type SkillImportItem struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type EventInput struct {
	ID         string          `json:"id"`
	Generation int64           `json:"generation"`
	Sequence   int64           `json:"sequence"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"safe_payload"`
	OccurredAt time.Time       `json:"occurred_at"`
}

type ResultInput struct {
	ID         string          `json:"id"`
	Generation int64           `json:"generation"`
	Status     string          `json:"status"`
	Summary    string          `json:"summary"`
	Output     string          `json:"output"`
	Checks     json.RawMessage `json:"checks"`
	Unverified bool            `json:"unverified"`
	// Usage 为空时不发送该字段，兼容旧中心。
	Usage *runtime.Usage `json:"usage,omitempty"`
	// ArtifactIDs 是本次 Run 用发布工具登记的产物；为空时不发送，结果的规范化哈希与之前一致。
	// 中心据此把产物关联到结果（关联后才会按保留期清理）。
	ArtifactIDs []string `json:"artifact_ids,omitempty"`
}

// UploadedArtifact 是中心登记后的产物（RunArtifact 的子集）。
type UploadedArtifact struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	MIMEType  string `json:"mime_type"`
}

// artifactUploadTimeout 是单次产物上传的总时限（单个产物最大 20 MB）；须小于 Runtime 侧工具调用的时限（2 分钟）。
const artifactUploadTimeout = 100 * time.Second

// UploadRuntimeSessionExport sends one encrypted session package to the short-lived export slot.
func (c *Client) UploadRuntimeSessionExport(ctx context.Context, organizationID, exportID, wrappedKey string, content []byte) error {
	if c == nil || c.Token == nil || strings.TrimSpace(c.BaseURL) == "" || len(content) == 0 || int64(len(content)) > artifacts.MaxArtifactBytes || strings.TrimSpace(wrappedKey) == "" {
		return errors.New("supervisor: invalid runtime session export upload")
	}
	path := c.devicePath(organizationID, "runtime-session-exports", exportID, "content")
	for attempt := 0; ; attempt++ {
		token, err := c.Token(ctx)
		if err != nil {
			return err
		}
		requestContext, cancel := context.WithTimeout(ctx, artifactUploadTimeout)
		request, err := http.NewRequestWithContext(requestContext, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(content))
		if err == nil {
			request.ContentLength = int64(len(content))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Accept", "application/json")
			request.Header.Set("Content-Type", "application/octet-stream")
			request.Header.Set("X-Runtime-Session-Wrapped-Key", wrappedKey)
		}
		if err == nil {
			httpClient := c.HTTP
			if httpClient == nil {
				httpClient = http.DefaultClient
			}
			var response *http.Response
			response, err = httpClient.Do(request)
			if err == nil {
				payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
				_ = response.Body.Close()
				if readErr != nil {
					err = readErr
				} else if len(payload) > maxResponseBytes {
					err = errors.New("supervisor: center response is too large")
				} else if response.StatusCode < 200 || response.StatusCode > 299 {
					var envelope struct {
						Error struct {
							Code string `json:"code"`
						} `json:"error"`
					}
					_ = json.Unmarshal(payload, &envelope)
					err = &APIError{Status: response.StatusCode, Code: envelope.Error.Code}
				}
			}
		}
		cancel()
		if attempt == 0 && c.Invalidate != nil && IsAPIError(err, http.StatusUnauthorized, "") {
			c.Invalidate(token)
			continue
		}
		return err
	}
}

// UploadRunArtifact 上传一个产物（POST /v1/device/.../runs/{runID}/artifacts，请求体为原始字节）。
// 中心对同一 Run 内容相同的产物返回已有产物（200）；Run 已结束或代次不符 409。会话令牌过期时续期后重试一次。
func (c *Client) UploadRunArtifact(ctx context.Context, organizationID, runID string, generation int64, artifactID, name, mimeType string, content []byte) (UploadedArtifact, error) {
	if c == nil || c.Token == nil || strings.TrimSpace(c.BaseURL) == "" {
		return UploadedArtifact{}, errors.New("supervisor: client is not configured")
	}
	path := c.devicePath(organizationID, "runs", runID, "artifacts")
	for attempt := 0; ; attempt++ {
		token, err := c.Token(ctx)
		if err != nil {
			return UploadedArtifact{}, err
		}
		uploaded, err := c.uploadArtifact(ctx, path, token, generation, artifactID, name, mimeType, content)
		if attempt == 0 && c.Invalidate != nil && IsAPIError(err, http.StatusUnauthorized, "") {
			c.Invalidate(token)
			continue
		}
		return uploaded, err
	}
}

func (c *Client) uploadArtifact(ctx context.Context, path, token string, generation int64, artifactID, name, mimeType string, content []byte) (UploadedArtifact, error) {
	requestContext, cancel := context.WithTimeout(ctx, artifactUploadTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(content))
	if err != nil {
		return UploadedArtifact{}, err
	}
	request.ContentLength = int64(len(content))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Run-Generation", strconv.FormatInt(generation, 10))
	request.Header.Set("X-Artifact-ID", artifactID)
	request.Header.Set("X-Artifact-Name", name)
	request.Header.Set("X-Artifact-MIME", mimeType)
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return UploadedArtifact{}, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return UploadedArtifact{}, err
	}
	if len(payload) > maxResponseBytes {
		return UploadedArtifact{}, errors.New("supervisor: center response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(payload, &envelope)
		return UploadedArtifact{}, &APIError{Status: response.StatusCode, Code: envelope.Error.Code}
	}
	var uploaded UploadedArtifact
	if err := json.Unmarshal(payload, &uploaded); err != nil || uploaded.ID == "" {
		return UploadedArtifact{}, fmt.Errorf("supervisor: decode artifact response: %v", err)
	}
	return uploaded, nil
}

func (c *Client) devicePath(organizationID string, parts ...string) string {
	segments := []string{"v1", "device", "organizations", url.PathEscape(organizationID)}
	for _, part := range parts {
		segments = append(segments, url.PathEscape(part))
	}
	return "/" + strings.Join(segments, "/")
}

func (c *Client) ListOffers(ctx context.Context, organizationID string) ([]Offer, error) {
	var response struct {
		Offers []Offer `json:"offers"`
	}
	err := c.do(ctx, http.MethodGet, c.devicePath(organizationID, "offers"), nil, &response)
	return response.Offers, err
}

// StoreMCPInventory uses the device-authenticated private endpoint. This body
// is never placed in a heartbeat, websocket buffer, command result, or outbox.
func (c *Client) StoreMCPInventory(ctx context.Context, org string, runtimes map[string]mcpconfig.Inventory) error {
	return c.do(ctx, http.MethodPut, c.devicePath(org, "mcp-inventory"), map[string]any{"runtimes": runtimes}, nil)
}

func (c *Client) Claim(ctx context.Context, organizationID, offerID string) (Lease, error) {
	var lease Lease
	err := c.do(ctx, http.MethodPost, c.devicePath(organizationID, "offers", offerID, "claim"), struct{}{}, &lease)
	return lease, err
}

func (c *Client) Context(ctx context.Context, organizationID, runID string, generation int64) (RunContext, error) {
	var result RunContext
	path := c.devicePath(organizationID, "runs", runID, "context") + "?generation=" + strconv.FormatInt(generation, 10) + "&shared_task_dir=1"
	err := c.do(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

// CallTool 调用中心的任务工具接口。业务拒绝是 200 + ok=false；HTTP 失败 / 404 / 非 200 时
// 返回 ok=false 与简短原因（不含令牌），同时返回 err 供调用方记录。
func (c *Client) CallTool(ctx context.Context, organizationID, runID string, generation int64, tool, callID string, arguments json.RawMessage) (string, bool, error) {
	if len(arguments) == 0 || !json.Valid(arguments) {
		arguments = json.RawMessage("{}")
	}
	var response struct {
		OK   bool   `json:"ok"`
		Text string `json:"text"`
	}
	path := c.devicePath(organizationID, "runs", runID, "tools", tool)
	err := c.do(ctx, http.MethodPost, path, map[string]any{"generation": generation, "call_id": callID, "arguments": arguments}, &response)
	if err != nil {
		var apiErr *APIError
		switch {
		case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
			return "The task tool is not available for this run (not found).", false, err
		case errors.As(err, &apiErr):
			return fmt.Sprintf("The task tool request was rejected by the center (%d %s).", apiErr.Status, apiErr.Code), false, err
		case ctx.Err() != nil:
			return "The task tool request timed out or was cancelled.", false, err
		default:
			return "The task tool request failed (center unreachable).", false, err
		}
	}
	return response.Text, response.OK, nil
}

// RunSkill 下载本次 Run 的一个技能（需持有有效租约）。响应上限按 base64 单独放宽。
func (c *Client) RunSkill(ctx context.Context, organizationID, runID, skillID string, generation int64) (SkillBundle, error) {
	var bundle SkillBundle
	path := c.devicePath(organizationID, "runs", runID, "skills", skillID) + "?generation=" + strconv.FormatInt(generation, 10)
	err := c.doLimited(ctx, http.MethodGet, path, nil, &bundle, maxSkillResponseBytes)
	return bundle, err
}

// DownloadRunAttachment 把附件内容流式写入 dst，最多 limit 字节（超出返回错误）。会话令牌过期时续期后重试一次。
func (c *Client) DownloadRunAttachment(ctx context.Context, organizationID, runID, attachmentID string, generation int64, dst io.Writer, limit int64) error {
	if c == nil || c.Token == nil || strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("supervisor: client is not configured")
	}
	path := c.devicePath(organizationID, "runs", runID, "attachments", attachmentID) + "?generation=" + strconv.FormatInt(generation, 10)
	for attempt := 0; ; attempt++ {
		token, err := c.Token(ctx)
		if err != nil {
			return err
		}
		var written int64
		counted := &countingWriter{w: dst, n: &written}
		err = c.download(ctx, path, token, counted, limit)
		var apiErr *APIError
		if attempt == 0 && written == 0 && c.Invalidate != nil && errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
			c.Invalidate(token)
			continue
		}
		return err
	}
}

type countingWriter struct {
	w io.Writer
	n *int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	*c.n += int64(n)
	return n, err
}

func (c *Client) download(ctx context.Context, path, token string, dst io.Writer, limit int64) error {
	// 附件最大 20 MB，超时放宽为 5 分钟。
	requestContext, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(payload, &envelope)
		return &APIError{Status: response.StatusCode, Code: envelope.Error.Code}
	}
	copied, err := io.Copy(dst, io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if copied > limit {
		return errors.New("supervisor: attachment is larger than announced")
	}
	return nil
}

// UploadImportedSkill 为已领取的 skill_import 命令上传一个技能；导入已取消时返回 409 IMPORT_CANCELED。
func (c *Client) UploadImportedSkill(ctx context.Context, organizationID, commandID string, bundle SkillBundle) (SkillImportItem, error) {
	var item SkillImportItem
	err := c.do(ctx, http.MethodPost, c.devicePath(organizationID, "commands", commandID, "skills"), bundle, &item)
	return item, err
}

// DeviceCommand 是中心下发的远程命令（device_commands）。
type DeviceCommand struct {
	ID          string          `json:"id"`
	DeviceID    string          `json:"device_id"`
	Kind        string          `json:"kind"`
	Params      json.RawMessage `json:"params"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   string          `json:"error_code"`
	CreatedAt   time.Time       `json:"created_at"`
	DeliveredAt *time.Time      `json:"delivered_at"`
	CompletedAt *time.Time      `json:"completed_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
}

// PendingCommands 列出本设备在该组织的待执行命令。
func (c *Client) PendingCommands(ctx context.Context, organizationID string, limit int) ([]DeviceCommand, error) {
	var response struct {
		Commands []DeviceCommand `json:"commands"`
	}
	path := c.devicePath(organizationID, "commands") + "?status=PENDING&limit=" + strconv.Itoa(limit)
	err := c.do(ctx, http.MethodGet, path, nil, &response)
	return response.Commands, err
}

// ClaimCommand 领取命令（中心 CAS PENDING → DELIVERED）；过期 409 COMMAND_EXPIRED，已被领取 409 COMMAND_CLAIMED。
func (c *Client) ClaimCommand(ctx context.Context, organizationID, commandID string) (DeviceCommand, error) {
	var response struct {
		Command DeviceCommand `json:"command"`
	}
	err := c.do(ctx, http.MethodPost, c.devicePath(organizationID, "commands", commandID, "claim"), struct{}{}, &response)
	return response.Command, err
}

// CommandResult 回执命令结果；重复提交相同终态幂等。
func (c *Client) CommandResult(ctx context.Context, organizationID, commandID, status string, result json.RawMessage, errorCode string) error {
	return c.do(ctx, http.MethodPost, c.devicePath(organizationID, "commands", commandID, "result"),
		map[string]any{"status": status, "result": result, "error_code": errorCode}, nil)
}

// IsAPIError 报告 err 是否为中心返回的指定状态码与错误码（code 为空时只比较状态码）。
func IsAPIError(err error, status int, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == status && (code == "" || apiErr.Code == code)
}

// Permanent 报告重试不会改变结果（4xx，除 408/429）。
func Permanent(err error) bool { return permanent(err) }

func (c *Client) Start(ctx context.Context, organizationID, runID string, generation int64) (Lease, error) {
	var lease Lease
	err := c.do(ctx, http.MethodPost, c.devicePath(organizationID, "runs", runID, "start"),
		map[string]any{"generation": generation}, &lease)
	return lease, err
}

func (c *Client) Renew(ctx context.Context, organizationID, runID string, generation int64, challenge string) (Lease, error) {
	var lease Lease
	err := c.do(ctx, http.MethodPost, c.devicePath(organizationID, "runs", runID, "lease"),
		map[string]any{"generation": generation, "challenge": challenge}, &lease)
	return lease, err
}

func (c *Client) PrepareFailed(ctx context.Context, organizationID, runID string, generation int64, failureCode, detail string) error {
	return c.do(ctx, http.MethodPost, c.devicePath(organizationID, "runs", runID, "prepare-failed"),
		map[string]any{"generation": generation, "failure_code": failureCode, "detail": detail}, nil)
}

func (c *Client) AppendEvents(ctx context.Context, organizationID, runID string, events []EventInput) error {
	return c.do(ctx, http.MethodPost, c.devicePath(organizationID, "runs", runID, "events"),
		map[string]any{"events": events}, nil)
}

func (c *Client) AppendResult(ctx context.Context, organizationID, runID string, result ResultInput) error {
	return c.do(ctx, http.MethodPost, c.devicePath(organizationID, "runs", runID, "result"), result, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	return c.doLimited(ctx, method, path, body, out, maxResponseBytes)
}

func (c *Client) doLimited(ctx context.Context, method, path string, body, out any, limit int64) error {
	if c == nil || c.Token == nil || strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("supervisor: client is not configured")
	}
	var encoded []byte
	if body != nil {
		var err error
		if encoded, err = json.Marshal(body); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		token, err := c.Token(ctx)
		if err != nil {
			return err
		}
		err = c.send(ctx, method, path, token, encoded, body != nil, out, limit)
		var apiErr *APIError
		if attempt == 0 && c.Invalidate != nil && errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
			// 会话令牌过期或被吊销：续期后重试一次（所有设备端点在中心侧都按幂等设计）。
			c.Invalidate(token)
			continue
		}
		return err
	}
}

func (c *Client) send(ctx context.Context, method, path, token string, encoded []byte, hasBody bool, out any, limit int64) error {
	var reader io.Reader
	if hasBody {
		reader = bytes.NewReader(encoded)
	}
	requestContext, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, method, strings.TrimRight(c.BaseURL, "/")+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/context") {
		request.Header.Set("X-OpenFox-Conversation-Workdir", "1")
		request.Header.Set("X-OpenFox-Agent-MCP", "1")
		request.Header.Set("X-OpenFox-Repository-Auth", "1")
	}
	request.Header.Set("Accept", "application/json")
	if hasBody {
		request.Header.Set("Content-Type", "application/json")
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if method == http.MethodPut && strings.HasSuffix(request.URL.Path, "/mcp-inventory") {
		privateClient := *httpClient
		privateClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		httpClient = &privateClient
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if c.OnMCPInventoryPrivacy != nil && method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/offers") && response.StatusCode == 200 {
		parts := strings.Split(strings.TrimSuffix(path, "/offers"), "/")
		c.OnMCPInventoryPrivacy(parts[len(parts)-1], response.Header.Get("X-OpenFox-MCP-Inventory-Private") == "2")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(payload)) > limit {
		return errors.New("supervisor: center response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(payload, &envelope)
		return &APIError{Status: response.StatusCode, Code: envelope.Error.Code}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("supervisor: decode center response: %w", err)
	}
	return nil
}
