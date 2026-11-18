package api

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func (h *Handler) registerBuglyRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/bugly"
	mux.HandleFunc("GET "+base+"/configuration", h.buglyConfiguration)
	mux.HandleFunc("POST "+base+"/connections", h.createBuglyConnection)
	conn := base + "/connections/{connectionID}"
	mux.HandleFunc("POST "+conn+"/{action}", h.manageBuglyConnection)
	mux.HandleFunc("POST "+conn+"/bindings", h.createBuglyBinding)
	mux.HandleFunc("PATCH "+conn+"/bindings/{bindingID}", h.manageBuglyBinding)
	mux.HandleFunc("POST "+conn+"/bindings/{bindingID}/{action}", h.manageBuglyBinding)
	mux.HandleFunc("POST /v1/hooks/bugly/{token}", h.receiveBuglyWebhook)
}
func writeBuglyManagementError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, integrations.ErrBuglyInactive) {
		writeError(w, r, 409, "BUGLY_INACTIVE", "Bugly reception is disabled")
		return
	}
	writeIntegrationError(w, r, err)
}
func (h *Handler) buglyConfiguration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.BuglyConfiguration
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.BuglyConfiguration(ctx, tx, call.organizationID, call.selected.Actor)
		return e
	})
	if err != nil {
		writeBuglyManagementError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
func (h *Handler) createBuglyConnection(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var empty struct{}
	if decodeJSON(w, r, &empty) != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out integrations.Connection
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.CreateBuglyConnection(ctx, tx, call.organizationID, call.selected.Actor)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.bugly_connection_created", "integration_connection", out.ID, "", call.selected.Actor.AuthorizationVersion, nil)
	})
	if err != nil {
		writeBuglyManagementError(w, r, err)
		return
	}
	writeJSON(w, 201, out)
}
func (h *Handler) manageBuglyConnection(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.BuglyVersionInput
	if decodeJSON(w, r, &in) != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	action, id := r.PathValue("action"), r.PathValue("connectionID")
	var out any
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		if action == "secret" {
			out, e = h.integrations.BuglyWebhookSecret(ctx, tx, call.organizationID, call.selected.Actor, id, in.Version)
		} else {
			out, e = h.integrations.ManageBuglyConnection(ctx, tx, call.organizationID, call.selected.Actor, id, action, in.Version)
		}
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.bugly_"+action, "integration_connection", id, "", call.selected.Actor.AuthorizationVersion, nil)
	})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if err != nil {
		writeBuglyManagementError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
func (h *Handler) createBuglyBinding(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.BuglyBindingInput
	if decodeJSON(w, r, &in) != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out integrations.BuglyBinding
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.CreateBuglyBinding(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), in)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.bugly_binding_created", "integration_bugly_binding", out.ID, out.SpaceID, call.selected.Actor.AuthorizationVersion, nil)
	})
	if err != nil {
		writeBuglyManagementError(w, r, err)
		return
	}
	writeJSON(w, 201, out)
}
func (h *Handler) manageBuglyBinding(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.BuglyBindingUpdate
	if decodeJSON(w, r, &in) != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out integrations.BuglyBinding
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.ManageBuglyBinding(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.PathValue("bindingID"), r.PathValue("action"), in)
		if e != nil {
			return e
		}
		action := r.PathValue("action")
		if action == "" {
			action = "update"
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.bugly_binding_"+action, "integration_bugly_binding", out.ID, out.SpaceID, call.selected.Actor.AuthorizationVersion, nil)
	})
	if err != nil {
		writeBuglyManagementError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
func (h *Handler) receiveBuglyWebhook(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = r.Body.Close(); _ = controller.SetReadDeadline(time.Time{}) }()
	w.Header().Set("Cache-Control", "no-store")
	fail := func(status int, message string) {
		writeJSON(w, status, integrations.BuglyReceipt{Code: 1, Message: message})
	}
	token := r.PathValue("token")
	if len(token) != 43 {
		fail(404, "unavailable")
		return
	}
	hash := integrations.TokenHash(token)
	decision := h.webhookLimiter.Allow(hex.EncodeToString(hash))
	if !decision.Allowed {
		seconds := int(decision.RetryAfter/time.Second) + 1
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		fail(429, "rate_limited")
		return
	}
	if h.pool == nil {
		fail(503, "unavailable")
		return
	}
	var lookup integrations.GitWebhookLookup
	err := h.pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id::text,connection_id::text FROM openfox_private.resolve_bugly_webhook($1)`, hash).Scan(&lookup.OrganizationID, &lookup.ConnectionID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		fail(404, "unavailable")
		return
	}
	if err != nil {
		fail(503, "unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, integrations.BuglyBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			fail(413, "payload_too_large")
		} else {
			fail(400, "invalid_body")
		}
		return
	}
	var receipt integrations.BuglyReceipt
	err = h.runAsWorker(ctx, lookup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SET LOCAL lock_timeout='2500ms'`); e != nil {
			return e
		}
		var e error
		receipt, e = h.integrations.ReceiveBugly(ctx, tx, lookup, token, body)
		return e
	})
	if errors.Is(err, integrations.ErrBuglyInactive) || errors.Is(err, integrations.ErrNotFound) {
		fail(404, "unavailable")
		return
	}
	if err != nil {
		fail(503, "creation_unavailable")
		return
	}
	writeJSON(w, receipt.HTTPStatus, receipt)
}
