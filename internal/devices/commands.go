package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/daemon/storage"
	"openfox/internal/platform/postgres"
	"openfox/internal/runtimepolicy"
	"openfox/internal/tenancy"
	"openfox/internal/workdirpath"
)

// 远程命令（迁移 000032 的 device_commands）：Web 上「重新检测 Runtime / 测试连接 / 清理存储」，
// 由电脑所有者发起，经 WSS 唤醒，电脑用设备会话领取并回执。
var (
	ErrCommandNotFound = errors.New("devices: command not found")
	ErrCommandExpired  = errors.New("devices: command expired")
	ErrCommandClaimed  = errors.New("devices: command already claimed")
	ErrCommandConflict = errors.New("devices: command result conflicts with its state")
)

type CommandKind string

const (
	CommandWorkspacePath        CommandKind = workdirpath.Kind
	CommandDeviceSettings       CommandKind = "device_settings"
	CommandRuntimeProbe         CommandKind = "runtime_probe"
	CommandRuntimeTest          CommandKind = "runtime_test"
	CommandRuntimeSessionExport CommandKind = "runtime_session_export"
	CommandStorageCleanup       CommandKind = "storage_cleanup"
	// 扫描本机技能 / 导入选中的技能。skill_import 只能由 createSkillRuntimeImport 下发。
	CommandSkillScan   CommandKind = "skill_scan"
	CommandSkillImport CommandKind = "skill_import"
)

const (
	CommandPending   = "PENDING"
	CommandDelivered = "DELIVERED"
	CommandSucceeded = "SUCCEEDED"
	CommandFailed    = "FAILED"
	CommandExpired   = "EXPIRED"
)

const (
	// commandClaimWindow 是 PENDING 命令的领取期限（电脑离线时过期为 EXPIRED）。
	commandClaimWindow = 2 * time.Minute
	// commandExecutionGrace 是执行超时之外留给回执网络往返的余量。
	commandExecutionGrace = 30 * time.Second
	maxCommandParamsBytes = 2048
	maxCommandResultBytes = 16 * 1024
	// maxSkillScanResultBytes 是 skill_scan 结果的上限（与迁移 000042 的 device_commands_result_shape 一致）。
	maxSkillScanResultBytes = 64 * 1024
	maxCommandModelLength   = 128
	DefaultCommandLimit     = 20
	MaxCommandLimit         = 50
)

var commandErrorCodePattern = regexp.MustCompile(`^[A-Z0-9_]{0,64}$`)

// ExecutionTimeout 是电脑执行该种命令的超时（与 daemon 一致）；领取后的截止时间 = 领取时间 + 超时 + 余量。
func (kind CommandKind) ExecutionTimeout() time.Duration {
	if kind == CommandStorageCleanup || kind == CommandSkillImport {
		return 5 * time.Minute
	}
	if kind == CommandRuntimeSessionExport {
		return 3 * time.Minute
	}
	return 30 * time.Second
}

// maxResultBytes 是该种命令回执结果的大小上限。
func (kind CommandKind) maxResultBytes() int {
	if kind == CommandSkillScan {
		return maxSkillScanResultBytes
	}
	return maxCommandResultBytes
}

func (kind CommandKind) Valid() bool {
	switch kind {
	case CommandWorkspacePath, CommandDeviceSettings, CommandRuntimeProbe, CommandRuntimeTest, CommandRuntimeSessionExport, CommandStorageCleanup, CommandSkillScan, CommandSkillImport:
		return true
	default:
		return false
	}
}

// Command 是一条远程命令。Params / Result 是 JSON 对象原文；Result 为空表示 null。
type Command struct {
	ID          string
	DeviceID    string
	Kind        CommandKind
	Params      json.RawMessage
	Status      string
	Result      json.RawMessage
	ErrorCode   string
	CreatedAt   time.Time
	DeliveredAt *time.Time
	CompletedAt *time.Time
	ExpiresAt   time.Time
}

func (command Command) MarshalJSON() ([]byte, error) {
	params := command.Params
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	result := command.Result
	if len(result) == 0 {
		result = json.RawMessage("null")
	}
	return json.Marshal(struct {
		ID          string          `json:"id"`
		DeviceID    string          `json:"device_id"`
		Kind        CommandKind     `json:"kind"`
		Params      json.RawMessage `json:"params"`
		Status      string          `json:"status"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   string          `json:"error_code"`
		CreatedAt   time.Time       `json:"created_at"`
		DeliveredAt *time.Time      `json:"delivered_at"`
		CompletedAt *time.Time      `json:"completed_at"`
		ExpiresAt   time.Time       `json:"expires_at"`
	}{command.ID, command.DeviceID, command.Kind, params, command.Status, result, command.ErrorCode,
		command.CreatedAt, command.DeliveredAt, command.CompletedAt, command.ExpiresAt})
}

// CommandResultInput 是电脑回执的结果。
type CommandResultInput struct {
	Status    string
	Result    json.RawMessage
	ErrorCode string
}

// CommandStore 是远程命令的存储。用户侧方法要求 selection 为用户请求；设备侧方法要求 selection
// 来自 deviceauth.SelectOrganization（ActorID = 设备 ID，AuthorizationVersion = 授权版本）。
type CommandStore interface {
	// CreateCommand 返回命令与是否新建；已有同 kind 未完成命令时返回该命令与 false。
	CreateCommand(ctx context.Context, selection tenancy.OrganizationSelection, deviceID string, kind CommandKind, params json.RawMessage) (Command, bool, error)
	GetCommand(ctx context.Context, selection tenancy.OrganizationSelection, deviceID, commandID string) (Command, error)
	ListPendingCommands(ctx context.Context, selection tenancy.OrganizationSelection, limit int) ([]Command, error)
	ClaimCommand(ctx context.Context, selection tenancy.OrganizationSelection, commandID string) (Command, error)
	CompleteCommand(ctx context.Context, selection tenancy.OrganizationSelection, commandID string, input CommandResultInput) (Command, error)
}

// ValidRuntimeProvider 报告 provider 是否是已知的 Runtime 名称（codex、claude-code、opencode、pi）。
func ValidRuntimeProvider(provider string) bool {
	switch provider {
	case "codex", "claude-code", "opencode", "pi":
		return true
	}
	return false
}

// NormalizeCommandParams 校验并规范化命令参数：runtime_probe / storage_cleanup 只接受空对象，
// runtime_test 只接受 {provider:已知 Runtime, model?}（provider 省略按 codex）。返回规范化后的 JSON 对象。
func NormalizeCommandParams(kind CommandKind, raw json.RawMessage) (json.RawMessage, error) {
	if !kind.Valid() {
		return nil, ErrInvalidRequest
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	if len(raw) > maxCommandParamsBytes || raw[0] != '{' {
		return nil, ErrInvalidRequest
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, ErrInvalidRequest
	}
	if kind == CommandDeviceSettings {
		v, err := runtimepolicy.ParseDeviceSettings(raw)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		return json.Marshal(v)
	}
	if kind == CommandWorkspacePath {
		v, err := workdirpath.Parse(raw)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		return json.Marshal(v)
	}
	if kind == CommandStorageCleanup && len(fields) > 0 {
		f, err := storage.ParseCleanupFilter(raw)
		if err != nil {
			return nil, ErrInvalidRequest
		}
		return json.Marshal(f)
	}
	if kind == CommandSkillImport {
		return normalizeSkillImportParams(fields)
	}
	if kind == CommandRuntimeSessionExport {
		return normalizeRuntimeSessionExportParams(fields)
	}
	if kind != CommandRuntimeTest {
		if len(fields) != 0 {
			return nil, ErrInvalidRequest
		}
		return json.RawMessage("{}"), nil
	}
	params := struct {
		Provider string `json:"provider"`
		Model    string `json:"model,omitempty"`
	}{Provider: "codex"}
	for key, value := range fields {
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return nil, ErrInvalidRequest
		}
		switch key {
		case "provider":
			if !ValidRuntimeProvider(text) {
				return nil, ErrInvalidRequest
			}
			params.Provider = text
		case "model":
			text = strings.TrimSpace(text)
			if len(text) > maxCommandModelLength || strings.IndexFunc(text, unicode.IsControl) >= 0 {
				return nil, ErrInvalidRequest
			}
			params.Model = text
		default:
			return nil, ErrInvalidRequest
		}
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

type RuntimeSessionExportParams struct {
	ExportID  string `json:"export_id"`
	RunID     string `json:"run_id"`
	Runtime   string `json:"runtime"`
	SessionID string `json:"session_id"`
	PublicKey string `json:"public_key"`
}

func normalizeRuntimeSessionExportParams(fields map[string]json.RawMessage) (json.RawMessage, error) {
	if len(fields) != 5 {
		return nil, ErrInvalidRequest
	}
	for _, key := range []string{"export_id", "run_id", "runtime", "session_id", "public_key"} {
		if _, ok := fields[key]; !ok {
			return nil, ErrInvalidRequest
		}
	}
	var params RuntimeSessionExportParams
	encodedFields, err := json.Marshal(fields)
	if err != nil || json.Unmarshal(encodedFields, &params) != nil {
		return nil, ErrInvalidRequest
	}
	if params.ExportID, err = normalizeUUID(params.ExportID); err != nil {
		return nil, ErrInvalidRequest
	}
	if params.RunID, err = normalizeUUID(params.RunID); err != nil {
		return nil, ErrInvalidRequest
	}
	if !ValidRuntimeProvider(params.Runtime) || len(params.SessionID) == 0 || len(params.SessionID) > 256 || len(params.PublicKey) == 0 || len(params.PublicKey) > 2048 {
		return nil, ErrInvalidRequest
	}
	if strings.TrimSpace(params.SessionID) != params.SessionID || strings.Contains(params.SessionID, "..") || strings.IndexFunc(params.SessionID, unicode.IsControl) >= 0 || strings.ContainsAny(params.SessionID, `/\\`) || strings.IndexFunc(params.PublicKey, unicode.IsControl) >= 0 {
		return nil, ErrInvalidRequest
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

// SkillImportParams 是 skill_import 命令的参数：目标空间与要导入的本机技能目录名（按导入顺序）。
type SkillImportParams struct {
	SpaceID string   `json:"space_id"`
	Names   []string `json:"names"`
}

var skillImportNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// maxSkillImportNames 与契约 createSkillRuntimeImport 的 names 上限一致。
const maxSkillImportNames = 20

// normalizeSkillImportParams 校验 {space_id, names}：空间 ID 是 UUID，1–20 个互不相同且符合目录名规则的名称。
func normalizeSkillImportParams(fields map[string]json.RawMessage) (json.RawMessage, error) {
	if len(fields) != 2 {
		return nil, ErrInvalidRequest
	}
	var params SkillImportParams
	if err := json.Unmarshal(fields["space_id"], &params.SpaceID); err != nil {
		return nil, ErrInvalidRequest
	}
	if err := json.Unmarshal(fields["names"], &params.Names); err != nil {
		return nil, ErrInvalidRequest
	}
	spaceID, err := normalizeUUID(params.SpaceID)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	params.SpaceID = spaceID
	if len(params.Names) == 0 || len(params.Names) > maxSkillImportNames {
		return nil, ErrInvalidRequest
	}
	seen := make(map[string]struct{}, len(params.Names))
	for _, name := range params.Names {
		if !skillImportNamePattern.MatchString(name) {
			return nil, ErrInvalidRequest
		}
		if _, ok := seen[name]; ok {
			return nil, ErrInvalidRequest
		}
		seen[name] = struct{}{}
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

// SameSkillImportParams 判断两份 skill_import 参数是否相同（空间与名称顺序都一致）。
func SameSkillImportParams(a, b json.RawMessage) bool {
	var left, right SkillImportParams
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return left.SpaceID == right.SpaceID && slices.Equal(left.Names, right.Names)
}

func validateCommandResult(input CommandResultInput) (CommandResultInput, error) {
	return validateCommandResultLimit(input, maxCommandResultBytes)
}

func validateCommandResultLimit(input CommandResultInput, limit int) (CommandResultInput, error) {
	input.ErrorCode = strings.TrimSpace(input.ErrorCode)
	if !commandErrorCodePattern.MatchString(input.ErrorCode) {
		return input, ErrInvalidRequest
	}
	result := bytes.TrimSpace(input.Result)
	if bytes.Equal(result, []byte("null")) {
		result = nil
	}
	if len(result) > 0 && (len(result) > limit || result[0] != '{' || !json.Valid(result)) {
		return input, ErrInvalidRequest
	}
	input.Result = result
	switch input.Status {
	case CommandSucceeded:
		if len(result) == 0 {
			return input, ErrInvalidRequest
		}
		input.ErrorCode = ""
	case CommandFailed:
		if input.ErrorCode == "" {
			return input, ErrInvalidRequest
		}
	default:
		return input, ErrInvalidRequest
	}
	return input, nil
}

const commandColumns = `id::text, device_id::text, kind, params, status, result, error_code,
	       created_at, delivered_at, completed_at, expires_at`

func scanCommand(row rowScanner) (Command, error) {
	var command Command
	var kind string
	var params, result []byte
	if err := row.Scan(&command.ID, &command.DeviceID, &kind, &params, &command.Status, &result,
		&command.ErrorCode, &command.CreatedAt, &command.DeliveredAt, &command.CompletedAt, &command.ExpiresAt); err != nil {
		return Command{}, err
	}
	command.Kind = CommandKind(kind)
	command.Params = params
	command.Result = result
	return command, nil
}

// expireOpenCommandsSQL 把超过 expires_at 的未完成命令标记为 EXPIRED；$1 为电脑 ID，附加条件由调用方拼接。
const expireOpenCommandsSQL = `
	UPDATE openfox.device_commands
	   SET status = 'EXPIRED', completed_at = clock_timestamp()
	 WHERE organization_id = openfox_private.current_organization_id()
	   AND device_id = $1::uuid AND status IN ('PENDING', 'DELIVERED')
	   AND expires_at <= clock_timestamp()`

func (s PostgresStore) CreateCommand(ctx context.Context, selection tenancy.OrganizationSelection, deviceID string, kind CommandKind, params json.RawMessage) (Command, bool, error) {
	if s.Pool == nil {
		return Command{}, false, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return Command{}, false, ErrInvalidRequest
	}
	// skill_import 带有中心侧的导入项记录，只能由 createSkillRuntimeImport 在同一事务里下发。
	if kind == CommandSkillImport {
		return Command{}, false, ErrInvalidRequest
	}
	var command Command
	created := false
	err := s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		command, created, err = CreateCommandInTx(ctx, tx, selection, deviceID, kind, params)
		return err
	})
	if err != nil {
		return Command{}, false, err
	}
	return command, created, nil
}

// CreateCommandInTx 在调用方已打开的用户租户事务里创建命令（要求当前成员是电脑所有者）。
// 已有同 kind 未完成命令时返回该命令与 false。
func CreateCommandInTx(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, deviceID string, kind CommandKind, params json.RawMessage) (Command, bool, error) {
	deviceID, err := normalizeUUID(deviceID)
	if err != nil {
		return Command{}, false, ErrDeviceNotFound
	}
	params, err = NormalizeCommandParams(kind, params)
	if err != nil {
		return Command{}, false, err
	}
	if err := requireOwnedDevice(ctx, tx, deviceID); err != nil {
		return Command{}, false, err
	}
	claimWindow := commandClaimWindow
	if kind == CommandWorkspacePath {
		if err := authorizeWorkDirectory(ctx, tx, selection, deviceID, params); err != nil {
			return Command{}, false, err
		}
		request, _ := workdirpath.Parse(params)
		claimWindow = time.Until(time.UnixMilli(request.ExpiresAt))
		if claimWindow <= 0 {
			return Command{}, false, ErrCommandExpired
		}
	}
	if _, err := tx.Exec(ctx, expireOpenCommandsSQL+` AND kind = $2`, deviceID, string(kind)); err != nil {
		return Command{}, false, err
	}
	// 部分唯一索引 device_commands_open_unique 保证同 kind 只有一条未完成；并发创建时冲突方读取已有的那条。
	for attempt := 0; attempt < 3; attempt++ {
		row := tx.QueryRow(ctx, `
			INSERT INTO openfox.device_commands (
				organization_id, device_id, kind, params, requested_by_user_id, expires_at
			) VALUES (
				openfox_private.current_organization_id(), $1::uuid, $2, $3::jsonb,
				openfox_private.current_actor_id(), clock_timestamp() + make_interval(secs => $4)
			)
			ON CONFLICT (organization_id, device_id, kind) WHERE status IN ('PENDING', 'DELIVERED') DO NOTHING
			RETURNING `+commandColumns, deviceID, string(kind), []byte(params), claimWindow.Seconds())
		inserted, err := scanCommand(row)
		if err == nil {
			// 审计元数据只允许白名单键：命令种类编码进 action（device.command_requested.<kind>）。
			return inserted, true, recordAudit(ctx, tx, selection, deviceID, "device.command_requested."+string(kind), map[string]string{
				"status": CommandPending,
			})
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Command{}, false, err
		}
		existing, err := scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+`
			  FROM openfox.device_commands
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND device_id = $1::uuid AND kind = $2 AND status IN ('PENDING', 'DELIVERED')`, deviceID, string(kind)))
		if err == nil {
			if kind == CommandStorageCleanup || kind == CommandDeviceSettings || kind == CommandWorkspacePath {
				prior, e := NormalizeCommandParams(kind, existing.Params)
				if e != nil || !bytes.Equal(prior, params) {
					return Command{}, false, ErrCommandConflict
				}
			}
			return existing, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Command{}, false, err
		}
		// 冲突的那条在两次语句之间已完成：重试插入。
	}
	return Command{}, false, ErrCommandConflict
}

func (s PostgresStore) GetCommand(ctx context.Context, selection tenancy.OrganizationSelection, deviceID, commandID string) (Command, error) {
	if s.Pool == nil {
		return Command{}, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return Command{}, ErrInvalidRequest
	}
	deviceID, err := normalizeUUID(deviceID)
	if err != nil {
		return Command{}, ErrCommandNotFound
	}
	commandID, err = normalizeUUID(commandID)
	if err != nil {
		return Command{}, ErrCommandNotFound
	}
	var command Command
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireOwnedDevice(ctx, tx, deviceID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, expireOpenCommandsSQL+` AND id = $2::uuid`, deviceID, commandID); err != nil {
			return err
		}
		var err error
		command, err = scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+`
			  FROM openfox.device_commands
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND device_id = $1::uuid AND id = $2::uuid`, deviceID, commandID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCommandNotFound
		}
		if err == nil && command.Kind == CommandWorkspacePath {
			var requester string
			if err := tx.QueryRow(ctx, `SELECT requested_by_user_id::text FROM openfox.device_commands
				WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, commandID).Scan(&requester); err != nil || requester != selection.ActorID {
				return ErrCommandNotFound
			}
			return authorizeWorkDirectory(ctx, tx, selection, deviceID, command.Params)
		}
		return err
	})
	return command, err
}

// requireOwnedDevice 校验当前成员是电脑所有者，且当前组织对电脑有 ACTIVE 授权；否则统一 ErrDeviceNotFound（404）。
func requireOwnedDevice(ctx context.Context, tx pgx.Tx, deviceID string) error {
	var owned bool
	err := tx.QueryRow(ctx, `
		SELECT d.owner_user_id IS NOT DISTINCT FROM openfox_private.current_actor_id()
		  FROM openfox.physical_devices d
		  JOIN openfox.device_grants g ON g.device_id = d.id
		 WHERE d.id = $1::uuid AND d.status = 'ACTIVE'
		   AND g.organization_id = openfox_private.current_organization_id() AND g.status = 'ACTIVE'`, deviceID).Scan(&owned)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !owned) {
		return ErrDeviceNotFound
	}
	return err
}

func (s PostgresStore) ListPendingCommands(ctx context.Context, selection tenancy.OrganizationSelection, limit int) ([]Command, error) {
	if limit == 0 {
		limit = DefaultCommandLimit
	}
	if limit < 1 || limit > MaxCommandLimit {
		return nil, ErrInvalidRequest
	}
	commands := make([]Command, 0)
	err := s.runForDevice(ctx, selection, func(ctx context.Context, tx pgx.Tx, deviceID string) error {
		if _, err := tx.Exec(ctx, expireOpenCommandsSQL, deviceID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+commandColumns+`
			  FROM openfox.device_commands
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND device_id = $1::uuid AND status = 'PENDING' AND expires_at > clock_timestamp()
			 ORDER BY created_at, id
			 LIMIT $2`, deviceID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			command, err := scanCommand(rows)
			if err != nil {
				return err
			}
			commands = append(commands, command)
		}
		return rows.Err()
	})
	return commands, err
}

func (s PostgresStore) ClaimCommand(ctx context.Context, selection tenancy.OrganizationSelection, commandID string) (Command, error) {
	commandID, err := normalizeUUID(commandID)
	if err != nil {
		return Command{}, ErrCommandNotFound
	}
	var command Command
	// outcome 是需要在提交（保留 EXPIRED 标记）之后才返回给调用方的业务错误。
	var outcome error
	err = s.runForDevice(ctx, selection, func(ctx context.Context, tx pgx.Tx, deviceID string) error {
		claimed, err := scanCommand(tx.QueryRow(ctx, `
			UPDATE openfox.device_commands
			   SET status = 'DELIVERED', delivered_at = statement_timestamp(),
			       -- 截止时间与 delivered_at 取同一个时间点，二者之差恰为执行超时 + 余量。
			       expires_at = CASE WHEN kind='workspace_path' THEN expires_at ELSE statement_timestamp() + make_interval(secs => CASE kind
			           WHEN 'storage_cleanup' THEN $3::double precision WHEN 'skill_import' THEN $3::double precision ELSE $4::double precision END) END
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND device_id = $1::uuid AND id = $2::uuid
			   AND status = 'PENDING' AND expires_at > clock_timestamp()
			 RETURNING `+commandColumns, deviceID, commandID,
			(CommandStorageCleanup.ExecutionTimeout() + commandExecutionGrace).Seconds(),
			(CommandRuntimeProbe.ExecutionTimeout() + commandExecutionGrace).Seconds()))
		if err == nil {
			command = claimed
			if claimed.Kind == CommandStorageCleanup {
				var f storage.CleanupFilter
				if err := json.Unmarshal(claimed.Params, &f); err != nil {
					return err
				}
				if len(f.Statuses) > 0 {
					// Only the owning device receives run IDs; the stored user command keeps its original filter.
					rows, err := tx.Query(ctx, `SELECT r.id::text FROM openfox.runs r
					 JOIN openfox.tasks t ON t.organization_id=r.organization_id AND t.id=r.task_id
					 JOIN openfox.task_statuses st ON st.organization_id=t.organization_id AND st.id=t.status_id
					 WHERE r.organization_id=openfox_private.current_organization_id() AND r.computer_id=$1::uuid
					 AND t.deleted_at IS NULL AND st.category=ANY($2::text[]) ORDER BY r.created_at DESC LIMIT 501`, deviceID, f.Statuses)
					if err != nil {
						return err
					}
					ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
					if err != nil {
						return err
					}
					command.Params, err = json.Marshal(storage.StatusCleanupClaim{Filter: claimed.Params, Runs: ids})
					if err != nil {
						return err
					}
				}
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		current, err := lockDeviceCommand(ctx, tx, deviceID, commandID)
		if err != nil {
			return err
		}
		switch {
		case current.Status == CommandPending:
			// UPDATE 未命中而仍为 PENDING，只可能是已过期。
			if err := markCommandExpired(ctx, tx, commandID); err != nil {
				return err
			}
			outcome = ErrCommandExpired
		case current.Status == CommandExpired:
			outcome = ErrCommandExpired
		default:
			outcome = ErrCommandClaimed
		}
		return nil
	})
	if err != nil {
		return Command{}, err
	}
	if outcome != nil {
		return Command{}, outcome
	}
	return command, nil
}

func (s PostgresStore) CompleteCommand(ctx context.Context, selection tenancy.OrganizationSelection, commandID string, input CommandResultInput) (Command, error) {
	commandID, err := normalizeUUID(commandID)
	if err != nil {
		return Command{}, ErrCommandNotFound
	}
	// 先按所有种类中最大的上限校验形状；具体种类的上限在读到命令后再卡。
	input, err = validateCommandResultLimit(input, maxSkillScanResultBytes)
	if err != nil {
		return Command{}, err
	}
	var command Command
	var outcome error
	err = s.runForDevice(ctx, selection, func(ctx context.Context, tx pgx.Tx, deviceID string) error {
		locked, err := lockDeviceCommand(ctx, tx, deviceID, commandID)
		if err != nil {
			return err
		}
		if len(input.Result) > locked.Kind.maxResultBytes() {
			outcome = ErrInvalidRequest
			return nil
		}
		if locked.Kind == CommandWorkspacePath && validateWorkDirectoryResult(input) != nil {
			outcome = ErrInvalidRequest
			return nil
		}
		if locked.Kind == CommandRuntimeSessionExport {
			var params RuntimeSessionExportParams
			if json.Unmarshal(locked.Params, &params) != nil || params.ExportID == "" {
				outcome = ErrInvalidRequest
				return nil
			}
			var exportStatus string
			exportErr := tx.QueryRow(ctx, `SELECT status FROM openfox.runtime_session_exports
				WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid
				  AND device_id=$2::uuid FOR UPDATE`, params.ExportID, deviceID).Scan(&exportStatus)
			if errors.Is(exportErr, pgx.ErrNoRows) {
				if input.Status == CommandSucceeded {
					input.Status, input.ErrorCode = CommandFailed, "EXPORT_EXPIRED"
				}
			} else if exportErr != nil {
				return exportErr
			} else {
				if input.Status == CommandSucceeded && exportStatus != "READY" {
					input.Status, input.ErrorCode = CommandFailed, "UPLOAD_MISSING"
				}
				if input.Status == CommandFailed && exportStatus == "PENDING" {
					errorCode := input.ErrorCode
					if errorCode == "" {
						errorCode = "DEVICE_EXPORT_FAILED"
					}
					if _, err := tx.Exec(ctx, `UPDATE openfox.runtime_session_exports
						SET status='FAILED', error_code=$3
						WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid
						  AND device_id=$2::uuid AND status='PENDING'`, params.ExportID, deviceID, errorCode); err != nil {
						return err
					}
				}
			}
		}
		var result any
		if len(input.Result) > 0 {
			result = []byte(input.Result)
		}
		completed, err := scanCommand(tx.QueryRow(ctx, `
			UPDATE openfox.device_commands
			   SET status = $3, result = $4::jsonb, error_code = $5, completed_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND device_id = $1::uuid AND id = $2::uuid
			   AND status = 'DELIVERED' AND expires_at > clock_timestamp()
			 RETURNING `+commandColumns, deviceID, commandID, input.Status, result, input.ErrorCode))
		if err == nil {
			command = completed
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		current, err := lockDeviceCommand(ctx, tx, deviceID, commandID)
		if err != nil {
			return err
		}
		switch current.Status {
		case CommandDelivered:
			// 已超过执行截止时间。
			if err := markCommandExpired(ctx, tx, commandID); err != nil {
				return err
			}
			outcome = ErrCommandExpired
		case CommandExpired:
			outcome = ErrCommandExpired
		case CommandSucceeded, CommandFailed:
			if current.Status == input.Status && current.ErrorCode == input.ErrorCode {
				// 重复回执相同终态：幂等返回已存结果。
				command = current
				return nil
			}
			outcome = ErrCommandConflict
		default:
			outcome = ErrCommandConflict
		}
		return nil
	})
	// 结果的 jsonb 存储大小超过迁移 device_commands_result_shape 的上限（文本接近上限的密集结果会触发）：按无效请求处理。
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "device_commands_result_shape" {
		return Command{}, ErrInvalidRequest
	}
	if err != nil {
		return Command{}, err
	}
	if outcome != nil {
		return Command{}, outcome
	}
	return command, nil
}

func lockDeviceCommand(ctx context.Context, tx pgx.Tx, deviceID, commandID string) (Command, error) {
	command, err := scanCommand(tx.QueryRow(ctx, `SELECT `+commandColumns+`
		  FROM openfox.device_commands
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND device_id = $1::uuid AND id = $2::uuid
		 FOR UPDATE`, deviceID, commandID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Command{}, ErrCommandNotFound
	}
	return command, err
}

func markCommandExpired(ctx context.Context, tx pgx.Tx, commandID string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.device_commands
		   SET status = 'EXPIRED', completed_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND status IN ('PENDING', 'DELIVERED')`, commandID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrCommandConflict
	}
	return nil
}

// runForDevice 以设备请求身份进入组织事务，并在租户上下文中复核授权仍为 ACTIVE 且版本未变
// （控制面选择组织与本事务之间授权可能被吊销）。
func (s PostgresStore) runForDevice(ctx context.Context, selection tenancy.OrganizationSelection, fn func(context.Context, pgx.Tx, string) error) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	deviceID, err := normalizeUUID(selection.ActorID)
	if err != nil || selection.AuthorizationVersion == 0 {
		return ErrInvalidRequest
	}
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
	return s.Pool.RunForOrganization(ctx, selection.OrganizationID, tenancy.SourceDeviceRequest, selector,
		func(ctx context.Context, tx pgx.Tx) error {
			var permissionVersion int64
			if err := tx.QueryRow(ctx, `
				SELECT permission_version
				  FROM openfox.device_grants
				 WHERE organization_id = openfox_private.current_organization_id()
				   AND device_id = $1::uuid AND status = 'ACTIVE'
				 FOR SHARE`, deviceID).Scan(&permissionVersion); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrGrantNotFound
				}
				return err
			}
			if permissionVersion <= 0 || uint64(permissionVersion) != selection.AuthorizationVersion {
				return ErrGrantNotFound
			}
			return fn(ctx, tx, deviceID)
		})
}
