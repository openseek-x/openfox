package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"openfox/internal/runtime"
	"openfox/internal/runtimepolicy"
)

// 模型列表的上限：条数、单页请求数与字段长度（与 Agent 配置 model 字段的 128 字符上限一致）。
const (
	maxListedModels    = 100
	maxModelListPages  = 5
	maxModelFieldRunes = 128
	modelListTimeout   = 30 * time.Second
)

// Model 是 Codex 报告的一个可选模型。ID 即 Agent 配置中保存的 model 值。
type Model struct {
	ID                     string   `json:"id"`
	DisplayName            string   `json:"display_name"`
	IsDefault              bool     `json:"is_default"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
}

// ListModels 通过 `codex app-server` 的 model/list 读取当前登录账号可用的模型，不调用模型。
// 与 Start 一样使用独立 CODEX_HOME（model/list 需要登录凭据）。
func (a *Adapter) ListModels(ctx context.Context) ([]Model, error) {
	if a == nil {
		return nil, runtime.ErrUnavailable
	}
	capabilities, err := a.Detect(ctx)
	if err != nil {
		return nil, err
	}
	if capabilities.Login != runtime.LoginLoggedIn {
		return nil, fmt.Errorf("%w: Codex login is not available", runtime.ErrUnavailable)
	}
	prepared, err := a.prepare(capabilities.Executable, nil, nil)
	if err != nil {
		return nil, err
	}
	session := &modelListSession{}
	// 不需要工作目录：以本次独立 CODEX_HOME 作为 cwd，结束后一并删除。
	execution, err := runtime.StartProcess(ctxOrBackground(ctx), runtime.ProcessSpec{
		Executable: prepared.path, Args: []string{"app-server"}, Directory: prepared.home.dir, Environment: prepared.env,
		Timeout: modelListTimeout, MaxLineBytes: runtime.DefaultMaxLineBytes, MaxOutputBytes: runtime.DefaultMaxOutput,
		MaxEvents: runtime.DefaultMaxEvents, RequireCompletion: true, Redact: prepared.redact, Session: session,
	})
	if err != nil {
		prepared.home.remove()
		return nil, err
	}
	result, waitErr := execution.Wait(ctxOrBackground(ctx))
	if waitErr != nil {
		_ = execution.Cancel(context.Background())
		_, _ = execution.Wait(context.Background())
	}
	_, _ = prepared.home.finish()
	if waitErr != nil {
		return nil, waitErr
	}
	if result.Status != runtime.StatusSucceeded {
		return nil, fmt.Errorf("%w: codex model/list %s: %s", runtime.ErrUnavailable, strings.ToLower(string(result.Status)), result.Error)
	}
	return session.models, nil
}

// modelListSession：initialize → initialized → model/list（按 nextCursor 翻页）→ 结束。
// 字段由进程监督协程串行写入；ListModels 在 Wait 返回后读取。
type modelListSession struct {
	models []Model
	pages  int
}

func (s *modelListSession) Begin(send runtime.Sender, _ func(error)) error {
	return sendRequest(send, requestInitialize, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "openfox_daemon", "title": "OpenFox", "version": "1"},
		"capabilities": nil,
	})
}

func (s *modelListSession) HandleLine(line []byte, send runtime.Sender) ([]runtime.Event, bool, error) {
	var message rpcMessage
	if err := json.Unmarshal(line, &message); err != nil {
		return nil, false, fmt.Errorf("%w: invalid codex app-server message", runtime.ErrProtocol)
	}
	hasID := len(message.ID) > 0 && string(message.ID) != "null"
	if hasID && message.Method != "" {
		return nil, false, replyUnsupported(send, message.ID)
	}
	if !hasID {
		return nil, false, nil
	}
	id, err := strconv.Atoi(string(message.ID))
	if err != nil {
		return nil, false, fmt.Errorf("%w: unexpected codex app-server response id", runtime.ErrProtocol)
	}
	if message.Error != nil {
		return []runtime.Event{{Type: runtime.EventFailed, Text: "codex app-server model/list failed: " + message.Error.Message}}, true, nil
	}
	if id == requestInitialize {
		if err := send([]byte(`{"method":"initialized"}`)); err != nil {
			return nil, false, err
		}
		return nil, false, s.requestPage(send, nil)
	}
	var page struct {
		Data []struct {
			ID                        string `json:"id"`
			Model                     string `json:"model"`
			DisplayName               string `json:"displayName"`
			Hidden                    bool   `json:"hidden"`
			IsDefault                 bool   `json:"isDefault"`
			DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
			SupportedReasoningEfforts []struct {
				ReasoningEffort string `json:"reasoningEffort"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
		NextCursor *string `json:"nextCursor"`
	}
	if err := json.Unmarshal(message.Result, &page); err != nil {
		return nil, false, fmt.Errorf("%w: invalid model/list response", runtime.ErrProtocol)
	}
	for _, item := range page.Data {
		id := cleanModelField(item.ID)
		if id == "" {
			id = cleanModelField(item.Model)
		}
		if id == "" || item.Hidden || len(s.models) >= maxListedModels {
			continue
		}
		name := cleanModelField(item.DisplayName)
		if name == "" {
			name = id
		}
		var efforts []string
		for _, option := range item.SupportedReasoningEfforts {
			efforts = append(efforts, modelReasoningEffort(option.ReasoningEffort))
		}
		s.models = append(s.models, Model{ID: id, DisplayName: name, IsDefault: item.IsDefault,
			ReasoningEfforts:       runtimepolicy.NormalizeReasoningEfforts(efforts),
			DefaultReasoningEffort: runtimepolicy.NormalizeDefaultReasoningEffort(modelReasoningEffort(item.DefaultReasoningEffort), efforts)})
	}
	if page.NextCursor != nil && *page.NextCursor != "" && s.pages < maxModelListPages && len(s.models) < maxListedModels {
		return nil, false, s.requestPage(send, page.NextCursor)
	}
	return []runtime.Event{{Type: runtime.EventCompleted}}, true, nil
}

func (s *modelListSession) requestPage(send runtime.Sender, cursor *string) error {
	s.pages++
	params := map[string]any{"limit": maxListedModels, "includeHidden": false}
	if cursor != nil {
		params["cursor"] = *cursor
	}
	// 每页使用递增 ID（2、3、…），响应按 ID 区分即可，不依赖内容。
	return sendRequest(send, requestInitialize+s.pages, "model/list", params)
}

func (s *modelListSession) Interrupt(runtime.Sender) bool { return false }

func modelReasoningEffort(value string) string {
	effort := strings.ToUpper(value)
	if effort == "XHIGH" {
		return runtimepolicy.ReasoningExtraHigh
	}
	return effort
}

// cleanModelField 去掉控制字符并限制长度，避免把异常内容写进心跳与 Agent 配置。
func cleanModelField(value string) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value))
	if runes := []rune(value); len(runes) > maxModelFieldRunes {
		value = string(runes[:maxModelFieldRunes])
	}
	return value
}
