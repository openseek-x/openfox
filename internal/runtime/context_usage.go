package runtime

// ContextUsage is optional Runtime-reported metadata. Nil means unknown; zero
// current tokens is valid. It never substitutes for billable cumulative Usage.
type ContextUsage struct {
	Schema         int     `json:"schema"`
	Runtime        string  `json:"runtime"`
	RequestedModel string  `json:"requested_model"`
	Model          *string `json:"model"`
	CurrentTokens  *int64  `json:"current_tokens"`
	WindowTokens   *int64  `json:"window_tokens"`
}
