package runtime

// 阶段 12b：适配器把 CLI 的结构化错误映射为中心既有的失败码枚举（runs_failure_code_valid）。
// 只依据结构化字段（事件类型、错误枚举、状态码），不匹配自然语言文本；识别不了的返回空，由中心记为 INTERNAL。
const (
	FailureCredentialRejected = "CREDENTIAL_REJECTED"
	FailureQuotaExhausted     = "QUOTA_EXHAUSTED"
	FailureRateLimited        = "RATE_LIMITED"
	FailureProviderOverloaded = "PROVIDER_OVERLOADED"
	FailureModelNotFound      = "MODEL_NOT_FOUND"
	FailureContextTooLarge    = "CONTEXT_TOO_LARGE"
	FailureNetworkUnreachable = "NETWORK_UNREACHABLE"
)

// FailureCodeForHTTPStatus 把上游返回的 HTTP 状态码（结构化数字字段）映射为失败码；其余状态码不猜。
func FailureCodeForHTTPStatus(status int) string {
	switch status {
	case 401:
		return FailureCredentialRejected
	case 402:
		return FailureQuotaExhausted
	case 429:
		return FailureRateLimited
	case 503, 529:
		return FailureProviderOverloaded
	}
	return ""
}
