package codex

import (
	"encoding/json"
	"testing"

	"openfox/internal/runtime"
)

func TestContextUsageIsLastSampleNotCumulative(t *testing.T) {
	s, clock := newStepSession()
	s.model = "chosen-model"
	model := "reported-model"
	s.reportedModel = &model
	for _, tokens := range []int64{60000, 6236, 0, 130000} {
		events, done, err := s.HandleLine([]byte(notification("thread/tokenUsage/updated", map[string]any{
			"threadId": s.threadID, "turnId": s.turnID, "tokenUsage": map[string]any{
				"total": map[string]any{"inputTokens": 90000, "outputTokens": 10000},
				"last":  map[string]any{"totalTokens": tokens}, "modelContextWindow": 124518,
			},
		})), (&capturedSender{}).send)
		if err != nil || done || len(events) != 1 || events[0].Type != runtime.EventContextUsage || !events[0].At.Equal(clock.at) {
			t.Fatalf("events=%+v done=%v err=%v", events, done, err)
		}
		var got runtime.ContextUsage
		if err := json.Unmarshal(events[0].Payload, &got); err != nil {
			t.Fatal(err)
		}
		if got.Schema != 1 || got.Runtime != "codex" || got.RequestedModel != "chosen-model" || got.Model == nil || *got.Model != model || got.CurrentTokens == nil || *got.CurrentTokens != tokens || got.WindowTokens == nil || *got.WindowTokens != 124518 {
			t.Fatalf("sample=%s", events[0].Payload)
		}
		if usage := s.Usage(); usage == nil || usage.Input != 90000 || usage.Output != 10000 {
			t.Fatalf("cumulative usage changed: %+v", usage)
		}
	}
}

func TestContextUsageOptionalCountersAndScope(t *testing.T) {
	for _, raw := range []string{`null`, `-1`, `1.5`, `"5"`, `9007199254740992`, `1e100`, `{}`, `true`} {
		t.Run(raw, func(t *testing.T) {
			s, _ := newStepSession()
			data := `{"threadId":"th-1","turnId":"tu-1","tokenUsage":{"last":{"totalTokens":` + raw + `},"modelContextWindow":` + raw + `}}`
			events := s.contextUsageUpdated(json.RawMessage(data))
			if len(events) != 1 {
				t.Fatalf("events=%+v", events)
			}
			var got runtime.ContextUsage
			if err := json.Unmarshal(events[0].Payload, &got); err != nil {
				t.Fatal(err)
			}
			if got.CurrentTokens != nil || got.WindowTokens != nil || got.Model != nil {
				t.Fatalf("sample=%s", events[0].Payload)
			}
		})
	}
	s, _ := newStepSession()
	for _, raw := range []string{
		`{"threadId":"other","turnId":"tu-1","tokenUsage":{}}`,
		`{"threadId":"th-1","turnId":"other","tokenUsage":{}}`,
		`{"threadId":"th-1","tokenUsage":{}}`,
		`{"turnId":"tu-1","tokenUsage":{}}`,
		`{"threadId":"th-1","turnId":"tu-1","tokenUsage":null}`,
		`{"threadId":"th-1","turnId":"tu-1","tokenUsage":"invalid"}`,
	} {
		if events := s.contextUsageUpdated(json.RawMessage(raw)); len(events) != 0 {
			t.Fatalf("%s emitted %+v", raw, events)
		}
	}
	for _, raw := range []string{`{}`, `{"last":null}`, `{"last":"bad","modelContextWindow":0}`} {
		events := s.contextUsageUpdated(json.RawMessage(`{"threadId":"th-1","turnId":"tu-1","tokenUsage":` + raw + `}`))
		var got runtime.ContextUsage
		if len(events) != 1 {
			t.Fatal(events)
		}
		if err := json.Unmarshal(events[0].Payload, &got); err != nil {
			t.Fatal(err)
		}
		if got.CurrentTokens != nil || got.WindowTokens != nil {
			t.Fatalf("sample=%s", events[0].Payload)
		}
	}
	if zero := contextToken(json.RawMessage(`0`), false); zero == nil || *zero != 0 {
		t.Fatal("zero lost")
	}
	if contextToken(json.RawMessage(`0`), true) != nil {
		t.Fatal("zero window")
	}
	if maximum := contextToken(json.RawMessage(`9007199254740991`), false); maximum == nil || *maximum != maxContextTokens {
		t.Fatal("safe maximum lost")
	}
}

func TestContextUsageThreadModelIsOptionalAndRedacted(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`"chosen-model"`, "chosen-model"}, {`"s3cr3t"`, "[REDACTED]"},
		{`null`, ""}, {`123`, ""}, {`""`, ""},
	} {
		s, _ := newStepSession()
		events, done, err := s.HandleLine([]byte(`{"id":2,"result":{"thread":{"id":"new-thread"},"model":`+tc.raw+`}}`), (&capturedSender{}).send)
		if err != nil || done || len(events) != 0 || s.threadID != "new-thread" {
			t.Fatalf("handshake failed: %v", err)
		}
		if tc.want == "" {
			if s.reportedModel != nil {
				t.Fatal(*s.reportedModel)
			}
		} else if s.reportedModel == nil || *s.reportedModel != tc.want {
			t.Fatalf("model=%v want=%s", s.reportedModel, tc.want)
		}
	}
}
