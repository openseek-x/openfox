package codex

import (
	"testing"

	domainruntime "openfox/internal/runtime"
)

func failedCode(t *testing.T, line string) string {
	t.Helper()
	events, done, err := newTestSession().HandleLine([]byte(line), (&capturedSender{}).send)
	if err != nil || !done {
		t.Fatalf("done=%v err=%v", done, err)
	}
	for _, e := range events {
		if e.Type == domainruntime.EventFailed {
			return e.FailureCode
		}
	}
	t.Fatalf("no failed event: %#v", events)
	return ""
}

// 阶段 12b：codex 映射依据公开协议定义，本机未安装 codex，未在真实 CLI 上验证。
func TestCodexFailureCodeMapping(t *testing.T) {
	cases := []struct{ name, info, want string }{
		{"string unauthorized", `"unauthorized"`, domainruntime.FailureCredentialRejected},
		{"pascal usage", `"UsageLimitExceeded"`, domainruntime.FailureQuotaExhausted},
		{"overloaded", `"serverOverloaded"`, domainruntime.FailureProviderOverloaded},
		{"context", `"contextWindowExceeded"`, domainruntime.FailureContextTooLarge},
		{"object 429", `{"httpConnectionFailed":{"httpStatusCode":429}}`, domainruntime.FailureRateLimited},
		{"object no status", `{"httpConnectionFailed":{}}`, domainruntime.FailureNetworkUnreachable},
		{"object unknown status", `{"responseStreamDisconnected":{"httpStatusCode":500}}`, ""},
		{"other", `"other"`, ""},
		{"null", `null`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := `{"method":"error","params":{"error":{"message":"m","codexErrorInfo":` + tc.info + `},"willRetry":false}}`
			if got := failedCode(t, line); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestCodexUpstreamCodeFieldAndNoTextMatching(t *testing.T) {
	line := `{"method":"error","params":{"error":{"message":"unexpected status 404: {\"error\":{\"code\":\"model_not_found\",\"message\":\"x\"}}"},"willRetry":false}}`
	if got := failedCode(t, line); got != domainruntime.FailureModelNotFound {
		t.Fatalf("got %q", got)
	}
	plain := `{"method":"error","params":{"error":{"message":"rate limit exceeded insufficient_quota"},"willRetry":false}}`
	if got := failedCode(t, plain); got != "" {
		t.Fatalf("natural language must not map, got %q", got)
	}
}

func TestCodexTurnFailedCarriesCode(t *testing.T) {
	line := `{"method":"turn/completed","params":{"threadId":"th-1","turn":{"id":"tu-1","status":"failed","error":{"message":"m","codexErrorInfo":"unauthorized"}}}}`
	if got := failedCode(t, line); got != domainruntime.FailureCredentialRejected {
		t.Fatalf("got %q", got)
	}
}
