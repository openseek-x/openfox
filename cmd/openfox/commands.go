package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/commands"
	"openfox/internal/daemon/storage"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/devices"
	"openfox/internal/runtime"
	"openfox/internal/runtime/codex"
	"openfox/internal/runtimeexports"
	"openfox/internal/runtimepolicy"
	"openfox/internal/workdirpath"
)

// heartbeatRequester 是 RemoteClient.RequestHeartbeat（测试可替换）。
type heartbeatRequester interface {
	RequestHeartbeat()
}

// commandHandlers 实现五种远程命令。都不调用模型、不消耗 token。
type commandHandlers struct {
	draftTask       commands.Handler
	workDirectory   func(context.Context, workdirpath.Scope) (string, error)
	reporter        *capabilityReporter
	storage         *storageReporter
	storagePolicy   func(context.Context, string, runtimepolicy.DeviceSettingsCommand) (supervisor.StoragePolicyResult, error)
	deviceSettings  func(context.Context, runtimepolicy.DeviceSettingsCommand) (supervisor.DeviceSettings, error)
	filteredCleanup func(context.Context, string, storage.CleanupFilter) (any, error)
	cleanup         func(context.Context) (supervisor.CleanupResult, error)
	heartbeat       heartbeatRequester
	now             func() time.Time
	// 阶段 5c：skill_scan / skill_import 使用的技能目录与中心上传端点。
	userSkillDirs   func() ([]string, error)
	uploader        skillUploader
	sessionExporter func(string) runtime.NativeSessionExporter
	sessionUpload   func(context.Context, string, string, string, []byte) error
	// 阶段 12e：key_rotate 执行两步式密钥轮换；只在 --config 模式下可用。
	keyRotate func(context.Context) (daemon.KeyRotationResult, error)
}

func newCommandExecutor(executor *executorRuntime, reporter *capabilityReporter, stats *storageReporter, heartbeat heartbeatRequester, organizations []string, rotator *daemon.KeyRotator) (*commands.Executor, error) {
	handlers := &commandHandlers{draftTask: executor.supervisor.DraftTask, workDirectory: executor.supervisor.WorkDirectoryPath, storagePolicy: executor.supervisor.StoragePolicy, reporter: reporter, storage: stats, cleanup: executor.supervisor.CleanupStorage, filteredCleanup: executor.supervisor.FilteredCleanup, deviceSettings: executor.supervisor.ConfigureDevice, heartbeat: heartbeat,
		userSkillDirs: executor.adapter.UserSkillDirs, uploader: executor.client,
		sessionExporter: executor.sessionExporter, sessionUpload: executor.client.UploadRuntimeSessionExport}
	if rotator != nil {
		handlers.keyRotate = rotator.Rotate
	}
	return commands.New(commands.Config{
		Client: executor.client, Organizations: organizations, Handlers: handlers.all(), Timeouts: commands.DefaultTimeouts(),
	})
}

func (h *commandHandlers) all() map[string]commands.Handler {
	handlers := h.baseHandlers()
	if h.keyRotate != nil {
		handlers[commands.KindKeyRotate] = func(ctx context.Context, raw json.RawMessage) (any, error) {
			var params map[string]json.RawMessage
			if err := json.Unmarshal(raw, &params); err != nil || len(params) != 0 {
				return nil, commands.ErrInvalidParams
			}
			result, err := h.keyRotate(ctx)
			if err != nil {
				code := "KEY_ROTATION_FAILED"
				if errors.Is(err, daemon.ErrKeyRotationUnsupported) {
					code = "KEY_ROTATION_UNSUPPORTED"
				}
				return nil, &commands.CodedError{Code: code}
			}
			// 只回报公钥摘要编号，不含任何密钥材料。
			return map[string]any{"key_id": result.KeyID, "previous_key_id": result.PreviousKeyID, "resumed": result.Resumed}, nil
		}
	}
	return handlers
}

func (h *commandHandlers) baseHandlers() map[string]commands.Handler {
	return map[string]commands.Handler{
		commands.KindWorkspacePath: h.workspacePath, commands.KindTaskDraft: h.draftTask,
		commands.KindDeviceSettings: func(ctx context.Context, raw json.RawMessage) (any, error) {
			v, e := runtimepolicy.ParseDeviceSettings(raw)
			if e != nil {
				return nil, commands.ErrInvalidParams
			}
			if v.Storage {
				org, _, ok := commands.FromContext(ctx)
				if !ok || h.storagePolicy == nil {
					return nil, commands.ErrInvalidParams
				}
				result, err := h.storagePolicy(ctx, org, v)
				if err != nil {
					return nil, &commands.CodedError{Code: "SETTINGS_REJECTED"}
				}
				return result, nil
			}
			result, e := h.deviceSettings(ctx, v)
			if e != nil {
				return nil, &commands.CodedError{Code: "SETTINGS_REJECTED"}
			}
			return result, nil
		},
		commands.KindRuntimeProbe:         h.runtimeProbe,
		commands.KindRuntimeTest:          h.runtimeTest,
		commands.KindStorageCleanup:       h.storageCleanup,
		commands.KindSkillScan:            h.skillScan,
		commands.KindSkillImport:          h.skillImport,
		commands.KindRuntimeSessionExport: h.runtimeSessionExport,
	}
}

func (h *commandHandlers) runtimeSessionExport(ctx context.Context, raw json.RawMessage) (any, error) {
	var input devices.RuntimeSessionExportParams
	if err := decodeParams(raw, &input); err != nil {
		return nil, err
	}
	organizationID, _, ok := commands.FromContext(ctx)
	if !ok || h.sessionExporter == nil || h.sessionUpload == nil {
		return nil, &commands.CodedError{Code: "EXPORT_UNAVAILABLE"}
	}
	exporter := h.sessionExporter(input.Runtime)
	archive, err := runtimeexports.Package(ctx, input.Runtime, input.SessionID, exporter)
	if err != nil {
		if errors.Is(err, runtime.ErrInvalidRequest) {
			return nil, commands.ErrInvalidParams
		}
		return nil, &commands.CodedError{Code: "SESSION_UNAVAILABLE"}
	}
	ciphertext, wrappedKey, err := runtimeexports.Encrypt(archive, input.PublicKey)
	if err != nil {
		return nil, &commands.CodedError{Code: "PUBLIC_KEY_INVALID"}
	}
	if len(ciphertext) == 0 || len(ciphertext) > 20*1024*1024 {
		return nil, &commands.CodedError{Code: "PACKAGE_TOO_LARGE"}
	}
	wrapped := base64.RawURLEncoding.EncodeToString(wrappedKey)
	if err := h.sessionUpload(ctx, organizationID, input.ExportID, wrapped, ciphertext); err != nil {
		return nil, &commands.CodedError{Code: "UPLOAD_FAILED"}
	}
	return map[string]any{"export_id": input.ExportID, "ready": true}, nil
}

func (h *commandHandlers) clock() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// runtimeProbe 重新检测 Runtime，结果与心跳 capabilities.runtimes 同结构；随后立即发一次心跳刷新 capabilities。
func (h *commandHandlers) runtimeProbe(ctx context.Context, params json.RawMessage) (any, error) {
	if err := emptyParams(params); err != nil {
		return nil, err
	}
	value, inventoryErr := h.reporter.refreshWithInventory(ctx)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if h.heartbeat != nil {
		h.heartbeat.RequestHeartbeat()
	}
	if inventoryErr != nil {
		return nil, inventoryErr
	}
	return map[string]any{"runtimes": h.reporter.allRuntimes(), "checked_at": value.CheckedAt}, nil
}

type runtimeTestResult struct {
	Provider       string `json:"provider"`
	OK             bool   `json:"ok"`
	LoginStatus    string `json:"login_status"`
	Model          string `json:"model"`
	ModelAvailable bool   `json:"model_available"`
	LatencyMS      int64  `json:"latency_ms"`
	Message        string `json:"message"`
}

// runtimeTest 只做 Runtime 自检：登录状态 + 模型列表 + 指定模型是否在列表中。
func (h *commandHandlers) runtimeTest(ctx context.Context, params json.RawMessage) (any, error) {
	var input struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if err := decodeParams(params, &input); err != nil {
		return nil, err
	}
	input.Model = strings.TrimSpace(input.Model)
	prober, known := h.reporter.proberFor(input.Provider)
	if !known || len(input.Model) > 128 {
		return nil, commands.ErrInvalidParams
	}
	if input.Provider != codex.RuntimeName {
		return h.otherRuntimeTest(ctx, prober, input.Provider, input.Model)
	}
	started := h.clock()
	result := runtimeTestResult{Provider: input.Provider, Model: input.Model, LoginStatus: string(runtime.LoginUnknown)}
	finish := func(message string) runtimeTestResult {
		result.LatencyMS = h.clock().Sub(started).Milliseconds()
		result.Message = boundedText(message)
		return result
	}
	report, err := h.reporter.prober.Detect(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return finish("Codex is not available on this computer: " + boundedText(err.Error())), nil
	}
	result.LoginStatus = string(report.Login)
	if !report.Runnable {
		return finish("Codex cannot run on this computer: " + report.Reason), nil
	}
	models, err := h.reporter.prober.ListModels(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return finish("Codex model list could not be read: " + boundedText(err.Error())), nil
	}
	if input.Model == "" || strings.EqualFold(input.Model, "default") {
		// 未指定模型：使用 Codex 默认模型，有可用模型即视为可用。
		result.ModelAvailable = len(models) > 0
	} else {
		for _, model := range models {
			if model.ID == input.Model {
				result.ModelAvailable = true
				break
			}
		}
	}
	switch {
	case !result.ModelAvailable && input.Model != "" && !strings.EqualFold(input.Model, "default"):
		return finish("model " + input.Model + " is not available for the Codex account on this computer"), nil
	case !result.ModelAvailable:
		return finish("no models are available for the Codex account on this computer"), nil
	}
	result.OK = true
	if report.Login == runtime.LoginUnknown {
		return finish("Codex configuration and model list are ready; authentication is verified when a request is sent"), nil
	}
	return finish("Codex is ready"), nil
}

// runtimeDisplayNames 是 Codex 之外 Runtime 的用户可见名称（用于自检说明）。
var runtimeDisplayNames = map[string]string{"claude-code": "Claude Code", "opencode": "OpenCode", "pi": "pi", "codebuddy": "CodeBuddy", "hermes": "Hermes", "gemini": "Gemini CLI"}

// otherRuntimeTest 是 Claude Code / OpenCode / pi 的自检：命令可执行、登录状态不是明确未登录；
// 指定模型时，模型列表有实际条目（不止 default）才校验模型是否存在。不调用模型。
func (h *commandHandlers) otherRuntimeTest(ctx context.Context, prober capabilityProber, provider, model string) (any, error) {
	started := h.clock()
	name := runtimeDisplayNames[provider]
	result := runtimeTestResult{Provider: provider, Model: model, LoginStatus: string(runtime.LoginUnknown)}
	finish := func(message string) runtimeTestResult {
		result.LatencyMS = h.clock().Sub(started).Milliseconds()
		result.Message = boundedText(message)
		return result
	}
	report, err := prober.Detect(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return finish(name + " is not available on this computer: " + boundedText(err.Error())), nil
	}
	result.LoginStatus = string(report.Login)
	if report.Login == runtime.LoginLoggedOut {
		return finish(name + " is not logged in on this computer"), nil
	}
	if provider == "gemini" && report.ProtocolReady && report.RequiresRunConfiguration {
		result.OK = true
		return finish("Gemini protocol is ready; the explicit Gemini API key and model are checked when a Run starts"), nil
	}
	if provider == "hermes" && report.ProtocolReady && report.RequiresRunConfiguration {
		result.OK = true
		return finish("Hermes protocol is ready; public HTTPS model configuration and declared context capacity are checked when a Run starts"), nil
	}
	if !report.Runnable {
		return finish(name + " cannot run here: " + report.Reason), nil
	}
	result.ModelAvailable = true
	if model != "" && !strings.EqualFold(model, "default") {
		models, listErr := prober.ListModels(ctx)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 列表取不到或只有内置默认项时无法判断，按可用处理（实际运行时由 Runtime 报告）。
		if listErr == nil && len(models) > 1 {
			result.ModelAvailable = false
			for _, item := range models {
				if item.ID == model {
					result.ModelAvailable = true
					break
				}
			}
			if !result.ModelAvailable {
				return finish("model " + model + " is not available for " + name + " on this computer"), nil
			}
		}
	}
	result.OK = true
	if report.Login == runtime.LoginUnknown {
		return finish(name + " is installed; its login status could not be verified"), nil
	}
	return finish(name + " is ready"), nil
}

// storageCleanup 清理已结束 Run 的 worktree、过期 CODEX_HOME 与 journal 旧记录，随后重新统计存储并发心跳。
func (h *commandHandlers) storageCleanup(ctx context.Context, params json.RawMessage) (any, error) {
	var result any
	var err error
	if emptyParams(params) == nil {
		result, err = h.cleanup(ctx)
	} else {
		f, e := storage.ParseClaimedFilter(params)
		org, _, ok := commands.FromContext(ctx)
		if e != nil || !ok || h.filteredCleanup == nil {
			return nil, commands.ErrInvalidParams
		}
		result, err = h.filteredCleanup(ctx, org, f)
		if errors.Is(err, supervisor.ErrCleanupChanged) {
			return nil, &commands.CodedError{Code: "PREVIEW_CHANGED"}
		}
		if f.Action == "preview" && err == nil {
			return result, nil
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &commands.CodedError{Code: "CLEANUP_FAILED", Result: result}
	}
	if h.storage != nil {
		h.storage.measure(ctx)
	}
	if h.heartbeat != nil {
		h.heartbeat.RequestHeartbeat()
	}
	return result, nil
}

func emptyParams(params json.RawMessage) error {
	var value map[string]json.RawMessage
	if err := decodeParams(params, &value); err != nil {
		return err
	}
	if len(value) != 0 {
		return commands.ErrInvalidParams
	}
	return nil
}

func decodeParams(params json.RawMessage, out any) error {
	if len(params) == 0 || string(params) == "null" {
		params = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(strings.NewReader(string(params)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return errors.Join(commands.ErrInvalidParams, errors.New("params must be a JSON object"))
	}
	return nil
}
