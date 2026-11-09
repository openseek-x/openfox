package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"openfox/internal/runtime"
)

// 执行步骤（schema v1）映射：item/started → running 步骤，增量通知按 item 缓冲后以 *_append 发出，
// item/completed → 终态步骤（完整字段）。映射失败只跳过步骤，不影响 Run 结果。
const (
	// 增量缓冲：累计到 stepFlushBytes 或距首个未发增量超过 stepFlushInterval 即发出。
	stepFlushInterval = 250 * time.Millisecond
	stepFlushBytes    = 4 * 1024
	// 单条 *_append 上限与单个步骤流式增量总量上限（超过后只等终态）。
	stepAppendChunk = 8 * 1024
	stepAppendTotal = 256 * 1024
	// 终态字段上限。单条事件需小于 supervisor 的 56 KiB data 上限。
	stepTextLimit       = 32 * 1024
	stepOutputLimit     = 32 * 1024
	stepToolOutputLimit = 8 * 1024
	stepDiffLimit       = 24 * 1024
	stepPatchBudget     = 40 * 1024
	stepCommandLimit    = 4 * 1024
	stepFieldLimit      = 1024
	stepPayloadLimit    = 48 * 1024
	stepMaxIDBytes      = 64
	stepSearchResults   = 20
	stepWebResults      = 10
	stepTodoItems       = 50
	// 解析搜索结果时最多读取的输出字节数。
	stepSearchScanBytes = 64 * 1024

	stepIDPlan    = "plan"
	stepIDTurnEnd = "turn_end"
)

// stepItem 是 ThreadItem 中步骤映射需要的字段；只对已处理的 item 类型解码，
// 避免不同类型的同名字段（如 userMessage.content）解码冲突。
type stepItem struct {
	Type             string                    `json:"type"`
	ID               string                    `json:"id"`
	Text             string                    `json:"text"`
	Phase            *string                   `json:"phase"`
	Summary          []string                  `json:"summary"`
	Content          []string                  `json:"content"`
	Command          string                    `json:"command"`
	Cwd              string                    `json:"cwd"`
	Status           string                    `json:"status"`
	CommandActions   []commandAction           `json:"commandActions"`
	AggregatedOutput *string                   `json:"aggregatedOutput"`
	ExitCode         *int                      `json:"exitCode"`
	DurationMs       *int64                    `json:"durationMs"`
	Changes          []fileChange              `json:"changes"`
	Server           string                    `json:"server"`
	Tool             string                    `json:"tool"`
	Namespace        *string                   `json:"namespace"`
	Result           *mcpResult                `json:"result"`
	Error            *struct{ Message string } `json:"error"`
	ContentItems     []dynamicContent          `json:"contentItems"`
	Success          *bool                     `json:"success"`
	Query            string                    `json:"query"`
	Action           *webAction                `json:"action"`
	Results          []json.RawMessage         `json:"results"`
}

type commandAction struct {
	Type    string  `json:"type"`
	Command string  `json:"command"`
	Name    string  `json:"name"`
	Path    *string `json:"path"`
	Query   *string `json:"query"`
}

type fileChange struct {
	Path string `json:"path"`
	Kind struct {
		Type     string  `json:"type"`
		MovePath *string `json:"move_path"`
	} `json:"kind"`
	Diff string `json:"diff"`
}

type mcpResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent"`
}

type dynamicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type webAction struct {
	Type    string   `json:"type"`
	Query   *string  `json:"query"`
	Queries []string `json:"queries"`
	URL     *string  `json:"url"`
}

// itemTrack 记录一个已开始、尚未完成的 item 对应的步骤与增量缓冲。
type itemTrack struct {
	// steps 是该 item 映射出的步骤（commandActions 可能拆成多个 read/search 步骤），只含 id 与 kind。
	steps     []runtime.Step
	startedAt time.Time
	closed    bool
	// 增量缓冲只用于单步骤 item（exec / thinking / agent_text）。
	buffer      strings.Builder
	bufferSince time.Time
	streamed    int
	// reasoning 的增量来源（summary 或 content），先到者为准，避免两路交错。
	source string
}

func (t *itemTrack) streamKind() string {
	if len(t.steps) != 1 {
		return ""
	}
	switch kind := t.steps[0].Kind; kind {
	case runtime.StepKindExec, runtime.StepKindThinking, runtime.StepKindAgentText:
		return kind
	}
	return ""
}

func (s *appServerSession) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func millisTime(ms int64, fallback time.Time) time.Time {
	if ms <= 0 {
		return fallback
	}
	return time.UnixMilli(ms).UTC()
}

// itemStarted 发出 running 步骤并登记缓冲。
func (s *appServerSession) itemStarted(raw json.RawMessage, at time.Time) []runtime.Event {
	item, ok := decodeStepItem(raw)
	if !ok || item.ID == "" {
		return nil
	}
	if s.items == nil {
		s.items = map[string]*itemTrack{}
	}
	if _, seen := s.items[item.ID]; seen {
		return nil
	}
	steps := s.startedSteps(item)
	if len(steps) == 0 {
		return nil
	}
	track := &itemTrack{startedAt: at}
	events := make([]runtime.Event, 0, len(steps))
	for _, step := range steps {
		track.steps = append(track.steps, runtime.Step{ID: step.ID, Kind: step.Kind})
		step.State, step.StartedAt = runtime.StepRunning, timePtr(at)
		events = append(events, s.stepEvent(step, at))
	}
	s.items[item.ID] = track
	s.order = append(s.order, item.ID)
	return events
}

// startedSteps 返回 item 开始时的步骤（不含 state / 时间）。
func (s *appServerSession) startedSteps(item stepItem) []runtime.Step {
	id := stepID(item.ID)
	switch item.Type {
	case "reasoning":
		return []runtime.Step{{ID: id, Kind: runtime.StepKindThinking}}
	case "agentMessage":
		if item.Phase != nil && *item.Phase == "commentary" {
			return []runtime.Step{{ID: id, Kind: runtime.StepKindAgentText}}
		}
		return nil
	case "commandExecution":
		if steps := s.activitySteps(item); steps != nil {
			return steps
		}
		return []runtime.Step{{ID: id, Kind: runtime.StepKindExec,
			Command: s.redact(boundBytes(item.Command, stepCommandLimit)), Cwd: s.relPath(item.Cwd)}}
	case "fileChange":
		step := runtime.Step{ID: id, Kind: runtime.StepKindPatch}
		step.Files = s.patchFiles(item.Changes)
		return []runtime.Step{step}
	case "webSearch":
		return []runtime.Step{s.webStep(item)}
	case "mcpToolCall", "dynamicToolCall":
		return []runtime.Step{{ID: id, Kind: runtime.StepKindExec, Command: s.redact(boundBytes(toolName(item), stepFieldLimit))}}
	default:
		return nil
	}
}

// activitySteps：commandActions 全是 read / search / listFiles 时，每个动作映射为一个 read / search 步骤；
// 否则返回 nil（按 exec 处理）。
func (s *appServerSession) activitySteps(item stepItem) []runtime.Step {
	if len(item.CommandActions) == 0 {
		return nil
	}
	steps := make([]runtime.Step, 0, len(item.CommandActions))
	for index, action := range item.CommandActions {
		id := item.ID
		if index > 0 {
			id = fmt.Sprintf("%s:%d", item.ID, index)
		}
		step := runtime.Step{ID: stepID(id)}
		switch action.Type {
		case "read":
			step.Kind, step.Path = runtime.StepKindRead, s.relPath(deref(action.Path))
		case "search":
			step.Kind, step.Query, step.Scope = runtime.StepKindSearch, s.redact(boundBytes(deref(action.Query), stepFieldLimit)), s.relPath(deref(action.Path))
		case "listFiles":
			step.Kind, step.Scope = runtime.StepKindSearch, s.relPath(deref(action.Path))
		default:
			return nil
		}
		steps = append(steps, step)
	}
	return steps
}

// itemCompleted 发出终态步骤；返回值只含步骤事件，旧事件由 itemEvent 负责。
func (s *appServerSession) itemCompleted(raw json.RawMessage, at time.Time) []runtime.Event {
	item, ok := decodeStepItem(raw)
	if !ok || item.ID == "" {
		return nil
	}
	track := s.items[item.ID]
	if track != nil && track.closed {
		return nil
	}
	var startedAt *time.Time
	if track != nil {
		startedAt = timePtr(track.startedAt)
		track.closed = true
		// 终态带完整字段，丢弃尚未发出的增量。
		track.buffer.Reset()
		s.removeOpen(item.ID)
	}
	steps := s.completedSteps(item, track != nil)
	if len(steps) == 0 && track != nil {
		// 已发出 running 但终态无可映射内容时，仍按原步骤结束，避免前端一直显示运行中。
		for _, base := range track.steps {
			steps = append(steps, runtime.Step{ID: base.ID, Kind: base.Kind, State: runtime.StepDone})
		}
	}
	events := make([]runtime.Event, 0, len(steps))
	for _, step := range steps {
		step.StartedAt, step.EndedAt = startedAt, timePtr(at)
		if step.DurationMS == nil && startedAt != nil {
			step.DurationMS = durationPtr(at.Sub(*startedAt))
		}
		events = append(events, s.stepEvent(step, at))
	}
	if track != nil {
		// 结束的 item 不再接收增量；保留空记录以忽略迟到的增量。
		s.items[item.ID] = &itemTrack{closed: true}
	}
	return events
}

func (s *appServerSession) completedSteps(item stepItem, started bool) []runtime.Step {
	id := stepID(item.ID)
	switch item.Type {
	case "reasoning":
		parts := append(append([]string(nil), nonEmpty(item.Summary)...), nonEmpty(item.Content)...)
		text := strings.Join(parts, "\n\n")
		if text == "" && !started {
			return nil
		}
		step := runtime.Step{ID: id, Kind: runtime.StepKindThinking, State: runtime.StepDone}
		step.Text, step.Truncated = s.boundText(text, stepTextLimit)
		return []runtime.Step{step}
	case "agentMessage":
		if item.Phase == nil || *item.Phase != "commentary" {
			if started {
				// 开始时是 commentary、结束时不是：只结束步骤，正文按最终回复处理。
				return []runtime.Step{{ID: id, Kind: runtime.StepKindAgentText, State: runtime.StepDone}}
			}
			return nil
		}
		step := runtime.Step{ID: id, Kind: runtime.StepKindAgentText, State: runtime.StepDone}
		step.Text, step.Truncated = s.boundText(item.Text, stepTextLimit)
		return []runtime.Step{step}
	case "commandExecution":
		state := commandState(item.Status, item.ExitCode)
		if steps := s.activitySteps(item); steps != nil {
			if len(steps) == 1 && steps[0].Kind == runtime.StepKindSearch && item.AggregatedOutput != nil {
				steps[0].SearchResults, steps[0].TotalFiles = s.searchResults(*item.AggregatedOutput, steps[0].Scope)
			}
			for index := range steps {
				steps[index].State = state
				steps[index].DurationMS = item.DurationMs
			}
			return steps
		}
		step := runtime.Step{ID: id, Kind: runtime.StepKindExec, State: state, DurationMS: item.DurationMs,
			Command: s.redact(boundBytes(item.Command, stepCommandLimit)), Cwd: s.relPath(item.Cwd), ExitCode: item.ExitCode}
		output := ""
		if item.AggregatedOutput != nil {
			output, step.Truncated = s.boundTail(*item.AggregatedOutput, stepOutputLimit)
		} else {
			step.TerminatedBeforeOutput = state != runtime.StepDone
		}
		step.Output = &output
		return []runtime.Step{step}
	case "fileChange":
		return []runtime.Step{{ID: id, Kind: runtime.StepKindPatch, State: patchState(item.Status), Files: s.patchFiles(item.Changes)}}
	case "webSearch":
		step := s.webStep(item)
		step.State = runtime.StepDone
		return []runtime.Step{step}
	case "mcpToolCall", "dynamicToolCall":
		step := runtime.Step{ID: id, Kind: runtime.StepKindExec, State: toolState(item), DurationMS: item.DurationMs,
			Command: s.redact(boundBytes(toolName(item), stepFieldLimit))}
		output, truncated := s.boundText(toolOutput(item), stepToolOutputLimit)
		step.Output, step.Truncated = &output, truncated
		return []runtime.Step{step}
	default:
		return nil
	}
}

// handleDelta 处理输出 / 推理 / commentary 增量；未登记或已结束的 item 忽略。
func (s *appServerSession) handleDelta(method string, raw json.RawMessage) []runtime.Event {
	var params struct {
		ThreadID string `json:"threadId"`
		ItemID   string `json:"itemId"`
		Delta    string `json:"delta"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.ItemID == "" {
		return nil
	}
	if s.threadID != "" && params.ThreadID != "" && params.ThreadID != s.threadID {
		return nil
	}
	track := s.items[params.ItemID]
	if track == nil || track.closed {
		return nil
	}
	kind := track.streamKind()
	switch method {
	case "item/commandExecution/outputDelta":
		if kind != runtime.StepKindExec {
			return nil
		}
	case "item/agentMessage/delta":
		if kind != runtime.StepKindAgentText {
			return nil
		}
	case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
		if kind != runtime.StepKindThinking {
			return nil
		}
		source := "summary"
		if method == "item/reasoning/textDelta" {
			source = "content"
		}
		if track.source == "" {
			track.source = source
		} else if track.source != source {
			return nil
		}
	case "item/reasoning/summaryPartAdded":
		if kind != runtime.StepKindThinking || track.source != "summary" || track.streamed+track.buffer.Len() == 0 {
			return nil
		}
		params.Delta = "\n\n"
	default:
		return nil
	}
	if params.Delta == "" || track.streamed+track.buffer.Len() >= stepAppendTotal {
		return nil
	}
	now := s.clock()
	if track.buffer.Len() == 0 {
		track.bufferSince = now
	}
	track.buffer.WriteString(params.Delta)
	if track.buffer.Len() >= stepFlushBytes || now.Sub(track.bufferSince) >= stepFlushInterval {
		return s.flushTrack(track, now)
	}
	return nil
}

// Flush 实现 runtime.FlushingSession：发出所有未发的增量。
func (s *appServerSession) Flush(now time.Time) []runtime.Event {
	var events []runtime.Event
	for _, itemID := range s.order {
		if track := s.items[itemID]; track != nil && !track.closed && track.buffer.Len() > 0 {
			events = append(events, s.flushTrack(track, now.UTC())...)
		}
	}
	return events
}

func (s *appServerSession) flushTrack(track *itemTrack, now time.Time) []runtime.Event {
	text := s.redact(track.buffer.String())
	track.buffer.Reset()
	text = boundBytes(text, stepAppendTotal-track.streamed)
	track.streamed += len(text)
	base := track.steps[0]
	var events []runtime.Event
	for text != "" {
		chunk := boundBytes(text, stepAppendChunk)
		if chunk == "" {
			break
		}
		text = text[len(chunk):]
		step := runtime.Step{ID: base.ID, Kind: base.Kind, State: runtime.StepRunning}
		if base.Kind == runtime.StepKindExec {
			step.OutputAppend = chunk
		} else {
			step.Append = chunk
		}
		events = append(events, s.stepEvent(step, now))
	}
	return events
}

// planUpdated 把 turn/plan/updated 映射为固定 id 的 todo 步骤。
func (s *appServerSession) planUpdated(raw json.RawMessage) ([]runtime.Event, error) {
	var params struct {
		ThreadID    string  `json:"threadId"`
		TurnID      string  `json:"turnId"`
		Explanation *string `json:"explanation"`
		Plan        []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, nil
	}
	if (s.threadID != "" && params.ThreadID != "" && params.ThreadID != s.threadID) || (s.turnID != "" && params.TurnID != "" && params.TurnID != s.turnID) {
		return nil, nil
	}
	now := s.clock()
	step := runtime.Step{ID: stepIDPlan, Kind: runtime.StepKindTodo, State: runtime.StepRunning, Items: []runtime.StepTodoItem{}}
	allDone := len(params.Plan) > 0
	for index, entry := range params.Plan {
		status := "pending"
		switch entry.Status {
		case "inProgress":
			status = "in_progress"
		case "completed":
			status = "completed"
		}
		if status != "completed" {
			allDone = false
		}
		if index < stepTodoItems {
			step.Items = append(step.Items, runtime.StepTodoItem{Text: s.redact(boundBytes(entry.Step, stepFieldLimit)), Status: status})
		}
	}
	if allDone {
		step.State = runtime.StepDone
	}
	if params.Explanation != nil {
		step.Explanation = s.redact(boundBytes(*params.Explanation, stepFieldLimit))
	}
	s.planSeen = true
	return []runtime.Event{s.stepEvent(step, now)}, nil
}

// tokenUsageUpdated 记录最近一次整轮累计用量（total），随结果上报。
func (s *appServerSession) tokenUsageUpdated(raw json.RawMessage) {
	var params struct {
		ThreadID   string `json:"threadId"`
		TokenUsage struct {
			Total *struct {
				InputTokens           int64 `json:"inputTokens"`
				CachedInputTokens     int64 `json:"cachedInputTokens"`
				CacheWriteInputTokens int64 `json:"cacheWriteInputTokens"`
				OutputTokens          int64 `json:"outputTokens"`
				ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
			} `json:"total"`
		} `json:"tokenUsage"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.TokenUsage.Total == nil {
		return
	}
	if s.threadID != "" && params.ThreadID != "" && params.ThreadID != s.threadID {
		return
	}
	total := params.TokenUsage.Total
	s.usage = &runtime.Usage{
		Input: nonNegative(total.InputTokens), CachedInput: nonNegative(total.CachedInputTokens),
		CacheWriteInput: nonNegative(total.CacheWriteInputTokens), Output: nonNegative(total.OutputTokens),
		ReasoningOutput: nonNegative(total.ReasoningOutputTokens),
	}
}

// Usage 实现 runtime.UsageReporter。
func (s *appServerSession) Usage() *runtime.Usage {
	if s.usage == nil {
		return nil
	}
	usage := *s.usage
	return &usage
}

// turnEnd 结束仍在运行的步骤（interrupted）并发出 turn_end；finalState 为 done / failed / cancelled。
// durationMS 为 nil 时按 turn/start 到现在计算。只发一次。
func (s *appServerSession) turnEnd(finalState string, durationMS *int64) []runtime.Event {
	if s.turnEnded {
		return nil
	}
	s.turnEnded = true
	now := s.clock()
	var events []runtime.Event
	for _, itemID := range s.order {
		track := s.items[itemID]
		if track == nil || track.closed {
			continue
		}
		track.closed = true
		track.buffer.Reset()
		for _, base := range track.steps {
			events = append(events, s.stepEvent(runtime.Step{ID: base.ID, Kind: base.Kind, State: runtime.StepInterrupted,
				StartedAt: timePtr(track.startedAt), EndedAt: timePtr(now), DurationMS: durationPtr(now.Sub(track.startedAt))}, now))
		}
	}
	s.order = nil
	step := runtime.Step{ID: stepIDTurnEnd, Kind: runtime.StepKindTurnEnd, FinalState: finalState, EndedAt: timePtr(now)}
	switch finalState {
	case "done":
		step.State = runtime.StepDone
	case "cancelled":
		step.State = runtime.StepInterrupted
	default:
		step.State = runtime.StepFailed
	}
	if !s.turnStarted.IsZero() {
		step.StartedAt = timePtr(s.turnStarted)
		if durationMS == nil {
			durationMS = durationPtr(now.Sub(s.turnStarted))
		}
	}
	if durationMS == nil {
		durationMS = durationPtr(0)
	}
	step.DurationMS = durationMS
	return append(events, s.stepEvent(step, now))
}

func (s *appServerSession) removeOpen(itemID string) {
	for index, id := range s.order {
		if id == itemID {
			s.order = append(s.order[:index], s.order[index+1:]...)
			return
		}
	}
}

// stepEvent 序列化步骤；超过 stepPayloadLimit 时逐步缩减大字段并标记 truncated。
func (s *appServerSession) stepEvent(step runtime.Step, at time.Time) runtime.Event {
	payload, err := runtime.StepPayload(step)
	for attempt := 0; (err != nil || len(payload) > stepPayloadLimit) && attempt < 4; attempt++ {
		step = shrinkStep(step)
		payload, err = runtime.StepPayload(step)
	}
	if err != nil || len(payload) > stepPayloadLimit {
		payload, _ = runtime.StepPayload(runtime.Step{ID: step.ID, Kind: step.Kind, State: step.State, Truncated: true})
	}
	return runtime.Event{Type: runtime.EventStep, At: at, Payload: payload}
}

// shrinkStep 把大字段减半（输出保留尾部），用于 JSON 转义导致超限的极端情况。
func shrinkStep(step runtime.Step) runtime.Step {
	half := func(text string) string { return boundBytes(text, len(text)/2) }
	step.Truncated = true
	step.Text, step.Content, step.Command = half(step.Text), half(step.Content), half(step.Command)
	step.Append, step.OutputAppend = half(step.Append), half(step.OutputAppend)
	if step.Output != nil {
		output := tailBytes(*step.Output, len(*step.Output)/2)
		step.Output = &output
	}
	files := make([]runtime.StepPatchFile, len(step.Files))
	for index, file := range step.Files {
		if len(file.Diff) > 0 {
			file.Diff, file.Truncated = half(file.Diff), true
		}
		files[index] = file
	}
	step.Files = files
	if len(step.SearchResults) > 5 {
		step.SearchResults = step.SearchResults[:5]
	}
	return step
}

func (s *appServerSession) webStep(item stepItem) runtime.Step {
	step := runtime.Step{ID: stepID(item.ID), Kind: runtime.StepKindWebSearch}
	query := item.Query
	if item.Action != nil {
		switch item.Action.Type {
		case "openPage", "findInPage":
			step.Kind, step.URL = runtime.StepKindWebFetch, s.redact(boundBytes(deref(item.Action.URL), stepFieldLimit*2))
			return step
		case "search":
			if query == "" {
				query = deref(item.Action.Query)
			}
			if query == "" && len(item.Action.Queries) > 0 {
				query = item.Action.Queries[0]
			}
		}
	}
	step.Query = s.redact(boundBytes(query, stepFieldLimit))
	for _, raw := range item.Results {
		if len(step.WebResults) >= stepWebResults {
			break
		}
		var result struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		}
		if json.Unmarshal(raw, &result) == nil && result.URL != "" {
			step.WebResults = append(step.WebResults, runtime.StepWebResult{
				Title: s.redact(boundBytes(result.Title, stepFieldLimit)), URL: s.redact(boundBytes(result.URL, stepFieldLimit*2))})
		}
	}
	return step
}

func (s *appServerSession) patchFiles(changes []fileChange) []runtime.StepPatchFile {
	files := make([]runtime.StepPatchFile, 0, len(changes))
	budget := stepPatchBudget
	for _, change := range changes {
		file := runtime.StepPatchFile{Path: s.relPath(change.Path), Kind: change.Kind.Type}
		if file.Kind != "add" && file.Kind != "delete" {
			file.Kind = "update"
		}
		if change.Kind.MovePath != nil {
			file.MovePath = s.relPath(*change.Kind.MovePath)
		}
		file.Additions, file.Deletions = diffStats(file.Kind, change.Diff)
		diff := s.redact(change.Diff)
		limit := stepDiffLimit
		if budget < limit {
			limit = budget
		}
		file.Diff = boundBytes(diff, limit)
		file.Truncated = len(file.Diff) < len(diff)
		budget -= len(file.Diff)
		files = append(files, file)
	}
	return files
}

// diffStats 统计增删行数。add / delete 的 diff 可能是整份文件内容（无 @@ 块），按行数计。
func diffStats(kind, diff string) (int, int) {
	if diff == "" {
		return 0, 0
	}
	if !strings.Contains(diff, "@@") {
		lines := strings.Count(strings.TrimSuffix(diff, "\n"), "\n") + 1
		switch kind {
		case "add":
			return lines, 0
		case "delete":
			return 0, lines
		}
	}
	var additions, deletions int
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "):
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return additions, deletions
}

// searchResults 从 rg / grep 风格输出（file:line:text 或纯文件列表）汇总每个文件的匹配数。
func (s *appServerSession) searchResults(output, scope string) ([]runtime.StepSearchResult, *int) {
	output = boundBytes(output, stepSearchScanBytes)
	counts := map[string]int{}
	var order []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		file, matched := line, false
		if prefix, _, found := strings.Cut(line, ":"); found {
			if prefix == "" || allDigits(prefix) {
				// 单文件搜索的输出没有文件名前缀，计入搜索范围。
				file, matched = scope, true
			} else {
				file, matched = prefix, true
			}
		}
		if file == "" {
			continue
		}
		if _, seen := counts[file]; !seen {
			order = append(order, file)
			counts[file] = 0
		}
		if matched {
			counts[file]++
		}
	}
	if len(order) == 0 {
		return nil, intPtr(0)
	}
	results := make([]runtime.StepSearchResult, 0, min(len(order), stepSearchResults))
	for _, file := range order {
		if len(results) >= stepSearchResults {
			break
		}
		results = append(results, runtime.StepSearchResult{File: s.relPath(boundBytes(file, stepFieldLimit)), Matches: counts[file]})
	}
	return results, intPtr(len(order))
}

// relPath 把工作目录下的绝对路径改为相对路径（工作目录本身为 "."），避免把本机目录结构发给中心；
// 工作目录外的路径原样保留。结果已脱敏。
func (s *appServerSession) relPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if s.cwd != "" && filepath.IsAbs(path) {
		root := filepath.Clean(s.cwd)
		clean := filepath.Clean(path)
		if clean == root {
			return "."
		}
		if strings.HasPrefix(clean, root+string(filepath.Separator)) {
			path = clean[len(root)+1:]
		}
	}
	return s.redact(boundBytes(path, stepFieldLimit))
}

func (s *appServerSession) boundText(text string, limit int) (string, bool) {
	text = s.redact(text)
	bounded := boundBytes(text, limit)
	return bounded, len(bounded) < len(text)
}

// boundTail 保留输出尾部（错误信息通常在末尾）。
func (s *appServerSession) boundTail(text string, limit int) (string, bool) {
	text = s.redact(text)
	bounded := tailBytes(text, limit)
	return bounded, len(bounded) < len(text)
}

func decodeStepItem(raw json.RawMessage) (stepItem, bool) {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return stepItem{}, false
	}
	switch head.Type {
	case "reasoning", "agentMessage", "commandExecution", "fileChange", "webSearch", "mcpToolCall", "dynamicToolCall":
	default:
		return stepItem{}, false
	}
	var item stepItem
	if json.Unmarshal(raw, &item) != nil {
		return stepItem{}, false
	}
	return item, true
}

func commandState(status string, exitCode *int) string {
	switch status {
	case "inProgress":
		return runtime.StepRunning
	case "failed":
		return runtime.StepFailed
	case "declined":
		return runtime.StepDenied
	}
	if exitCode != nil && *exitCode != 0 {
		return runtime.StepFailed
	}
	return runtime.StepDone
}

func patchState(status string) string {
	switch status {
	case "inProgress":
		return runtime.StepRunning
	case "failed":
		return runtime.StepFailed
	case "declined":
		return runtime.StepDenied
	}
	return runtime.StepDone
}

func toolState(item stepItem) string {
	switch {
	case item.Status == "inProgress":
		return runtime.StepRunning
	case item.Status == "failed", item.Success != nil && !*item.Success, item.Error != nil:
		return runtime.StepFailed
	}
	return runtime.StepDone
}

func toolName(item stepItem) string {
	switch {
	case item.Type == "mcpToolCall" && item.Server != "":
		return item.Server + "." + item.Tool
	case item.Namespace != nil && *item.Namespace != "":
		return *item.Namespace + "." + item.Tool
	}
	return item.Tool
}

// toolOutput 返回工具结果的文本摘要：MCP 的 text 内容、结构化结果或错误说明；动态工具的 inputText。
func toolOutput(item stepItem) string {
	if item.Error != nil && item.Error.Message != "" {
		return item.Error.Message
	}
	var parts []string
	if item.Result != nil {
		for _, raw := range item.Result.Content {
			var content dynamicContent
			if json.Unmarshal(raw, &content) == nil && content.Type == "text" && content.Text != "" {
				parts = append(parts, content.Text)
			}
		}
		if len(parts) == 0 && len(item.Result.StructuredContent) > 0 && string(item.Result.StructuredContent) != "null" {
			parts = append(parts, string(item.Result.StructuredContent))
		}
	}
	for _, content := range item.ContentItems {
		if content.Type == "inputText" && content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// stepID 保证 id 非空且不超过 64 字节；过长时用哈希代替（同一原 id 总是得到同一结果）。
func stepID(raw string) string {
	if raw != "" && len(raw) <= stepMaxIDBytes {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	return "h-" + hex.EncodeToString(sum[:])[:40]
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
	for cut < len(text) && !utf8Start(text[cut]) {
		cut++
	}
	return text[cut:]
}

func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func allDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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

func intPtr(value int) *int { return &value }
