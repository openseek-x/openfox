package codex

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	domainruntime "openfox/internal/runtime"
)

type capturedSender struct{ messages []string }

func (c *capturedSender) send(message []byte) error {
	c.messages = append(c.messages, string(message))
	return nil
}

// legacyEvents 去掉执行步骤事件，只保留旧事件。
func legacyEvents(events []domainruntime.Event) []domainruntime.Event {
	result := make([]domainruntime.Event, 0, len(events))
	for _, event := range events {
		if event.Type != domainruntime.EventStep {
			result = append(result, event)
		}
	}
	return result
}

func newTestSession() *appServerSession {
	return &appServerSession{cwd: "/work", redact: func(text string) string { return text }, threadID: "th-1", turnID: "tu-1"}
}

func TestServerApprovalRequestIsDeclined(t *testing.T) {
	session, sender := newTestSession(), &capturedSender{}
	events, done, err := session.HandleLine([]byte(`{"id":"srv-1","method":"item/fileChange/requestApproval","params":{}}`), sender.send)
	if err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	if len(events) != 1 || events[0].Type != domainruntime.EventApprovalRequired || !events[0].Critical {
		t.Fatalf("events = %#v", events)
	}
	if len(sender.messages) != 1 || sender.messages[0] != `{"id":"srv-1","result":{"decision":"decline"}}` {
		t.Fatalf("reply = %v", sender.messages)
	}
}

func TestUnknownServerRequestGetsErrorReplyWithoutFailing(t *testing.T) {
	session, sender := newTestSession(), &capturedSender{}
	events, done, err := session.HandleLine([]byte(`{"id":7,"method":"item/tool/call","params":{}}`), sender.send)
	if err != nil || done || len(events) != 0 {
		t.Fatalf("events=%v done=%v err=%v", events, done, err)
	}
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0], `"id":7`) || !strings.Contains(sender.messages[0], `-32601`) {
		t.Fatalf("reply = %v", sender.messages)
	}
}

func TestTurnCompletionForOtherTurnIsIgnored(t *testing.T) {
	session := newTestSession()
	events, done, err := session.HandleLine([]byte(`{"method":"turn/completed","params":{"threadId":"th-1","turn":{"id":"other","status":"completed"}}}`), (&capturedSender{}).send)
	if err != nil || done || len(events) != 0 {
		t.Fatalf("events=%v done=%v err=%v", events, done, err)
	}
}

func TestUnexpectedInterruptionFails(t *testing.T) {
	session := newTestSession()
	events, done, _ := session.HandleLine([]byte(`{"method":"turn/completed","params":{"threadId":"th-1","turn":{"id":"tu-1","status":"interrupted"}}}`), (&capturedSender{}).send)
	events = legacyEvents(events)
	if !done || len(events) != 1 || events[0].Type != domainruntime.EventFailed {
		t.Fatalf("events=%v done=%v", events, done)
	}
}

func TestInterruptBeforeTurnStartFallsBackToSignal(t *testing.T) {
	session, sender := &appServerSession{}, &capturedSender{}
	if session.Interrupt(sender.send) || len(sender.messages) != 0 {
		t.Fatalf("interrupt before turn start must not send: %v", sender.messages)
	}
}

func TestMalformedLineIsProtocolError(t *testing.T) {
	_, _, err := newTestSession().HandleLine([]byte(`not json`), (&capturedSender{}).send)
	if err == nil || !strings.Contains(err.Error(), "invalid codex app-server message") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpstreamJSONErrorIsSummarized(t *testing.T) {
	line := `{"method":"error","params":{"error":{"message":"unexpected status 400 Bad Request: {\"error\":{\"message\":\"The model 'gpt-x' does not exist\",\"type\":\"invalid_request_error\",\"param\":null,\"code\":\"model_not_found\"}}"},"willRetry":false}}`
	events, done, err := newTestSession().HandleLine([]byte(line), (&capturedSender{}).send)
	events = legacyEvents(events)
	if err != nil || !done || len(events) != 1 || events[0].Type != domainruntime.EventFailed {
		t.Fatalf("events=%v done=%v err=%v", events, done, err)
	}
	if want := "unexpected status 400 Bad Request: The model 'gpt-x' does not exist (model_not_found)"; events[0].Text != want {
		t.Fatalf("text = %q, want %q", events[0].Text, want)
	}
	if !strings.Contains(string(events[0].Payload), `model_not_found`) || !strings.Contains(string(events[0].Payload), `"detail"`) {
		t.Fatalf("payload must keep the original text: %s", events[0].Payload)
	}
}

func TestReadableFailureShapes(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"detail":"Unsupported model"}`, "Unsupported model"},
		{`stream error: {"message":"rate limited","code":"rate_limit"} retrying`, "stream error: rate limited (rate_limit)"},
		{`{"error":"bad key"}`, "bad key"},
		{`plain failure`, "plain failure"},
		{`broken {json`, "broken {json"},
		{`{"other":1}`, `{"other":1}`},
		{`x: {"error":{"message":"code already in text model_not_found","code":"model_not_found"}}`, "x: code already in text model_not_found"},
	}
	for _, c := range cases {
		if got, _ := readableFailure(c.in); got != c.want {
			t.Errorf("readableFailure(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if plain := failureEvent(domainruntime.EventFailed, "plain"); plain.Payload != nil {
		t.Fatalf("unchanged failures carry no payload: %s", plain.Payload)
	}
	if got := boundBytes("ab中文", 4); got != "ab" {
		t.Fatalf("boundBytes = %q", got)
	}
}

func toolSession(call func(ctx context.Context, name, callID string, arguments json.RawMessage) (string, bool)) *appServerSession {
	session := newTestSession()
	session.tools = []domainruntime.Tool{{Name: "openfox_task_view", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	session.toolCall = call
	session.toolCtx = context.Background()
	return session
}

// syncSender 线程安全地收集消息，并在收到第一条时通知。
type syncSender struct {
	mu       sync.Mutex
	messages []string
	got      chan struct{}
}

func newSyncSender() *syncSender { return &syncSender{got: make(chan struct{}, 8)} }

func (c *syncSender) send(message []byte) error {
	c.mu.Lock()
	c.messages = append(c.messages, string(message))
	c.mu.Unlock()
	c.got <- struct{}{}
	return nil
}

func (c *syncSender) wait(t *testing.T) string {
	t.Helper()
	select {
	case <-c.got:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reply")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.messages[len(c.messages)-1]
}

func TestBeginDeclaresExperimentalApiOnlyWithTools(t *testing.T) {
	plain, sender := newTestSession(), &capturedSender{}
	if err := plain.Begin(sender.send, func(error) {}); err != nil {
		t.Fatal(err)
	}
	defer plain.stopHandshake()
	if !strings.Contains(sender.messages[0], `"capabilities":null`) {
		t.Fatalf("no tools: %s", sender.messages[0])
	}
	withTools, sender2 := toolSession(nil), &capturedSender{}
	if err := withTools.Begin(sender2.send, func(error) {}); err != nil {
		t.Fatal(err)
	}
	defer withTools.stopHandshake()
	if !strings.Contains(sender2.messages[0], `"capabilities":{"experimentalApi":true,"requestAttestation":false}`) {
		t.Fatalf("tools: %s", sender2.messages[0])
	}
}

func TestThreadStartCarriesDynamicToolsAndDegrades(t *testing.T) {
	session, sender := toolSession(nil), &capturedSender{}
	if _, _, err := session.HandleLine([]byte(`{"id":1,"result":{}}`), sender.send); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 2 || !strings.Contains(sender.messages[1], `"dynamicTools":[{"description":"d","inputSchema":{"type":"object"},"name":"openfox_task_view","type":"function"}]`) {
		t.Fatalf("messages = %v", sender.messages)
	}
	events, done, err := session.HandleLine([]byte(`{"id":2,"error":{"code":-32600,"message":"experimental"}}`), sender.send)
	if err != nil || done || len(events) != 1 || events[0].Type != domainruntime.EventWarning {
		t.Fatalf("events=%v done=%v err=%v", events, done, err)
	}
	if len(sender.messages) != 3 || strings.Contains(sender.messages[2], "dynamicTools") || !strings.Contains(sender.messages[2], `"thread/start"`) {
		t.Fatalf("retry = %v", sender.messages)
	}
	// 第二次失败不再重发，按失败结束。
	events, done, _ = session.HandleLine([]byte(`{"id":2,"error":{"code":-1,"message":"boom"}}`), sender.send)
	if !done || len(events) != 1 || events[0].Type != domainruntime.EventFailed {
		t.Fatalf("events=%v done=%v", events, done)
	}
}

func TestToolCallRepliesWithContentItems(t *testing.T) {
	var gotName, gotCall string
	var gotArgs json.RawMessage
	session := toolSession(func(_ context.Context, name, callID string, args json.RawMessage) (string, bool) {
		gotName, gotCall, gotArgs = name, callID, args
		return "hello", true
	})
	sender := newSyncSender()
	line := `{"id":"c-1","method":"item/tool/call","params":{"threadId":"th-1","turnId":"tu-1","callId":"call-9","namespace":null,"tool":"openfox_task_view","arguments":{"a":1}}}`
	if _, _, err := session.HandleLine([]byte(line), sender.send); err != nil {
		t.Fatal(err)
	}
	reply := sender.wait(t)
	if reply != `{"id":"c-1","result":{"contentItems":[{"text":"hello","type":"inputText"}],"success":true}}` {
		t.Fatalf("reply = %s", reply)
	}
	if gotName != "openfox_task_view" || gotCall != "call-9" || string(gotArgs) != `{"a":1}` {
		t.Fatalf("call = %q %q %s", gotName, gotCall, gotArgs)
	}
}

func TestToolCallUnknownToolAndNilHandler(t *testing.T) {
	session := toolSession(func(context.Context, string, string, json.RawMessage) (string, bool) { return "x", true })
	sender := &capturedSender{}
	line := `{"id":5,"method":"item/tool/call","params":{"callId":"c","tool":"other","arguments":{}}}`
	if _, _, err := session.HandleLine([]byte(line), sender.send); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0], `"success":false`) || !strings.Contains(sender.messages[0], "unknown tool") {
		t.Fatalf("reply = %v", sender.messages)
	}
	nilHandler, sender2 := toolSession(nil), &capturedSender{}
	_, _, _ = nilHandler.HandleLine([]byte(`{"id":6,"method":"item/tool/call","params":{"callId":"c","tool":"openfox_task_view"}}`), sender2.send)
	if len(sender2.messages) != 1 || !strings.Contains(sender2.messages[0], `"success":false`) {
		t.Fatalf("reply = %v", sender2.messages)
	}
}

func TestToolCallTimeoutAndRedaction(t *testing.T) {
	old := appServerToolCallTimeout
	appServerToolCallTimeout = 50 * time.Millisecond
	defer func() { appServerToolCallTimeout = old }()
	session := toolSession(func(ctx context.Context, _, _ string, _ json.RawMessage) (string, bool) {
		<-ctx.Done()
		return "late", true
	})
	sender := newSyncSender()
	line := `{"id":8,"method":"item/tool/call","params":{"callId":"c","tool":"openfox_task_view","arguments":{}}}`
	_, _, _ = session.HandleLine([]byte(line), sender.send)
	if reply := sender.wait(t); !strings.Contains(reply, `"success":false`) || !strings.Contains(reply, "timed out") {
		t.Fatalf("reply = %s", reply)
	}
	session = toolSession(func(context.Context, string, string, json.RawMessage) (string, bool) { return "token=SECRET", true })
	session.redact = func(text string) string { return strings.ReplaceAll(text, "SECRET", "[redacted]") }
	sender = newSyncSender()
	_, _, _ = session.HandleLine([]byte(line), sender.send)
	if reply := sender.wait(t); strings.Contains(reply, "SECRET") {
		t.Fatalf("reply leaks secret: %s", reply)
	}
}
