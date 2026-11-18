package cliagent

import (
	"testing"

	"openfox/internal/runtime"
)

// 阶段 12b：适配器只按结构化字段映射失败码；识别不了的保持为空（服务端按 INTERNAL 处理）。
func TestClaudeFailureCodeMapping(t *testing.T) {
	cases := []struct {
		name   string
		assist map[string]any
		result map[string]any
		want   string
	}{
		{"authentication_failed", map[string]any{"error": "authentication_failed"}, nil, runtime.FailureCredentialRejected},
		{"billing_error", map[string]any{"error": "billing_error"}, nil, runtime.FailureQuotaExhausted},
		{"rate_limit", map[string]any{"error": "rate_limit"}, nil, runtime.FailureRateLimited},
		{"overloaded", map[string]any{"error": "overloaded"}, nil, runtime.FailureProviderOverloaded},
		{"model_not_found", map[string]any{"error": "model_not_found"}, nil, runtime.FailureModelNotFound},
		{"http 429 only on result", nil, map[string]any{"api_error_status": 429}, runtime.FailureRateLimited},
		{"http 401 only on result", nil, map[string]any{"api_error_status": 401}, runtime.FailureCredentialRejected},
		{"http 529 only on result", nil, map[string]any{"api_error_status": 529}, runtime.FailureProviderOverloaded},
		{"unmapped enum", map[string]any{"error": "server_error"}, nil, ""},
		{"unmapped status", nil, map[string]any{"api_error_status": 418}, ""},
		{"natural language is ignored", nil, map[string]any{"result": "rate limit exceeded, quota exhausted"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, NewClaudeCode(""))
			var lines []string
			if tc.assist != nil {
				msg := map[string]any{"type": "assistant", "message": map[string]any{"content": []any{}}}
				for k, v := range tc.assist {
					msg[k] = v
				}
				lines = append(lines, j(msg))
			}
			res := map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "result": "boom"}
			for k, v := range tc.result {
				res[k] = v
			}
			lines = append(lines, j(res))
			f.set(scenario{Stdout: lines, Exit: 1})
			out := f.run(runtime.RunRequest{})
			if out.result.Status != runtime.StatusFailed || out.result.FailureCode != tc.want {
				t.Fatalf("status=%s failure_code=%q, want %q", out.result.Status, out.result.FailureCode, tc.want)
			}
		})
	}
}

func TestOpenCodeFailureCodeMapping(t *testing.T) {
	status := func(n int) map[string]any { return map[string]any{"statusCode": n, "message": "x"} }
	cases := []struct {
		name string
		errn string
		data map[string]any
		want string
	}{
		{"auth", "ProviderAuthError", map[string]any{"message": "x"}, runtime.FailureCredentialRejected},
		{"model", "ProviderModelNotFoundError", map[string]any{"message": "x"}, runtime.FailureModelNotFound},
		{"context", "ContextOverflowError", map[string]any{"message": "x"}, runtime.FailureContextTooLarge},
		{"api 402", "APIError", status(402), runtime.FailureQuotaExhausted},
		{"api 429", "APIError", status(429), runtime.FailureRateLimited},
		{"api 503", "APIError", status(503), runtime.FailureProviderOverloaded},
		{"api unknown status", "APIError", status(500), ""},
		{"api no status", "APIError", map[string]any{"message": "rate limit"}, ""},
		{"unknown name", "SomethingNew", map[string]any{"message": "quota exhausted"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, NewOpenCode(""))
			f.set(scenario{Stdout: []string{j(map[string]any{"type": "error", "error": map[string]any{"name": tc.errn, "data": tc.data}})}, Exit: 1})
			out := f.run(runtime.RunRequest{})
			if out.result.Status != runtime.StatusFailed || out.result.FailureCode != tc.want {
				t.Fatalf("status=%s failure_code=%q, want %q", out.result.Status, out.result.FailureCode, tc.want)
			}
		})
	}
}
