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
	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gongfeng"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

func (h *Handler) registerGongfengRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations"
	mux.HandleFunc("POST /v1/hooks/git/gongfeng/{token}", h.receiveGongfengWebhook)
	mux.HandleFunc("POST "+base+"/gongfeng/token", h.bindGongfengToken)
	mux.HandleFunc("GET "+base+"/connections/{connectionID}/gongfeng/projects", h.listGongfengProjects)
	mux.HandleFunc("GET "+base+"/connections/{connectionID}/gongfeng/configuration", h.getGongfengConfiguration)
	mux.HandleFunc("GET "+base+"/connections/{connectionID}/gongfeng/work", h.listGongfengWork)
	mux.HandleFunc("POST "+base+"/gongfeng/repositories", h.createGongfengRepository)
	repo := base + "/connections/{connectionID}/gongfeng/repositories/{repositoryID}"
	mux.HandleFunc("PATCH "+repo, h.manageGongfengRepository)
	mux.HandleFunc("POST "+repo+"/{action}", h.manageGongfengRepository)
	mux.HandleFunc("POST "+base+"/connections/{connectionID}/gongfeng/rotate-webhook", h.rotateGongfengWebhook)
}

func writeGongfengError(w http.ResponseWriter, r *http.Request, err error) {
	var limited *gitapi.ErrRateLimited
	switch {
	case errors.As(err, &limited):
		writeError(w, r, 429, "GONGFENG_RATE_LIMITED", "Gongfeng request is rate limited")
	case errors.Is(err, gitapi.ErrTokenRevoked):
		writeIntegrationError(w, r, integrations.ErrNeedsReauth)
	case errors.Is(err, gongfeng.ErrForbidden):
		writeIntegrationError(w, r, integrations.ErrForbidden)
	case errors.Is(err, gitapi.ErrNotFound):
		writeIntegrationError(w, r, integrations.ErrInvalidRepository)
	case errors.Is(err, gongfeng.ErrIncomplete):
		writeError(w, r, 502, "GONGFENG_INCOMPLETE", "Gongfeng response is incomplete; no completion was assumed")
	case errors.Is(err, gongfeng.ErrInvalid):
		writeError(w, r, 422, "GONGFENG_INVALID", "Gongfeng request or response is invalid")
	case errors.Is(err, gitapi.ErrUnknownResult):
		writeError(w, r, 409, "GONGFENG_RESULT_UNKNOWN", "Gongfeng result must be reconciled before retrying")
	case errors.Is(err, gitapi.ErrRequestFailed):
		writeError(w, r, 502, "GONGFENG_UNAVAILABLE", "Gongfeng request failed")
	case errors.Is(err, integrations.ErrGongfengQueueFull):
		writeError(w, r, 503, "GONGFENG_QUEUE_FULL", "Gongfeng pending work limit reached")
	default:
		writeIntegrationError(w, r, err)
	}
}
func (h *Handler) bindGongfengToken(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input integrations.GongfengTokenInput
	if decodeJSON(w, r, &input) != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var plan integrations.GongfengTokenPlan
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		plan, e = h.integrations.PrepareGongfengToken(ctx, tx, call.organizationID, call.selected.Actor, input)
		return e
	})
	var user gongfeng.User
	if err == nil {
		user, err = h.integrations.ValidateGongfengToken(r.Context(), plan)
	}
	var out integrations.Connection
	if err == nil {
		err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			var e error
			out, e = h.integrations.SaveGongfengToken(ctx, tx, call.organizationID, call.selected.Actor, plan, user)
			if e != nil {
				return e
			}
			return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.gongfeng_credentials_saved", "integration_connection", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "gongfeng"})
		})
	}
	if err != nil {
		writeGongfengError(w, r, err)
		return
	}
	status := http.StatusOK
	if input.ConnectionID == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}
func (h *Handler) listGongfengProjects(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	page := 1
	var err error
	if raw := r.URL.Query().Get("page"); raw != "" {
		page, err = strconv.Atoi(raw)
	}
	if err != nil || page < 1 || page > gongfeng.MaxPages {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var access integrations.GongfengAccess
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		access, e = h.integrations.PrepareGongfeng(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return e
	})
	var result gongfeng.Page[gongfeng.Project]
	if err == nil {
		result, err = access.Projects(r.Context(), r.URL.Query().Get("q"), page)
	}
	if errors.Is(err, gitapi.ErrTokenRevoked) && access.ID != "" {
		_ = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			if e := h.integrations.CheckGongfengAccess(ctx, tx, call.organizationID, call.selected.Actor, access); e != nil {
				return e
			}
			return h.integrations.MarkGongfengTokenRevoked(ctx, tx, access.ID, access.Version)
		})
	}
	if err == nil {
		err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			return h.integrations.CheckGongfengAccess(ctx, tx, call.organizationID, call.selected.Actor, access)
		})
	}
	if err != nil {
		writeGongfengError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": result.Items, "next_page": result.Next, "connection_version": access.Version})
}
func (h *Handler) getGongfengConfiguration(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.GongfengConfiguration
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.GetGongfengConfiguration(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.URL.Query().Get("after"))
		return e
	})
	if err != nil {
		writeGongfengError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) listGongfengWork(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.GongfengWorkPage
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.ListGongfengWork(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.URL.Query().Get("after"))
		return e
	})
	if err != nil {
		writeGongfengError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) createGongfengRepository(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input integrations.GongfengRepositoryInput
	if decodeJSON(w, r, &input) != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	id, err := membership.NewID()
	if err != nil {
		writeIntegrationError(w, r, err)
		return
	}
	var plan integrations.GongfengRepositoryPlan
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		if e := call.authorizeSpace(ctx, tx, service, input.SpaceID, policy.ActionCreateObject, id); e != nil {
			return e
		}
		var e error
		plan, e = h.integrations.PrepareGongfengRepository(ctx, tx, call.organizationID, call.selected.Actor, id, input)
		return e
	})
	if err == nil {
		plan, err = h.integrations.ValidateGongfengRepository(r.Context(), plan)
	}
	var out integrations.GongfengRepository
	if err == nil {
		err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
			if e := call.authorizeSpace(ctx, tx, service, input.SpaceID, policy.ActionCreateObject, id); e != nil {
				return e
			}
			var e error
			out, e = h.integrations.SaveGongfengRepository(ctx, tx, call.organizationID, call.selected.Actor, plan)
			if e != nil {
				return e
			}
			return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.gongfeng_repository_created", "integration_sync_link", id, input.SpaceID, call.selected.Actor.AuthorizationVersion, map[string]string{"source": "gongfeng"})
		})
	}
	if err != nil {
		writeGongfengError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) receiveGongfengWebhook(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer func() {
		// net/http may drain an unread body in Close/finishRequest. Keep the
		// bounded deadline until it is closed, including malformed slow bodies.
		_ = r.Body.Close()
		_ = controller.SetReadDeadline(time.Time{})
	}()
	token := r.PathValue("token")
	if len(token) < minGitWebhookToken || len(token) > 128 {
		writeError(w, r, 404, "NOT_FOUND", "webhook is not available")
		return
	}
	hash := integrations.TokenHash(token)
	if !h.allowKey(w, r, h.webhookLimiter, hex.EncodeToString(hash)) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, gitWebhookBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			writeError(w, r, 413, "PAYLOAD_TOO_LARGE", "webhook body exceeds 5 MiB")
		} else {
			writeError(w, r, 400, "INVALID_REQUEST", "request body is invalid")
		}
		return
	}
	if h.pool == nil {
		writeError(w, r, 503, "NOT_READY", "database is not ready")
		return
	}
	var lookup integrations.GitWebhookLookup
	err = h.pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id::text,connection_id::text FROM openfox_private.resolve_git_webhook($1) WHERE provider='gongfeng'`, hash).Scan(&lookup.OrganizationID, &lookup.ConnectionID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, 404, "NOT_FOUND", "webhook is not available")
		return
	}
	var receipt integrations.GongfengReceipt
	if err == nil {
		err = h.runAsWorker(ctx, lookup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
			// Lock waits belong to the two-second reception budget. Returning 200
			// before this transaction commits would lose events on a worker crash.
			if _, e := tx.Exec(ctx, `SET LOCAL lock_timeout='1500ms'`); e != nil {
				return e
			}
			var e error
			receipt, e = h.integrations.ReceiveGongfeng(ctx, tx, lookup, token, r.Header, body)
			return e
		})
	}
	if errors.Is(err, gongfeng.ErrAuthentication) {
		writeError(w, r, 401, "SIGNATURE_REJECTED", "webhook authentication is invalid")
		return
	}
	if err != nil {
		writeGongfengError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func (h *Handler) manageGongfengRepository(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	action := r.PathValue("action")
	var input integrations.GongfengRepositoryUpdate
	var reconcile integrations.GongfengReconcileInput
	if action == "reconcile" {
		if decodeJSON(w, r, &reconcile) != nil {
			writeIntegrationError(w, r, integrations.ErrInvalidRequest)
			return
		}
	} else {
		if action != "" && action != "remove" && action != "retry" {
			writeIntegrationError(w, r, integrations.ErrNotFound)
			return
		}
		if decodeJSON(w, r, &input) != nil {
			writeIntegrationError(w, r, integrations.ErrInvalidRequest)
			return
		}
	}
	var out integrations.GongfengRepository
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		switch action {
		case "":
			out, e = h.integrations.UpdateGongfengRepository(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.PathValue("repositoryID"), input)
		case "remove":
			out, e = h.integrations.RemoveGongfengRepository(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.PathValue("repositoryID"), input)
		case "retry":
			out, e = h.integrations.RetryGongfengRepository(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.PathValue("repositoryID"), input)
		case "reconcile":
			out, e = h.integrations.ReconcileGongfeng(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.PathValue("repositoryID"), reconcile)
		}
		if e != nil {
			return e
		}
		verb := action
		if verb == "" {
			verb = "updated"
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.gongfeng_repository_"+verb, "integration_sync_link", out.SyncLinkID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "gongfeng"})
	})
	if err != nil {
		writeGongfengError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (h *Handler) rotateGongfengWebhook(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Version uint64 `json:"version"`
	}
	if decodeJSON(w, r, &input) != nil {
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out integrations.Connection
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.RotateGongfengWebhook(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), input.Version)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.gongfeng_webhook_rotated", "integration_connection", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "gongfeng"})
	})
	if err != nil {
		writeGongfengError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
