package api

// 任务助手 Run 的空间任务工具的 HTTP 入口：设备事务里校验 Run 与授权，真正的读写交给 internal/spacetools。

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/dispatch"
	"openfox/internal/spacetools"
	"openfox/internal/tenancy"
)

func (h *Handler) callSpaceTool(w http.ResponseWriter, r *http.Request, deviceID string, selection tenancy.OrganizationSelection, tool string, input dispatch.ToolCall) {
	if !dispatch.IsSpaceTool(tool) {
		writeDispatchError(w, r, dispatch.ErrNotFound)
		return
	}
	var plan dispatch.SpaceToolPlan
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		plan, err = h.dispatch.PrepareSpaceTool(ctx, tx, selection.OrganizationID, deviceID, r.PathValue("runID"), input)
		return err
	})
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	result, err := spacetools.Execute(r.Context(), h.pool, selection.OrganizationID, plan, tool, input)
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
