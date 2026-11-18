package yunxiao

// RowMetadata is internal immutable source evidence, carried in an unmapped
// normalized column. Import configuration cannot replace IDs with labels.
type RowMetadata struct {
	TypeID      string          `json:"type_id"`
	TypeLabel   string          `json:"type_label"`
	StatusID    string          `json:"status_id"`
	StatusLabel string          `json:"status_label"`
	Priorities  []PriorityValue `json:"priorities,omitempty"`
}
type PriorityValue struct {
	FieldID     string `json:"field_id"`
	FieldLabel  string `json:"field_label"`
	OptionID    string `json:"option_id"`
	OptionLabel string `json:"option_label"`
	Source      string `json:"source"`
}

const MetadataColumn = "Yunxiao source metadata"

func TypeSource(id string) string         { return identityKey("type", id) }
func StatusSource(kind, id string) string { return identityKey("status", kind, id) }
func PrioritySource(kind, field, option string) string {
	return identityKey("priority", kind, field, option)
}
