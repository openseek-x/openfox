package teams

// Context is server-authored routing/authority metadata, never an Activity or a
// caller-supplied authorization grant. Source snapshots keep Route independently
// of transient queue retention. Group bindings omit per-message identity fields.
type Context struct {
	RejectedInputs  []string `json:"rejected_inputs,omitempty"`
	InputID         string   `json:"input_id,omitempty"`
	Notice          bool     `json:"notice,omitempty"`
	Route           Route    `json:"route"`
	BindingID       string   `json:"binding_id,omitempty"`
	ScopeID         string   `json:"scope_id,omitempty"`
	ScopeGeneration int64    `json:"scope_generation,omitempty"`
	IdentityID      string   `json:"identity_id,omitempty"`
	IdentityVersion int64    `json:"identity_version,omitempty"`
	ActorID         string   `json:"actor_id,omitempty"`
	ActorVersion    int64    `json:"actor_version,omitempty"`
	EventID         string   `json:"event_id,omitempty"`
	BodyHash        string   `json:"body_hash,omitempty"`
}
