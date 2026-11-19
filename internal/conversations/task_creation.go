package conversations

// TaskCreationOptions records the choices made by the requester before the Agent runs.
type TaskCreationOptions struct {
	AgentID        string `json:"agent_id,omitempty"`
	CrewID         string `json:"crew_id,omitempty"`
	ProjectID      string `json:"project_id,omitempty"`
	TypeID         string `json:"type_id,omitempty"`
	StatusID       string `json:"status_id,omitempty"`
	ParentTaskID   string `json:"parent_task_id,omitempty"`
	IterationID    string `json:"iteration_id,omitempty"`
	Priority       string `json:"priority,omitempty"`
	DueDate        string `json:"due_date,omitempty"`
	SubmitToTriage bool   `json:"submit_to_triage,omitempty"`
}

type TaskCreationRequest struct {
	TaskCreationOptions
	Brief string `json:"brief"`
}
