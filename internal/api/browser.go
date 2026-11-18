package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/browser"
	"openfox/internal/dispatch"
)

func (h *Handler) registerBrowserRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/organizations/{organizationID}/conversations/{conversationID}/browser-bindings", h.createBrowserBinding)
	mux.HandleFunc("DELETE /v1/organizations/{organizationID}/conversations/{conversationID}/browser-bindings/{bindingID}", h.revokeBrowserBinding)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/browser-bindings/{bindingID}/claim", h.claimBrowserBinding)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/browser-bindings/{bindingID}/heartbeat", h.heartbeatBrowserBinding)
	mux.HandleFunc("POST /v1/device/organizations/{organizationID}/runs/{runID}/browser-authorize", h.authorizeBrowserRun)
}

func writeBrowserError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, browser.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "browser request is invalid")
	case errors.Is(err, browser.ErrUnavailable):
		writeError(w, r, http.StatusNotFound, "BROWSER_UNAVAILABLE", "browser binding is unavailable")
	case errors.Is(err, browser.ErrConflict):
		writeError(w, r, http.StatusConflict, "BROWSER_BINDING_CONFLICT", "a live browser binding already exists")
	default:
		writeDispatchError(w, r, err)
	}
}

func (h *Handler) createBrowserBinding(w http.ResponseWriter, r *http.Request) {
	auth, selection, _, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	var input browser.CreateInput
	if decodeDeliveryJSON(w, r, &input) != nil {
		writeBrowserError(w, r, browser.ErrInvalid)
		return
	}
	var result browser.CreateResult
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = browser.Create(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), selection.ActorID, input)
		return err
	})
	if err != nil {
		writeBrowserError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) revokeBrowserBinding(w http.ResponseWriter, r *http.Request) {
	auth, selection, _, ok := h.conversationAuth(w, r)
	if !ok || !requireConversationCSRF(w, r, auth) {
		return
	}
	err := h.runConversationTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		return browser.Revoke(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), selection.ActorID, r.PathValue("bindingID"))
	})
	if err != nil {
		writeBrowserError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) claimBrowserBinding(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input struct {
		browser.Identity
		ClaimSecret string `json:"claim_secret"`
	}
	if decodeDeliveryJSON(w, r, &input) != nil {
		writeBrowserError(w, r, browser.ErrInvalid)
		return
	}
	var result browser.Binding
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = browser.Claim(ctx, tx, selection.OrganizationID, principal.DeviceID, r.PathValue("bindingID"), input.ClaimSecret, input.Identity)
		return err
	})
	if err != nil {
		writeBrowserError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) heartbeatBrowserBinding(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input browser.Identity
	if decodeDeliveryJSON(w, r, &input) != nil {
		writeBrowserError(w, r, browser.ErrInvalid)
		return
	}
	var result browser.Binding
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = browser.Heartbeat(ctx, tx, selection.OrganizationID, principal.DeviceID, r.PathValue("bindingID"), input)
		return err
	})
	if err != nil {
		writeBrowserError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) authorizeBrowserRun(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input dispatch.BrowserAuthorizationInput
	if decodeDeliveryJSON(w, r, &input) != nil {
		writeBrowserError(w, r, browser.ErrInvalid)
		return
	}
	var result dispatch.BrowserAuthorization
	err := h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		result, err = h.dispatch.AuthorizeBrowser(ctx, tx, selection.OrganizationID, principal.DeviceID, r.PathValue("runID"), input)
		return err
	})
	if err != nil {
		writeBrowserError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}
