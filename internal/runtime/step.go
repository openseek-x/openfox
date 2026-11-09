package runtime

import (
	"bytes"
	"encoding/json"
	"time"
)

// 执行步骤事件（schema v1）的公共结构。字段含义与合并规则见 contracts/openapi.yaml 的 Step。
// 各 kind 只填写自己的专有字段，其余字段为零值并在序列化时省略。

const (
	StepKindPrepEnv   = "prep_env"
	StepKindThinking  = "thinking"
	StepKindAgentText = "agent_text"
	StepKindExec      = "exec"
	StepKindPatch     = "patch"
	StepKindWriteFile = "write_file"
	StepKindRead      = "read"
	StepKindSearch    = "search"
	StepKindWebSearch = "web_search"
	StepKindWebFetch  = "web_fetch"
	StepKindTodo      = "todo"
	StepKindGroup     = "step_group"
	StepKindTurnEnd   = "turn_end"

	StepRunning     = "running"
	StepDone        = "done"
	StepFailed      = "failed"
	StepInterrupted = "interrupted"
	StepDenied      = "denied"
)

type Step struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	State      string     `json:"state"`
	GroupID    string     `json:"group_id,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	DurationMS *int64     `json:"duration_ms,omitempty"`

	// prep_env
	Substeps []StepSubstep `json:"substeps,omitempty"`
	Progress *float64      `json:"progress,omitempty"`
	// thinking / agent_text
	Text   string `json:"text,omitempty"`
	Append string `json:"append,omitempty"`
	// exec
	Command                string  `json:"command,omitempty"`
	Cwd                    string  `json:"cwd,omitempty"`
	OutputAppend           string  `json:"output_append,omitempty"`
	Output                 *string `json:"output,omitempty"`
	Truncated              bool    `json:"truncated,omitempty"`
	ExitCode               *int    `json:"exit_code,omitempty"`
	TerminatedBeforeOutput bool    `json:"terminated_before_output,omitempty"`
	// patch
	Files []StepPatchFile `json:"files,omitempty"`
	// write_file / read
	Path          string `json:"path,omitempty"`
	Content       string `json:"content,omitempty"`
	Applied       *bool  `json:"applied,omitempty"`
	DiffAvailable *bool  `json:"diff_available,omitempty"`
	LineStart     *int   `json:"line_start,omitempty"`
	LineEnd       *int   `json:"line_end,omitempty"`
	// search / web_search / web_fetch
	Query         string             `json:"query,omitempty"`
	Scope         string             `json:"scope,omitempty"`
	SearchResults []StepSearchResult `json:"-"`
	WebResults    []StepWebResult    `json:"-"`
	TotalFiles    *int               `json:"total_files,omitempty"`
	URL           string             `json:"url,omitempty"`
	// todo
	Items       []StepTodoItem `json:"items,omitempty"`
	Explanation string         `json:"explanation,omitempty"`
	// step_group
	Title string `json:"title,omitempty"`
	// turn_end
	FinalState string `json:"final_state,omitempty"`
}

// MarshalJSON 把 search / web_search 的两类结果合并到同一个 results 字段。
func (s Step) MarshalJSON() ([]byte, error) {
	type plain Step
	var results any
	switch {
	case len(s.SearchResults) > 0:
		results = s.SearchResults
	case len(s.WebResults) > 0:
		results = s.WebResults
	}
	return marshalNoEscape(struct {
		plain
		Results any `json:"results,omitempty"`
	}{plain(s), results})
}

type StepSubstep struct {
	Kind  string `json:"kind"`
	State string `json:"state"`
	Label string `json:"label,omitempty"`
}

type StepPatchFile struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	MovePath  string `json:"move_path,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated,omitempty"`
}

type StepSearchResult struct {
	File    string `json:"file"`
	Matches int    `json:"matches,omitempty"`
}

type StepWebResult struct {
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}

type StepTodoItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

// StepPayload 返回 EventStep 的载荷 {"schema":1,"step":{...}}。不转义 HTML 字符，
// 避免 diff / 命令输出中的 < > & 膨胀为 < 等 6 字节序列。
func StepPayload(step Step) (json.RawMessage, error) {
	return marshalNoEscape(struct {
		Schema int  `json:"schema"`
		Step   Step `json:"step"`
	}{StepSchemaVersion, step})
}

func marshalNoEscape(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}
