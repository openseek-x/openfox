package browser

type AuthorizationInput struct {
	Identity
	BindingID       string `json:"binding_id"`
	Generation      int64  `json:"generation"`
	ClientMessageID string `json:"client_message_id,omitempty"`
	Operation       string `json:"operation"`
}

type Authorization struct {
	Binding    Binding `json:"binding"`
	RunID      string  `json:"run_id"`
	Generation int64   `json:"generation"`
	ReadOnly   bool    `json:"read_only"`
}
