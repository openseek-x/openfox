package api

// 开始使用进度（组织成员都可读）。

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
)

func (h *Handler) registerGetStartedRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{organizationID}/get-started", h.getGetStarted)
}

func (h *Handler) getGetStarted(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var item collaboration.GetStartedProgress
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		var err error
		item, err = service.LoadGetStarted(ctx, tx, call.organizationID, call.selected.Actor)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
