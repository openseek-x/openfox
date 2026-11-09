package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/resourcepolicy"
)

func (h *Handler) registerResourcePolicyRoutes(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET /v1/organizations/{organizationID}/runtime-config", h.getRuntimeConfig)
	mux.HandleFunc("PATCH /v1/organizations/{organizationID}/runtime-config", h.updateRuntimeConfig)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runtime-config/ack", h.ackRuntimeConfig)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/cleanup/previews", h.createCleanupPreview)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/cleanup/previews/{previewID}/confirm", h.confirmCleanupPreview)
}

type runtimeConfigUpdateRequest struct {
	ExpectedVersion int64                 `json:"expected_version"`
	Policy          resourcepolicy.Policy `json:"policy"`
	RestartRequired bool                  `json:"restart_required"`
}

type runtimeConfigAckRequest struct {
	AppliedVersion  int64 `json:"applied_version"`
	RestartRequired bool  `json:"restart_required"`
}

type cleanupPreviewRequest struct {
	Limit      int `json:"limit"`
	TTLSeconds int `json:"ttl_seconds"`
}

func (h *Handler) getRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	_, selection, _, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	var config resourcepolicy.Config
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		config, err = h.resources.GetConfig(ctx, tx, selection.OrganizationID)
		return err
	})
	if err != nil {
		writeResourcePolicyError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, config)
}

func (h *Handler) updateRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireResourceAdmin(w, r, auth, role) {
		return
	}
	var input runtimeConfigUpdateRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeResourcePolicyError(w, r, resourcepolicy.ErrInvalidPolicy)
		return
	}
	var config resourcepolicy.Config
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		config, err = h.resources.UpdateConfig(ctx, tx, selection.OrganizationID, resourcepolicy.ConfigUpdate{
			ExpectedVersion: input.ExpectedVersion, Policy: input.Policy, RestartRequired: input.RestartRequired,
		})
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, collaborationRequestID(w, r), selection.OrganizationID, actor,
			"runtime_config.updated", "runtime_config", selection.OrganizationID, "", map[string]string{
				"version": formatInt64(config.ConfigVersion),
			})
	})
	if err != nil {
		writeResourcePolicyError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, config)
}

func (h *Handler) ackRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	_, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input runtimeConfigAckRequest
	if err := decodeDeliveryJSON(w, r, &input); err != nil {
		writeResourcePolicyError(w, r, resourcepolicy.ErrInvalidPolicy)
		return
	}
	var config resourcepolicy.Config
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		config, err = h.resources.AcknowledgeConfig(ctx, tx, selection.OrganizationID, resourcepolicy.ConfigAck{
			AppliedVersion: input.AppliedVersion, RestartRequired: input.RestartRequired,
		})
		return err
	})
	if err != nil {
		writeResourcePolicyError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, config)
}

func (h *Handler) createCleanupPreview(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireResourceAdmin(w, r, auth, role) {
		return
	}
	var input cleanupPreviewRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeResourcePolicyError(w, r, resourcepolicy.ErrInvalidPolicy)
		return
	}
	if input.Limit == 0 {
		input.Limit = resourcepolicy.MaxCleanupItems
	}
	if input.TTLSeconds < 0 {
		writeResourcePolicyError(w, r, resourcepolicy.ErrInvalidPolicy)
		return
	}
	previewID, err := newUUID()
	if err != nil {
		writeResourcePolicyError(w, r, err)
		return
	}
	now := time.Now().UTC()
	var preview resourcepolicy.CleanupPreview
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err = h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		preview, err = h.resources.CreateCleanupPreview(ctx, tx, selection.OrganizationID, auth.principal.UserID,
			previewID, now, time.Duration(input.TTLSeconds)*time.Second, input.Limit)
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, collaborationRequestID(w, r), selection.OrganizationID, actor,
			"cleanup.preview_created", "cleanup_preview", preview.ID, "", map[string]string{
				"version":       formatInt64(preview.Version),
				"resource_kind": "artifact",
			})
	})
	if err != nil {
		writeResourcePolicyError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, preview)
}

type cleanupConfirmResponse struct {
	Preview resourcepolicy.CleanupPreview `json:"preview"`
	Deleted int                           `json:"deleted"`
	Skipped int                           `json:"skipped"`
	Failed  int                           `json:"failed"`
}

func (h *Handler) confirmCleanupPreview(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok || !requireResourceAdmin(w, r, auth, role) {
		return
	}
	if h.delivery.Storage == nil {
		writeResourcePolicyError(w, r, resourcepolicy.ErrStorageUnavailable)
		return
	}
	previewID := r.PathValue("previewID")
	now := time.Now().UTC()
	var items []resourcepolicy.CleanupItem
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		items, err = h.resources.ConfirmCleanupPreview(ctx, tx, selection.OrganizationID, previewID, auth.principal.UserID, now)
		if err != nil {
			return err
		}
		return recordConversationAudit(ctx, tx, collaborationRequestID(w, r), selection.OrganizationID, actor,
			"cleanup.preview_confirmed", "cleanup_preview", previewID, "", map[string]string{
				"status":        "confirmed",
				"resource_kind": "artifact",
			})
	})
	if err != nil {
		writeResourcePolicyError(w, r, err)
		return
	}
	response := cleanupConfirmResponse{}
	for _, item := range items {
		if err := h.delivery.Storage.Delete(r.Context(), item.StorageKey); err != nil {
			response.Failed++
			markErr := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
				return h.resources.MarkCleanupItemFailed(ctx, tx, selection.OrganizationID, previewID, item.ID, item.ExpectedVersion)
			})
			if markErr != nil {
				writeResourcePolicyError(w, r, markErr)
				return
			}
			continue
		}
		markErr := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
			return h.resources.MarkCleanupItemDeleted(ctx, tx, selection.OrganizationID, previewID, item.ID, item.ExpectedVersion)
		})
		if errors.Is(markErr, resourcepolicy.ErrPreviewConflict) {
			response.Skipped++
			continue
		}
		if markErr != nil {
			writeResourcePolicyError(w, r, markErr)
			return
		}
		response.Deleted++
	}
	if err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		response.Preview, err = h.resources.GetCleanupPreview(ctx, tx, selection.OrganizationID, previewID)
		return err
	}); err != nil {
		writeResourcePolicyError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func formatInt64(value int64) string { return strconv.FormatInt(value, 10) }

func requireResourceAdmin(w http.ResponseWriter, r *http.Request, auth authContext, role string) bool {
	if auth.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(role)) {
	case "OWNER", "ADMIN":
		return true
	default:
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "organization administration is required")
		return false
	}
}

func writeResourcePolicyError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, resourcepolicy.ErrInvalidPolicy):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "resource policy request is invalid")
	case errors.Is(err, resourcepolicy.ErrConfigConflict), errors.Is(err, resourcepolicy.ErrPreviewConflict):
		writeError(w, r, http.StatusConflict, "VERSION_CONFLICT", "resource policy changed; refresh and retry")
	case errors.Is(err, resourcepolicy.ErrPreviewExpired):
		writeError(w, r, http.StatusGone, "PREVIEW_EXPIRED", "cleanup preview has expired")
	case errors.Is(err, resourcepolicy.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource policy record is not available")
	case errors.Is(err, resourcepolicy.ErrStorageUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "artifact storage is not ready")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}
