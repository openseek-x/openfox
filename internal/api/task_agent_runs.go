package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/policy"
)

// 召唤菜单里再次选择已指派的 Agent：不改处理人，再触发一次指派运行（复用或新开运行对话）。
// 每次调用都会入队一条 ASSIGNED 事件（worker 不去重），所以支持可选的 Idempotency-Key：
// 同键同参数重放不再入队，返回相同的 202 响应并带 Idempotent-Replayed: true；同键不同参数返回 409 IDEMPOTENCY_CONFLICT。

func (h *Handler) registerTaskAgentRunRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST "+taskBase+"/{taskID}/agent-runs", h.startTaskAgentRun)
}

type taskAgentRunRequest struct {
	AgentID                string  `json:"agent_id"`
	AssignmentConversation *string `json:"assignment_conversation"`
}

// taskAgentRunFingerprint 是幂等指纹里的请求体部分（规范化后的取值）。
type taskAgentRunFingerprint struct {
	AgentID                string `json:"agent_id"`
	AssignmentConversation string `json:"assignment_conversation"`
}

type taskAgentRunResponse struct {
	TaskID                 string `json:"task_id"`
	AgentID                string `json:"agent_id"`
	AssignmentConversation string `json:"assignment_conversation"`
}

func (h *Handler) startTaskAgentRun(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input taskAgentRunRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	mode := collaboration.AssignmentConversationReuse
	if input.AssignmentConversation != nil {
		normalized, err := collaboration.NormalizeAssignmentConversation(*input.AssignmentConversation)
		if err != nil || *input.AssignmentConversation == "" {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "assignment_conversation must be reuse or new")
			return
		}
		mode = normalized
	}
	spaceID, taskID := r.PathValue("spaceID"), r.PathValue("taskID")
	agentID := strings.ToLower(strings.TrimSpace(input.AgentID))
	key := strings.TrimSpace(r.Header.Get(idempotency.HeaderName))
	replayed := false
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		// 与修改处理人相同：需要编辑任务的权限。
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionUpdateObject, taskID); err != nil {
			return err
		}
		// 指纹用规范化后的取值（未传 assignment_conversation 与传 reuse 视为同一请求）。
		claim, err := idempotency.Begin(ctx, tx, "task.agent_run", key, struct {
			SpaceID string                  `json:"space_id"`
			TaskID  string                  `json:"task_id"`
			Body    taskAgentRunFingerprint `json:"body"`
		}{strings.ToLower(spaceID), strings.ToLower(taskID), taskAgentRunFingerprint{agentID, mode}})
		if err != nil {
			return err
		}
		if claim.Replay {
			// 首次请求已入队；响应只由请求参数决定，按相同参数原样返回。
			replayed = true
			return nil
		}
		if err := service.StartTaskAgentRun(ctx, tx, call.organizationID, spaceID, taskID, call.selected.Actor,
			collaboration.TaskAgentRunInput{AgentID: agentID, AssignmentConversation: mode}); err != nil {
			return err
		}
		return idempotency.Complete(ctx, tx, claim, strings.ToLower(taskID))
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	if replayed {
		w.Header().Set(idempotency.ReplayedHeader, "true")
	}
	writeJSON(w, http.StatusAccepted, taskAgentRunResponse{TaskID: taskID, AgentID: agentID, AssignmentConversation: mode})
}
