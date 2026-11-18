package collaboration

// RuleError 是带稳定错误码的业务规则错误。新增的业务错误码一律用它声明，
// 接口层按 Status 与 Code 原样输出，不需要逐个在 writeCollaborationError 里登记。
//
// Code 必须是 ^[A-Z][A-Z0-9_]*$ 形式的稳定标识（见 contracts/shared/error-schema.md）。
type RuleError struct {
	Status  int
	Code    string
	Message string
	// Details 是可选的附加信息，原样输出到错误响应的 error.details（不得包含不可读资源的信息）。
	Details map[string]any
}

func (e *RuleError) Error() string { return "collaboration: " + e.Message }

// NewRuleError 声明一个业务规则错误，用于包级 var。
func NewRuleError(status int, code, message string) *RuleError {
	return &RuleError{Status: status, Code: code, Message: message}
}

// WithDetails 返回带附加信息的副本（包级 var 不被修改）。
func (e *RuleError) WithDetails(details map[string]any) *RuleError {
	copied := *e
	copied.Details = details
	return &copied
}
