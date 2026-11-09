package cliagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"openfox/internal/runtime"
)

// 终态字段上限，与 Codex 适配器一致；单条事件需小于 supervisor 的 56 KiB data 上限。
const (
	stepTextLimit    = 32 * 1024
	stepOutputLimit  = 24 * 1024
	stepCommandLimit = 4 * 1024
	stepFieldLimit   = 1024
	stepPayloadLimit = 48 * 1024
	stepMaxIDBytes   = 64
	stepTodoItems    = 50
	// maxOpenSteps 限制同时未结束的步骤数，防止异常输出撑爆内存。
	maxOpenSteps = 256
)

const stepIDTurnEnd = "turn_end"

// openStep 是已发出 running、尚未结束的步骤。
type openStep struct {
	step      runtime.Step
	startedAt time.Time
}

// session 实现 runtime.Session：把 stdout 的 JSONL 行交给协议的 parse 函数，
// 并维护步骤状态（开始 / 结束 / 取消时的 interrupted）。所有方法都在进程监督协程中串行调用。
type session struct {
	proto  protocol
	cwd    string
	redact func(string) string
	now    func() time.Time

	open      map[string]*openStep
	order     []string
	turnStart time.Time
	turnEnded bool

	// 协议共用的状态。
	sessionID string
	usage     runtime.Usage
	hasUsage  bool
	// pendingText 是尚未确定是中间说明还是最终回复的助手文本。
	pendingText strings.Builder
	finished    bool
	// lastError 是协议报告的最近一次错误说明。
	lastError string
	// finalOutput 在终态时填充，由协议在发出 EventOutput 前设置。
	counter int
}

func newSession(proto protocol, cwd string, redact func(string) string) *session {
	return &session{proto: proto, cwd: cwd, redact: redact, open: map[string]*openStep{}}
}

func (s *session) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// Begin：提示经 stdin 或参数传入，不需要额外握手。
func (s *session) Begin(runtime.Sender, func(error)) error {
	s.turnStart = s.clock()
	return nil
}

func (s *session) HandleLine(line []byte, _ runtime.Sender) ([]runtime.Event, bool, error) {
	line = []byte(strings.TrimSpace(string(line)))
	if len(line) == 0 || s.finished {
		return nil, s.finished, nil
	}
	// 非 JSON 行（例如 CLI 的启动横幅）忽略；协议错误由「无终态」判定。
	if line[0] != '{' || !json.Valid(line) {
		return nil, false, nil
	}
	events, done := s.proto.parse(s, line)
	if done {
		s.finished = true
	}
	return events, done, nil
}

// Interrupt：不做协议层中断，由进程层随后向进程组发信号（SIGINT → SIGTERM → SIGKILL）。
func (s *session) Interrupt(runtime.Sender) bool { return false }

// Finalize 在进程退出后调用：被取消 / 超时时把仍在运行的步骤收尾；
// 正常退出而协议没有给出终态时，交给协议的 finalize 判定。
func (s *session) Finalize(stopped string) []runtime.Event {
	switch stopped {
	case "cancelled":
		return s.turnEnd("cancelled")
	case "timeout":
		return s.turnEnd("failed")
	}
	if s.finished || s.proto.finalize == nil {
		return nil
	}
	s.finished = true
	return s.proto.finalize(s)
}

func (s *session) Usage() *runtime.Usage {
	if !s.hasUsage {
		return nil
	}
	usage := s.usage
	return &usage
}

func (s *session) SessionID() string { return s.sessionID }

func (s *session) addUsage(input, cached, cacheWrite, output, reasoning int64) {
	s.hasUsage = true
	s.usage.Input += nonNegative(input)
	s.usage.CachedInput += nonNegative(cached)
	s.usage.CacheWriteInput += nonNegative(cacheWrite)
	s.usage.Output += nonNegative(output)
	s.usage.ReasoningOutput += nonNegative(reasoning)
}

// setSession 记录 Runtime 会话 ID，并在首次得到时发出 started 事件。
func (s *session) setSession(id string) []runtime.Event {
	id = strings.TrimSpace(id)
	if id == "" || s.sessionID != "" || len(id) > 256 {
		return nil
	}
	s.sessionID = id
	payload, _ := json.Marshal(map[string]string{"session_id": id})
	return []runtime.Event{{Type: runtime.EventStarted, Payload: payload}}
}

// startStep 发出 running 步骤。同 id 已开始时忽略。
func (s *session) startStep(id string, step runtime.Step) []runtime.Event {
	id = stepID(id)
	if _, ok := s.open[id]; ok || len(s.open) >= maxOpenSteps {
		return nil
	}
	now := s.clock()
	step.ID, step.State, step.StartedAt = id, runtime.StepRunning, timePtr(now)
	s.open[id] = &openStep{step: runtime.Step{ID: id, Kind: step.Kind, Command: step.Command, Path: step.Path,
		Query: step.Query, URL: step.URL, Scope: step.Scope, Items: step.Items}, startedAt: now}
	s.order = append(s.order, id)
	return []runtime.Event{s.stepEvent(step, now)}
}

// finishStep 结束步骤。未曾 start 过时直接发出终态步骤。output 为工具输出文本（仅 exec 使用）。
func (s *session) finishStep(id string, fallback runtime.Step, state, output string, exitCode *int) []runtime.Event {
	id = stepID(id)
	now := s.clock()
	step := fallback
	startedAt := now
	if open, ok := s.open[id]; ok {
		step = open.step
		startedAt = open.startedAt
		delete(s.open, id)
		for index, value := range s.order {
			if value == id {
				s.order = append(s.order[:index], s.order[index+1:]...)
				break
			}
		}
	}
	step.ID, step.State = id, state
	step.StartedAt, step.EndedAt = timePtr(startedAt), timePtr(now)
	step.DurationMS = durationPtr(now.Sub(startedAt))
	switch step.Kind {
	case runtime.StepKindExec:
		step.Command = s.redact(boundBytes(step.Command, stepCommandLimit))
		text := s.redact(output)
		bounded := tailBytes(text, stepOutputLimit)
		step.Output, step.Truncated = &bounded, len(bounded) < len(text)
		step.ExitCode = exitCode
		step.TerminatedBeforeOutput = false
	case runtime.StepKindWriteFile:
		applied := state == runtime.StepDone
		step.Applied = &applied
	}
	return []runtime.Event{s.stepEvent(step, now)}
}

// textStep 发出已完成的 thinking / agent_text 步骤。
func (s *session) textStep(kind, text string) []runtime.Event {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	now := s.clock()
	s.counter++
	step := runtime.Step{ID: stepID(kind + "-" + strconv.Itoa(s.counter)), Kind: kind, State: runtime.StepDone, EndedAt: timePtr(now)}
	redacted := s.redact(text)
	step.Text = boundBytes(redacted, stepTextLimit)
	step.Truncated = len(step.Text) < len(redacted)
	return []runtime.Event{s.stepEvent(step, now)}
}

// flushPending 把挂起的助手文本作为中间说明（agent_text 步骤）发出。
func (s *session) flushPending() []runtime.Event {
	text := s.pendingText.String()
	s.pendingText.Reset()
	return s.textStep(runtime.StepKindAgentText, text)
}

// takePending 取出挂起文本作为最终回复。
func (s *session) takePending() string {
	text := strings.TrimSpace(s.pendingText.String())
	s.pendingText.Reset()
	return text
}

// complete 发出最终回复（output）、turn_end 与 completed。
func (s *session) complete(output string) []runtime.Event {
	events := s.turnEnd("done")
	if output = strings.TrimSpace(output); output != "" {
		events = append(events, runtime.Event{Type: runtime.EventOutput, Text: s.redact(output)})
	}
	return append(events, runtime.Event{Type: runtime.EventCompleted})
}

// fail 发出 turn_end(failed) 与 failed 事件。
func (s *session) fail(text string) []runtime.Event {
	text = strings.TrimSpace(s.redact(text))
	if text == "" {
		text = s.proto.display + " run failed"
	}
	if len(text) > 4096 {
		text = boundBytes(text, 4096)
	}
	return append(s.turnEnd("failed"), runtime.Event{Type: runtime.EventFailed, Text: text})
}

// turnEnd 把仍在运行的步骤标记为 interrupted 并发出 turn_end；只发一次。
func (s *session) turnEnd(finalState string) []runtime.Event {
	if s.turnEnded {
		return nil
	}
	s.turnEnded = true
	now := s.clock()
	var events []runtime.Event
	for _, id := range s.order {
		open := s.open[id]
		if open == nil {
			continue
		}
		step := open.step
		step.State, step.StartedAt, step.EndedAt = runtime.StepInterrupted, timePtr(open.startedAt), timePtr(now)
		step.DurationMS = durationPtr(now.Sub(open.startedAt))
		events = append(events, s.stepEvent(step, now))
	}
	s.open, s.order = map[string]*openStep{}, nil
	step := runtime.Step{ID: stepIDTurnEnd, Kind: runtime.StepKindTurnEnd, FinalState: finalState, EndedAt: timePtr(now)}
	switch finalState {
	case "done":
		step.State = runtime.StepDone
	case "cancelled":
		step.State = runtime.StepInterrupted
	default:
		step.State = runtime.StepFailed
	}
	if !s.turnStart.IsZero() {
		step.StartedAt = timePtr(s.turnStart)
		step.DurationMS = durationPtr(now.Sub(s.turnStart))
	} else {
		step.DurationMS = durationPtr(0)
	}
	return append(events, s.stepEvent(step, now))
}

// stepEvent 序列化步骤；超过 stepPayloadLimit 时缩减大字段并标记 truncated。
func (s *session) stepEvent(step runtime.Step, at time.Time) runtime.Event {
	payload, err := runtime.StepPayload(step)
	for attempt := 0; (err != nil || len(payload) > stepPayloadLimit) && attempt < 4; attempt++ {
		step.Truncated = true
		step.Text, step.Command = boundBytes(step.Text, len(step.Text)/2), boundBytes(step.Command, len(step.Command)/2)
		if step.Output != nil {
			output := tailBytes(*step.Output, len(*step.Output)/2)
			step.Output = &output
		}
		payload, err = runtime.StepPayload(step)
	}
	if err != nil || len(payload) > stepPayloadLimit {
		payload, _ = runtime.StepPayload(runtime.Step{ID: step.ID, Kind: step.Kind, State: step.State, Truncated: true})
	}
	return runtime.Event{Type: runtime.EventStep, At: at, Payload: payload}
}

// relPath 把工作目录内的绝对路径转为相对路径，目录外的路径原样返回（已脱敏）。
func (s *session) relPath(path string) string {
	path = strings.TrimSpace(path)
	if s.cwd != "" && strings.HasPrefix(path, s.cwd+"/") {
		path = strings.TrimPrefix(path, s.cwd+"/")
	}
	return s.redact(boundBytes(path, stepFieldLimit))
}

// toolStep 按工具名把调用映射为步骤（只含开始时已知的字段）。名称不区分大小写，
// 同时覆盖 Claude Code（Bash/Read/...）、OpenCode 与 pi（bash/read/...）的内置工具。
func (s *session) toolStep(name string, input map[string]any) runtime.Step {
	lower := strings.ToLower(name)
	str := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := input[key].(string); ok && value != "" {
				return value
			}
		}
		return ""
	}
	switch lower {
	case "read", "view":
		return runtime.Step{Kind: runtime.StepKindRead, Path: s.relPath(str("file_path", "filePath", "path"))}
	case "write", "edit", "multiedit", "notebookedit", "patch", "apply_patch":
		return runtime.Step{Kind: runtime.StepKindWriteFile, Path: s.relPath(str("file_path", "filePath", "path", "notebook_path"))}
	case "glob", "grep", "find", "ls", "list":
		query := str("pattern", "query", "regex")
		return runtime.Step{Kind: runtime.StepKindSearch, Query: s.redact(boundBytes(query, stepFieldLimit)),
			Scope: s.relPath(str("path", "directory"))}
	case "websearch":
		return runtime.Step{Kind: runtime.StepKindWebSearch, Query: s.redact(boundBytes(str("query"), stepFieldLimit))}
	case "webfetch":
		return runtime.Step{Kind: runtime.StepKindWebFetch, URL: s.redact(boundBytes(str("url"), stepFieldLimit))}
	case "todowrite", "todo_write":
		step := runtime.Step{Kind: runtime.StepKindTodo}
		if todos, ok := input["todos"].([]any); ok {
			for _, raw := range todos {
				item, _ := raw.(map[string]any)
				text, _ := item["content"].(string)
				if text == "" {
					continue
				}
				if len(step.Items) >= stepTodoItems {
					break
				}
				status, _ := item["status"].(string)
				step.Items = append(step.Items, runtime.StepTodoItem{Text: s.redact(boundBytes(text, stepFieldLimit)), Status: todoStatus(status)})
			}
		}
		return step
	case "bash", "shell":
		return runtime.Step{Kind: runtime.StepKindExec, Command: s.redact(boundBytes(str("command", "cmd"), stepCommandLimit)), Cwd: ""}
	default:
		return runtime.Step{Kind: runtime.StepKindExec, Command: s.redact(boundBytes(name, stepFieldLimit))}
	}
}

func todoStatus(value string) string {
	switch value {
	case "completed", "done":
		return "completed"
	case "in_progress", "inProgress":
		return "in_progress"
	default:
		return "pending"
	}
}

func stepID(raw string) string {
	if raw != "" && len(raw) <= stepMaxIDBytes {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	return "h-" + hex.EncodeToString(sum[:])[:40]
}

func boundBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	if limit <= 0 {
		return ""
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// tailBytes 保留末尾不超过 limit 字节，且不切断 UTF-8 字符。
func tailBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	if limit <= 0 {
		return ""
	}
	cut := len(text) - limit
	for cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut++
	}
	return text[cut:]
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func timePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}

func durationPtr(value time.Duration) *int64 {
	ms := value.Milliseconds()
	if ms < 0 {
		ms = 0
	}
	return &ms
}
