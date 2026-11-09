package codex

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	domainruntime "openfox/internal/runtime"
)

// stepClock 是可手动推进的测试时钟。
type stepClock struct{ at time.Time }

func (c *stepClock) now() time.Time { return c.at }

func newStepSession() (*appServerSession, *stepClock) {
	clock := &stepClock{at: time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)}
	session := &appServerSession{
		cwd: "/work/repo", threadID: "th-1", turnID: "tu-1", now: clock.now, turnStarted: clock.at,
		redact: func(text string) string { return strings.ReplaceAll(text, "s3cr3t", "[REDACTED]") },
	}
	return session, clock
}

type decodedStep struct {
	Schema int            `json:"schema"`
	Step   map[string]any `json:"step"`
}

// feed 逐行送入通知，返回其中的步骤（按顺序）与旧事件。
func feed(t *testing.T, session *appServerSession, lines ...string) ([]map[string]any, []domainruntime.Event) {
	t.Helper()
	var steps []map[string]any
	var legacy []domainruntime.Event
	for _, line := range lines {
		events, _, err := session.HandleLine([]byte(line), (&capturedSender{}).send)
		if err != nil {
			t.Fatalf("HandleLine(%s): %v", line, err)
		}
		s, l := splitEvents(t, events)
		steps, legacy = append(steps, s...), append(legacy, l...)
	}
	return steps, legacy
}

func splitEvents(t *testing.T, events []domainruntime.Event) ([]map[string]any, []domainruntime.Event) {
	t.Helper()
	var steps []map[string]any
	var legacy []domainruntime.Event
	for _, event := range events {
		if event.Type != domainruntime.EventStep {
			legacy = append(legacy, event)
			continue
		}
		if len(event.Payload) > stepPayloadLimit {
			t.Fatalf("step payload %d bytes exceeds limit", len(event.Payload))
		}
		var decoded decodedStep
		if err := json.Unmarshal(event.Payload, &decoded); err != nil || decoded.Schema != 1 {
			t.Fatalf("invalid step payload %s: %v", event.Payload, err)
		}
		steps = append(steps, decoded.Step)
	}
	return steps, legacy
}

func notification(method string, params any) string {
	encoded, _ := json.Marshal(map[string]any{"method": method, "params": params})
	return string(encoded)
}

func started(item map[string]any) string {
	return notification("item/started", map[string]any{"threadId": "th-1", "turnId": "tu-1", "item": item, "startedAtMs": int64(1793260800000)})
}

func completed(item map[string]any) string {
	return notification("item/completed", map[string]any{"threadId": "th-1", "turnId": "tu-1", "item": item, "completedAtMs": int64(1793260802500)})
}

func delta(method, itemID, text string) string {
	return notification(method, map[string]any{"threadId": "th-1", "turnId": "tu-1", "itemId": itemID, "delta": text})
}

func TestExecStepLifecycleWithDeltaMerge(t *testing.T) {
	session, clock := newStepSession()
	item := map[string]any{"type": "commandExecution", "id": "cmd-1", "command": "go test ./... s3cr3t", "cwd": "/work/repo/pkg",
		"status": "inProgress", "commandActions": []any{map[string]any{"type": "unknown", "command": "go test"}}}
	steps, legacy := feed(t, session, started(item))
	if len(legacy) != 0 || len(steps) != 1 {
		t.Fatalf("started: steps=%v legacy=%v", steps, legacy)
	}
	if s := steps[0]; s["kind"] != "exec" || s["state"] != "running" || s["cwd"] != "pkg" || s["command"] != "go test ./... [REDACTED]" || s["started_at"] == nil {
		t.Fatalf("running step = %v", s)
	}
	// 小增量先缓冲，不立即发出。
	steps, _ = feed(t, session, delta("item/commandExecution/outputDelta", "cmd-1", "ok  pkg/a\n"))
	if len(steps) != 0 {
		t.Fatalf("small delta flushed early: %v", steps)
	}
	// 250ms 后的下一个增量把缓冲一起发出。
	clock.at = clock.at.Add(300 * time.Millisecond)
	steps, _ = feed(t, session, delta("item/commandExecution/outputDelta", "cmd-1", "ok  pkg/b s3cr3t\n"))
	if len(steps) != 1 || steps[0]["output_append"] != "ok  pkg/a\nok  pkg/b [REDACTED]\n" || steps[0]["output"] != nil {
		t.Fatalf("timed flush = %v", steps)
	}
	// 累计 4 KiB 立即发出；超过 8 KiB 的增量拆成多条。
	steps, _ = feed(t, session, delta("item/commandExecution/outputDelta", "cmd-1", strings.Repeat("x", 10*1024)))
	if len(steps) != 2 || len(steps[0]["output_append"].(string)) != 8*1024 || len(steps[1]["output_append"].(string)) != 2*1024 {
		t.Fatalf("size flush = %d steps", len(steps))
	}
	// Flush 定时器发出剩余缓冲。
	feed(t, session, delta("item/commandExecution/outputDelta", "cmd-1", "tail"))
	flushed, _ := splitEvents(t, session.Flush(clock.at))
	if len(flushed) != 1 || flushed[0]["output_append"] != "tail" {
		t.Fatalf("Flush = %v", flushed)
	}
	if again := session.Flush(clock.at); len(again) != 0 {
		t.Fatalf("second Flush = %v", again)
	}
	// 终态：完整输出、退出码、耗时；非零退出码为 failed；同时保留旧 tool 事件。
	item["status"], item["exitCode"], item["durationMs"], item["aggregatedOutput"] = "completed", 1, 2400, "FAIL s3cr3t\n"
	steps, legacy = feed(t, session, completed(item))
	if len(steps) != 1 || len(legacy) != 1 || legacy[0].Type != domainruntime.EventTool {
		t.Fatalf("completed: steps=%v legacy=%v", steps, legacy)
	}
	s := steps[0]
	if s["state"] != "failed" || s["output"] != "FAIL [REDACTED]\n" || s["exit_code"] != float64(1) || s["duration_ms"] != float64(2400) || s["ended_at"] == nil || s["started_at"] == nil {
		t.Fatalf("terminal step = %v", s)
	}
	// 结束后的迟到增量忽略。
	if steps, _ = feed(t, session, delta("item/commandExecution/outputDelta", "cmd-1", "late")); len(steps) != 0 {
		t.Fatalf("late delta emitted: %v", steps)
	}
}

func TestExecOutputIsTruncatedKeepingTail(t *testing.T) {
	session, _ := newStepSession()
	output := strings.Repeat("a", stepOutputLimit) + "THE END"
	steps, _ := feed(t, session, completed(map[string]any{"type": "commandExecution", "id": "cmd-2", "command": "make", "cwd": "/work/repo",
		"status": "completed", "exitCode": 0, "aggregatedOutput": output, "commandActions": []any{}}))
	if len(steps) != 1 {
		t.Fatalf("steps = %v", steps)
	}
	got := steps[0]["output"].(string)
	if len(got) != stepOutputLimit || !strings.HasSuffix(got, "THE END") || steps[0]["truncated"] != true || steps[0]["cwd"] != "." || steps[0]["state"] != "done" {
		t.Fatalf("truncated output len=%d step=%v", len(got), steps[0]["truncated"])
	}
}

func TestDeclinedCommandIsDeniedWithoutOutput(t *testing.T) {
	session, _ := newStepSession()
	steps, _ := feed(t, session, completed(map[string]any{"type": "commandExecution", "id": "cmd-3", "command": "rm -rf /", "cwd": "/",
		"status": "declined", "aggregatedOutput": nil, "commandActions": []any{}}))
	if s := steps[0]; s["state"] != "denied" || s["terminated_before_output"] != true || s["output"] != "" || s["cwd"] != "/" {
		t.Fatalf("declined step = %v", s)
	}
}

func TestReadAndSearchActionsBecomeActivitySteps(t *testing.T) {
	session, _ := newStepSession()
	item := map[string]any{"type": "commandExecution", "id": "cmd-4", "command": "cat a.go b.go", "cwd": "/work/repo", "status": "inProgress",
		"commandActions": []any{
			map[string]any{"type": "read", "command": "cat a.go", "name": "a.go", "path": "/work/repo/a.go"},
			map[string]any{"type": "read", "command": "cat b.go", "name": "b.go", "path": "/elsewhere/b.go"},
		}}
	steps, _ := feed(t, session, started(item), delta("item/commandExecution/outputDelta", "cmd-4", "package a"))
	if len(steps) != 2 || steps[0]["id"] != "cmd-4" || steps[1]["id"] != "cmd-4:1" || steps[0]["kind"] != "read" ||
		steps[0]["path"] != "a.go" || steps[1]["path"] != "/elsewhere/b.go" {
		t.Fatalf("read steps = %v", steps)
	}
	if flushed := session.Flush(time.Now()); len(flushed) != 0 {
		t.Fatalf("read output must not stream: %v", flushed)
	}
	item["status"], item["exitCode"] = "completed", 0
	steps, _ = feed(t, session, completed(item))
	if len(steps) != 2 || steps[0]["state"] != "done" || steps[1]["state"] != "done" || steps[0]["output"] != nil {
		t.Fatalf("completed read steps = %v", steps)
	}

	search := map[string]any{"type": "commandExecution", "id": "cmd-5", "command": "rg -n Foo", "cwd": "/work/repo", "status": "completed", "exitCode": 0,
		"aggregatedOutput": "/work/repo/a.go:3:Foo()\n/work/repo/a.go:9:Foo()\nb.go:1:Foo\n",
		"commandActions":   []any{map[string]any{"type": "search", "command": "rg -n Foo", "query": "Foo", "path": "/work/repo/internal"}}}
	steps, _ = feed(t, session, completed(search))
	s := steps[0]
	results := s["results"].([]any)
	if s["kind"] != "search" || s["query"] != "Foo" || s["scope"] != "internal" || s["total_files"] != float64(2) || len(results) != 2 {
		t.Fatalf("search step = %v", s)
	}
	if first := results[0].(map[string]any); first["file"] != "a.go" || first["matches"] != float64(2) {
		t.Fatalf("search result = %v", first)
	}

	list := map[string]any{"type": "commandExecution", "id": "cmd-6", "command": "ls", "cwd": "/work/repo", "status": "completed", "exitCode": 0,
		"aggregatedOutput": "a.go\nb.go\nc.go\n", "commandActions": []any{map[string]any{"type": "listFiles", "command": "ls", "path": nil}}}
	steps, _ = feed(t, session, completed(list))
	if s := steps[0]; s["kind"] != "search" || s["total_files"] != float64(3) || len(s["results"].([]any)) != 3 {
		t.Fatalf("listFiles step = %v", s)
	}
	// 混合了未知动作时按 exec 处理。
	mixed := map[string]any{"type": "commandExecution", "id": "cmd-7", "command": "cat a | sh", "cwd": "/work/repo", "status": "completed", "exitCode": 0,
		"aggregatedOutput": "", "commandActions": []any{map[string]any{"type": "read", "command": "cat a", "name": "a", "path": "/work/repo/a"}, map[string]any{"type": "unknown", "command": "sh"}}}
	if steps, _ = feed(t, session, completed(mixed)); steps[0]["kind"] != "exec" {
		t.Fatalf("mixed actions step = %v", steps[0])
	}
}

func TestReasoningAndCommentarySteps(t *testing.T) {
	session, clock := newStepSession()
	steps, _ := feed(t, session,
		started(map[string]any{"type": "reasoning", "id": "rs-1", "summary": []any{}, "content": []any{}}),
		delta("item/reasoning/summaryTextDelta", "rs-1", "Plan the "),
		// 另一来源的增量与首个来源交错时忽略。
		delta("item/reasoning/textDelta", "rs-1", "raw"),
		notification("item/reasoning/summaryPartAdded", map[string]any{"threadId": "th-1", "turnId": "tu-1", "itemId": "rs-1", "summaryIndex": 1}),
	)
	if len(steps) != 1 || steps[0]["kind"] != "thinking" || steps[0]["state"] != "running" {
		t.Fatalf("thinking start = %v", steps)
	}
	clock.at = clock.at.Add(time.Second)
	flushed, _ := splitEvents(t, session.Flush(clock.at))
	if len(flushed) != 1 || flushed[0]["append"] != "Plan the \n\n" {
		t.Fatalf("thinking append = %v", flushed)
	}
	steps, legacy := feed(t, session, completed(map[string]any{"type": "reasoning", "id": "rs-1", "summary": []any{"Plan the work", "Then s3cr3t"}, "content": []any{"detail"}}))
	if len(legacy) != 0 || steps[0]["state"] != "done" || steps[0]["text"] != "Plan the work\n\nThen [REDACTED]\n\ndetail" {
		t.Fatalf("thinking done = %v legacy=%v", steps, legacy)
	}
	// 未开始且没有内容的 reasoning 不产生步骤。
	if steps, _ = feed(t, session, completed(map[string]any{"type": "reasoning", "id": "rs-2", "summary": []any{}, "content": []any{}})); len(steps) != 0 {
		t.Fatalf("empty reasoning = %v", steps)
	}

	commentary := map[string]any{"type": "agentMessage", "id": "msg-1", "text": "", "phase": "commentary"}
	steps, _ = feed(t, session, started(commentary), delta("item/agentMessage/delta", "msg-1", "Looking"))
	if len(steps) != 1 || steps[0]["kind"] != "agent_text" {
		t.Fatalf("commentary start = %v", steps)
	}
	commentary["text"] = "Looking at files"
	steps, legacy = feed(t, session, completed(commentary))
	if steps[0]["text"] != "Looking at files" || steps[0]["state"] != "done" || len(legacy) != 1 || legacy[0].Type != domainruntime.EventTool {
		t.Fatalf("commentary done = %v legacy=%v", steps, legacy)
	}
	// 最终回复只有 output 事件，没有步骤，增量也不发。
	final := map[string]any{"type": "agentMessage", "id": "msg-2", "text": "", "phase": "final_answer"}
	steps, _ = feed(t, session, started(final), delta("item/agentMessage/delta", "msg-2", "Done"))
	if len(steps) != 0 || len(session.Flush(clock.at.Add(time.Second))) != 0 {
		t.Fatalf("final answer produced steps: %v", steps)
	}
	final["text"] = "Done"
	steps, legacy = feed(t, session, completed(final))
	if len(steps) != 0 || len(legacy) != 1 || legacy[0].Type != domainruntime.EventOutput || legacy[0].Text != "Done" {
		t.Fatalf("final answer: steps=%v legacy=%v", steps, legacy)
	}
}

func TestFileChangeBecomesPatchStep(t *testing.T) {
	session, _ := newStepSession()
	diff := "--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n-old\n+new\n+more <tag> & s3cr3t\n ctx\n"
	big := strings.Repeat("+line\n", 6000)
	steps, _ := feed(t, session, completed(map[string]any{"type": "fileChange", "id": "fc-1", "status": "completed", "changes": []any{
		map[string]any{"path": "/work/repo/a.go", "kind": map[string]any{"type": "update", "move_path": "/work/repo/b.go"}, "diff": diff},
		map[string]any{"path": "/work/repo/new.txt", "kind": map[string]any{"type": "add"}, "diff": "one\ntwo\n"},
		map[string]any{"path": "/work/repo/big.txt", "kind": map[string]any{"type": "update", "move_path": nil}, "diff": "@@ -0,0 +1 @@\n" + big},
	}}))
	files := steps[0]["files"].([]any)
	first, second, third := files[0].(map[string]any), files[1].(map[string]any), files[2].(map[string]any)
	if steps[0]["kind"] != "patch" || steps[0]["state"] != "done" || first["path"] != "a.go" || first["move_path"] != "b.go" ||
		first["additions"] != float64(2) || first["deletions"] != float64(1) || !strings.Contains(first["diff"].(string), "<tag> & [REDACTED]") {
		t.Fatalf("patch file = %v", first)
	}
	if second["kind"] != "add" || second["additions"] != float64(2) || second["deletions"] != float64(0) {
		t.Fatalf("added file = %v", second)
	}
	if third["truncated"] != true || len(third["diff"].(string)) > stepDiffLimit || third["additions"] != float64(6000) {
		t.Fatalf("big file truncated=%v len=%d", third["truncated"], len(third["diff"].(string)))
	}
	if steps, _ = feed(t, session, completed(map[string]any{"type": "fileChange", "id": "fc-2", "status": "declined", "changes": []any{}})); steps[0]["state"] != "denied" {
		t.Fatalf("declined patch = %v", steps[0])
	}
}

func TestWebSearchAndToolCallSteps(t *testing.T) {
	session, _ := newStepSession()
	steps, _ := feed(t, session,
		completed(map[string]any{"type": "webSearch", "id": "ws-1", "query": "", "action": map[string]any{"type": "search", "query": "go generics", "queries": nil},
			"results": []any{map[string]any{"title": "Go", "url": "https://go.dev"}, map[string]any{"title": "no url"}}}),
		completed(map[string]any{"type": "webSearch", "id": "ws-2", "query": "", "action": map[string]any{"type": "openPage", "url": "https://go.dev/doc"}, "results": nil}),
		completed(map[string]any{"type": "mcpToolCall", "id": "mcp-1", "server": "github", "tool": "search", "status": "completed", "arguments": map[string]any{},
			"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "3 issues"}}, "structuredContent": nil, "_meta": nil}, "error": nil, "durationMs": 40}),
		completed(map[string]any{"type": "mcpToolCall", "id": "mcp-2", "server": "github", "tool": "search", "status": "failed", "arguments": map[string]any{},
			"result": nil, "error": map[string]any{"message": "denied"}, "durationMs": nil}),
		completed(map[string]any{"type": "dynamicToolCall", "id": "dyn-1", "namespace": "fs", "tool": "list", "status": "completed", "arguments": map[string]any{},
			"contentItems": []any{map[string]any{"type": "inputText", "text": "a\nb"}}, "success": false, "durationMs": 5}),
		completed(map[string]any{"type": "contextCompaction", "id": "cc-1"}),
		completed(map[string]any{"type": "userMessage", "id": "um-1", "clientId": nil, "content": []any{map[string]any{"type": "text", "text": "hi"}}}),
	)
	if len(steps) != 5 {
		t.Fatalf("steps = %v", steps)
	}
	if s := steps[0]; s["kind"] != "web_search" || s["query"] != "go generics" || len(s["results"].([]any)) != 1 {
		t.Fatalf("web_search = %v", s)
	}
	if s := steps[1]; s["kind"] != "web_fetch" || s["url"] != "https://go.dev/doc" {
		t.Fatalf("web_fetch = %v", s)
	}
	if s := steps[2]; s["kind"] != "exec" || s["command"] != "github.search" || s["output"] != "3 issues" || s["state"] != "done" || s["duration_ms"] != float64(40) {
		t.Fatalf("mcp = %v", s)
	}
	if s := steps[3]; s["state"] != "failed" || s["output"] != "denied" {
		t.Fatalf("mcp failed = %v", s)
	}
	if s := steps[4]; s["command"] != "fs.list" || s["state"] != "failed" || s["output"] != "a\nb" {
		t.Fatalf("dynamic tool = %v", s)
	}
}

func TestPlanUsageAndTurnEnd(t *testing.T) {
	session, clock := newStepSession()
	steps, _ := feed(t, session,
		notification("turn/plan/updated", map[string]any{"threadId": "th-1", "turnId": "tu-1", "explanation": "steps", "plan": []any{
			map[string]any{"step": "read", "status": "completed"}, map[string]any{"step": "write", "status": "inProgress"}, map[string]any{"step": "test", "status": "pending"}}}),
		notification("thread/tokenUsage/updated", map[string]any{"threadId": "th-1", "turnId": "tu-1", "tokenUsage": map[string]any{
			"total": map[string]any{"totalTokens": 30, "inputTokens": 20, "cachedInputTokens": 5, "cacheWriteInputTokens": 1, "outputTokens": 10, "reasoningOutputTokens": 4},
			"last":  map[string]any{"totalTokens": 3, "inputTokens": 2, "cachedInputTokens": 0, "cacheWriteInputTokens": 0, "outputTokens": 1, "reasoningOutputTokens": 0}, "modelContextWindow": nil}}),
		notification("turn/plan/updated", map[string]any{"threadId": "th-1", "turnId": "other", "explanation": nil, "plan": []any{}}),
		started(map[string]any{"type": "commandExecution", "id": "cmd-9", "command": "sleep 9", "cwd": "/work/repo", "status": "inProgress", "commandActions": []any{}}),
	)
	if len(steps) != 2 || steps[0]["id"] != "plan" || steps[0]["kind"] != "todo" || steps[0]["state"] != "running" || steps[0]["explanation"] != "steps" {
		t.Fatalf("plan step = %v", steps)
	}
	items := steps[0]["items"].([]any)
	if len(items) != 3 || items[1].(map[string]any)["status"] != "in_progress" || items[0].(map[string]any)["status"] != "completed" {
		t.Fatalf("plan items = %v", items)
	}
	usage := session.Usage()
	if usage == nil || *usage != (domainruntime.Usage{Input: 20, CachedInput: 5, CacheWriteInput: 1, Output: 10, ReasoningOutput: 4}) {
		t.Fatalf("usage = %+v", usage)
	}
	clock.at = clock.at.Add(78 * time.Second)
	steps, legacy := feed(t, session, `{"method":"turn/completed","params":{"threadId":"th-1","turn":{"id":"tu-1","status":"completed","durationMs":null}}}`)
	if len(legacy) != 1 || legacy[0].Type != domainruntime.EventCompleted || len(steps) != 2 {
		t.Fatalf("turn end: steps=%v legacy=%v", steps, legacy)
	}
	// 仍在运行的 item 标为 interrupted，随后是 turn_end。
	if steps[0]["id"] != "cmd-9" || steps[0]["state"] != "interrupted" {
		t.Fatalf("open item = %v", steps[0])
	}
	if s := steps[1]; s["id"] != "turn_end" || s["state"] != "done" || s["final_state"] != "done" || s["duration_ms"] != float64(78000) {
		t.Fatalf("turn_end = %v", s)
	}
	// 只发一次。
	if events := session.turnEnd("failed", nil); len(events) != 0 {
		t.Fatalf("second turn_end = %v", events)
	}
}

func TestTurnEndStatesUseReportedDuration(t *testing.T) {
	cases := []struct {
		line, final, state string
		interrupted        bool
	}{
		{`{"method":"turn/completed","params":{"threadId":"th-1","turn":{"id":"tu-1","status":"interrupted","durationMs":1500}}}`, "cancelled", "interrupted", true},
		{`{"method":"turn/completed","params":{"threadId":"th-1","turn":{"id":"tu-1","status":"failed","durationMs":1500,"error":{"message":"boom"}}}}`, "failed", "failed", false},
		{`{"method":"error","params":{"error":{"message":"fatal"},"willRetry":false}}`, "failed", "failed", false},
	}
	for _, c := range cases {
		session, _ := newStepSession()
		session.interrupted = c.interrupted
		steps, _ := feed(t, session, c.line)
		if len(steps) != 1 || steps[0]["final_state"] != c.final || steps[0]["state"] != c.state {
			t.Fatalf("%s: steps = %v", c.line, steps)
		}
		if strings.Contains(c.line, "durationMs") && steps[0]["duration_ms"] != float64(1500) {
			t.Fatalf("%s: duration = %v", c.line, steps[0]["duration_ms"])
		}
	}
}

func TestStepPayloadStaysWithinLimitAndIDIsBounded(t *testing.T) {
	session, _ := newStepSession()
	// 控制字符在 JSON 中转义为 \u00XX（6 倍膨胀），必须仍在单条上限之内。
	noisy := strings.Repeat("\x01", stepOutputLimit)
	longID := strings.Repeat("i", 100)
	steps, _ := feed(t, session, completed(map[string]any{"type": "commandExecution", "id": longID, "command": "x", "cwd": "/work/repo",
		"status": "completed", "exitCode": 0, "aggregatedOutput": noisy, "commandActions": []any{}}))
	if len(steps) != 1 || steps[0]["truncated"] != true {
		t.Fatalf("noisy step = %v", steps[0]["truncated"])
	}
	id := steps[0]["id"].(string)
	if len(id) > stepMaxIDBytes || id != stepID(longID) || !strings.HasPrefix(id, "h-") {
		t.Fatalf("id = %q", id)
	}
}

func TestMalformedStepNotificationsAreIgnored(t *testing.T) {
	session, _ := newStepSession()
	steps, legacy := feed(t, session,
		`{"method":"item/started","params":{"threadId":"th-1","item":"not an object"}}`,
		`{"method":"item/commandExecution/outputDelta","params":{"itemId":1}}`,
		`{"method":"turn/plan/updated","params":{"plan":"bad"}}`,
		`{"method":"thread/tokenUsage/updated","params":{"tokenUsage":{"total":null}}}`,
		started(map[string]any{"type": "commandExecution", "id": "cmd-x", "command": "ls", "cwd": "/work/repo", "status": "inProgress", "commandActions": "bad"}),
	)
	if len(steps) != 0 || len(legacy) != 0 || session.Usage() != nil {
		t.Fatalf("steps=%v legacy=%v usage=%v", steps, legacy, session.Usage())
	}
	// 其他线程的通知忽略。
	other := notification("item/started", map[string]any{"threadId": "other", "item": map[string]any{"type": "reasoning", "id": "r", "summary": []any{}, "content": []any{}}})
	if steps, _ = feed(t, session, other); len(steps) != 0 {
		t.Fatalf("other thread = %v", steps)
	}
}
