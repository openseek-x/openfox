package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/integrations"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/yunxiao"
)

func (h *Handler) registerYunxiaoRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/organizations/{organizationID}/integrations/yunxiao/personal-token", h.bindYunxiaoPersonal)
	mux.HandleFunc("PUT /v1/organizations/{organizationID}/integrations/connections/{connectionID}/yunxiao/credentials", h.updateYunxiaoCredentials)
}
func writeYunxiaoError(w http.ResponseWriter, r *http.Request, err error) {
	var limited *gitapi.ErrRateLimited
	switch {
	case errors.Is(err, integrations.ErrYunxiaoIdentity):
		writeError(w, r, 400, "YUNXIAO_IDENTITY_MISMATCH", "Use your own PAT with the same current email and organization")
	case errors.Is(err, integrations.ErrYunxiaoHolder):
		writeError(w, r, 400, "YUNXIAO_HOLDER_MISMATCH", "Disconnect and reconnect to change the PAT holder")
	case errors.Is(err, yunxiao.ErrForbidden):
		writeError(w, r, 403, "YUNXIAO_PERMISSION_DENIED", "Yunxiao organization or resource read permission is missing")
	case errors.As(err, &limited):
		writeError(w, r, 429, "YUNXIAO_RATE_LIMITED", "Yunxiao request limit reached; try later")
	case errors.Is(err, yunxiao.ErrInvalid):
		writeIntegrationError(w, r, integrations.ErrInvalidRequest)
	case errors.Is(err, gitapi.ErrNotFound):
		writeIntegrationError(w, r, integrations.ErrNotFound)
	case errors.Is(err, gitapi.ErrTokenRevoked):
		writeIntegrationError(w, r, integrations.ErrNeedsReauth)
	case errors.Is(err, yunxiao.ErrProtocol), errors.Is(err, gitapi.ErrRequestFailed):
		writeIntegrationError(w, r, integrations.ErrUpstreamUnavailable)
	case errors.Is(err, yunxiao.ErrLimit):
		writeError(w, r, 422, "YUNXIAO_SOURCE_LIMIT", "Yunxiao source exceeds the import size limit")
	case errors.Is(err, idempotency.ErrConflict):
		writeError(w, r, 409, "IDEMPOTENCY_CONFLICT", "Idempotency key was used with different parameters")
	case errors.Is(err, idempotency.ErrInvalidKey):
		writeError(w, r, 400, "INVALID_IDEMPOTENCY_KEY", "Invalid idempotency key")
	default:
		writeIntegrationError(w, r, err)
	}
}
func (h *Handler) bindYunxiaoOrganization(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.ProviderTokenInput
	if decodeJSONLimit(w, r, 8<<10, &in) != nil {
		writeYunxiaoError(w, r, integrations.ErrInvalidRequest)
		return
	}
	key := r.Header.Get(idempotency.HeaderName)
	const operation = "integration.yunxiao.organization"
	var out integrations.Connection
	var replay bool
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		if err := h.integrations.CheckProviderToken(ctx, tx, call.organizationID, call.selected.Actor, "yunxiao", in); err != nil {
			return err
		}
		claim, err := idempotency.Lookup(ctx, tx, operation, key, in)
		if err != nil {
			return err
		}
		replay = claim.Replay
		if replay {
			out, err = h.integrations.ReplayYunxiao(ctx, tx, call.organizationID, call.selected.Actor, claim.ResourceID, false)
		}
		return err
	})
	if err != nil {
		writeYunxiaoError(w, r, err)
		return
	}
	if !replay {
		user, e := h.integrations.VerifyYunxiao(r.Context(), in)
		if e != nil {
			writeYunxiaoError(w, r, e)
			return
		}
		err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			if err := h.integrations.CheckProviderToken(ctx, tx, call.organizationID, call.selected.Actor, "yunxiao", in); err != nil {
				return err
			}
			claim, err := idempotency.Begin(ctx, tx, operation, key, in)
			if err != nil {
				return err
			}
			replay = claim.Replay
			if replay {
				out, err = h.integrations.ReplayYunxiao(ctx, tx, call.organizationID, call.selected.Actor, claim.ResourceID, false)
				return err
			}
			out, err = h.integrations.SaveYunxiao(ctx, tx, call.organizationID, call.selected.Actor, in, user)
			if err != nil {
				return err
			}
			if err = idempotency.Complete(ctx, tx, claim, out.ID); err != nil {
				return err
			}
			return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.connection_created", "integration_connection", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "yunxiao", "result": "ORGANIZATION"})
		})
	}
	if err != nil {
		writeYunxiaoError(w, r, err)
		return
	}
	if replay {
		w.Header().Set(idempotency.ReplayedHeader, "true")
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
func (h *Handler) bindYunxiaoPersonal(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in struct {
		ConnectionID string `json:"connection_id"`
		Token        string `json:"token"`
	}
	if decodeJSONLimit(w, r, 8<<10, &in) != nil || !yunxiao.ValidToken(in.Token) {
		writeYunxiaoError(w, r, integrations.ErrInvalidRequest)
		return
	}
	key := r.Header.Get(idempotency.HeaderName)
	const operation = "integration.yunxiao.personal"
	var target integrations.YunxiaoPersonalTarget
	var out integrations.Connection
	var replay bool
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var err error
		target, err = h.integrations.PrepareYunxiaoPersonal(ctx, tx, call.organizationID, call.selected.Actor, in.ConnectionID)
		if err != nil {
			return err
		}
		claim, err := idempotency.Lookup(ctx, tx, operation, key, in)
		if err != nil {
			return err
		}
		replay = claim.Replay
		if replay {
			out, err = h.integrations.ReplayYunxiao(ctx, tx, call.organizationID, call.selected.Actor, claim.ResourceID, true)
		}
		return err
	})
	if err != nil {
		writeYunxiaoError(w, r, err)
		return
	}
	if !replay {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		user, e := h.integrations.VerifyYunxiaoPersonal(ctx, target, in.Token)
		if e != nil {
			writeYunxiaoError(w, r, e)
			return
		}
		err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
			claim, err := idempotency.Begin(ctx, tx, operation, key, in)
			if err != nil {
				return err
			}
			replay = claim.Replay
			if replay {
				out, err = h.integrations.ReplayYunxiao(ctx, tx, call.organizationID, call.selected.Actor, claim.ResourceID, true)
				return err
			}
			out, err = h.integrations.SaveYunxiaoPersonal(ctx, tx, call.organizationID, call.selected.Actor, target, user)
			if err != nil {
				return err
			}
			if err = idempotency.Complete(ctx, tx, claim, out.ID); err != nil {
				return err
			}
			return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.connection_created", "integration_connection", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "yunxiao", "result": "PERSONAL"})
		})
	}
	if err != nil {
		writeYunxiaoError(w, r, err)
		return
	}
	if replay {
		w.Header().Set(idempotency.ReplayedHeader, "true")
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
func (h *Handler) updateYunxiaoCredentials(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version uint64 `json:"version"`
		Token   string `json:"token"`
	}
	if decodeJSONLimit(w, r, 8<<10, &in) != nil || in.Version == 0 || !yunxiao.ValidToken(in.Token) {
		writeYunxiaoError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var a integrations.YunxiaoAccess
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		a, e = h.integrations.PrepareYunxiao(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), false)
		return e
	})
	if err == nil && a.Version != in.Version {
		err = integrations.ErrVersionConflict
	}
	if err != nil {
		writeYunxiaoError(w, r, err)
		return
	}
	user, err := h.integrations.VerifyYunxiao(r.Context(), integrations.ProviderTokenInput{Method: "token", CompanyID: a.CompanyID, Secret: in.Token})
	if err != nil {
		writeYunxiaoError(w, r, err)
		return
	}
	var out integrations.Connection
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.UpdateYunxiao(ctx, tx, call.organizationID, call.selected.Actor, a, in.Token, user)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.credentials_updated", "integration_connection", out.ID, "", call.selected.Actor.AuthorizationVersion, map[string]string{"source": "yunxiao"})
	})
	if err != nil {
		writeYunxiaoError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
