package executiondelivery

import (
	"encoding/json"
)

// 执行步骤事件（type = "step"）的中心校验：safe_payload.data 必须是 {"schema": 1, "step": {...}}，
// step.id 非空且不超过 64 字节，kind / state 在白名单内。其余字段只做结构上的透传（前端按 schema 渲染），
// 大小由 MaxEventPayload 约束。不合法的事件使整批请求返回 400，与其他校验失败一致。

const (
	StepEventType     = "step"
	StepSchemaVersion = 1
	maxStepIDBytes    = 64
)

var stepKinds = map[string]struct{}{
	"prep_env": {}, "thinking": {}, "agent_text": {}, "exec": {}, "patch": {}, "write_file": {},
	"read": {}, "search": {}, "web_search": {}, "web_fetch": {}, "todo": {}, "step_group": {}, "turn_end": {},
}

var stepStates = map[string]struct{}{
	"running": {}, "done": {}, "failed": {}, "interrupted": {}, "denied": {},
}

func validStepPayload(payload json.RawMessage) bool {
	var body struct {
		Data *struct {
			Schema *int `json:"schema"`
			Step   *struct {
				ID      *string `json:"id"`
				Kind    *string `json:"kind"`
				State   *string `json:"state"`
				GroupID *string `json:"group_id"`
			} `json:"step"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return false
	}
	if body.Data == nil || body.Data.Schema == nil || *body.Data.Schema != StepSchemaVersion || body.Data.Step == nil {
		return false
	}
	step := body.Data.Step
	if step.ID == nil || *step.ID == "" || len(*step.ID) > maxStepIDBytes || step.Kind == nil || step.State == nil {
		return false
	}
	if step.GroupID != nil && len(*step.GroupID) > maxStepIDBytes {
		return false
	}
	if _, ok := stepKinds[*step.Kind]; !ok {
		return false
	}
	_, ok := stepStates[*step.State]
	return ok
}
