package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/policy"
)

// registerEstimationRoutes 注册空间估时接口。
func (h *Handler) registerEstimationRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	base := "/v1/organizations/{organizationID}/spaces/{spaceID}/estimation"
	mux.HandleFunc("PATCH "+base, h.updateSpaceEstimation)
	mux.HandleFunc("GET "+base+"/preview", h.previewSpaceEstimation)
}

type estimationPatchRequest struct {
	Version          uint64                             `json:"version"`
	Scale            string                             `json:"scale"`
	Extended         bool                               `json:"extended"`
	AllowZero        bool                               `json:"allow_zero"`
	CountUnestimated bool                               `json:"count_unestimated"`
	Convert          []collaboration.EstimateConversion `json:"convert"`
}

func (h *Handler) updateSpaceEstimation(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input estimationPatchRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	spaceID := r.PathValue("spaceID")
	requestID := collaborationRequestID(w, r)
	var detail collaboration.SpaceDetail
	var converted int64
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionManageSpace, ""); err != nil {
			return err
		}
		var err error
		converted, err = service.UpdateSpaceEstimation(ctx, tx, call.organizationID, spaceID, call.selected.Actor,
			collaboration.EstimationUpdateInput{
				Version: input.Version, Scale: input.Scale, Extended: input.Extended, AllowZero: input.AllowZero,
				CountUnestimated: input.CountUnestimated, Convert: input.Convert,
			})
		if err != nil {
			return err
		}
		detail, err = service.GetSpaceDetail(ctx, tx, call.organizationID, spaceID, call.selected.Actor)
		if err != nil {
			return err
		}
		return recordCollaborationAudit(ctx, tx, requestID, call.organizationID, call.selected.Actor,
			"space.estimation_updated", "space", spaceID, spaceID, call.selected.Actor.AuthorizationVersion,
			map[string]string{"space_id": spaceID, "version": fmt.Sprint(detail.Version), "status": detail.Estimation.Scale,
				"result": fmt.Sprintf("converted=%d", converted)})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"space": toSpaceDetailResponse(detail), "converted_tasks": converted})
}

func (h *Handler) previewSpaceEstimation(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	query := r.URL.Query()
	target := collaboration.SpaceEstimation{Scale: strings.TrimSpace(query.Get("scale"))}
	for key, value := range map[string]*bool{"extended": &target.Extended, "allow_zero": &target.AllowZero} {
		switch strings.ToLower(strings.TrimSpace(query.Get(key))) {
		case "", "false":
		case "true":
			*value = true
		default:
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", key+" must be true or false")
			return
		}
	}
	spaceID := r.PathValue("spaceID")
	var preview collaboration.EstimationPreview
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if err := call.authorizeSpace(ctx, tx, service, spaceID, policy.ActionReadSpace, ""); err != nil {
			return err
		}
		var err error
		preview, err = service.PreviewEstimation(ctx, tx, call.organizationID, spaceID, call.selected.Actor, target)
		return err
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}
