package codex

import (
	"fmt"
	"testing"
)

func TestModelListDefaultReasoning(t *testing.T) {
	for _, item := range []struct {
		field string
		want  string
	}{
		{`,"defaultReasoningEffort":"low"`, "LOW"},
		{`,"defaultReasoningEffort":"xhigh"`, "EXTRA_HIGH"},
		{`,"defaultReasoningEffort":"ultra"`, "ULTRA"},
		{"", ""}, {`,"defaultReasoningEffort":null`, ""},
		{`,"defaultReasoningEffort":"unknown"`, ""},
		{`,"defaultReasoningEffort":"medium"`, ""},
	} {
		session := &modelListSession{}
		line := fmt.Sprintf(`{"id":2,"result":{"data":[{"id":"model","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"xhigh"},{"reasoningEffort":"ultra"}]%s}],"nextCursor":null}}`, item.field)
		_, done, err := session.HandleLine([]byte(line), func([]byte) error { return nil })
		if err != nil || !done || len(session.models) != 1 || session.models[0].DefaultReasoningEffort != item.want {
			t.Fatalf("field %s: models=%+v done=%t error=%v", item.field, session.models, done, err)
		}
	}
}
