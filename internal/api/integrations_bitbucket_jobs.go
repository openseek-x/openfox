package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func (h *Handler) registerBitbucketJobRoutes(mux *http.ServeMux) {
	base := "/v1/organizations/{organizationID}/integrations/bitbucket/connections/{connectionID}"
	mux.HandleFunc("GET "+base+"/jobs", h.getBitbucketJobs)
	mux.HandleFunc("POST "+base+"/jobs/retry", h.retryBitbucketJob)
}
func (h *Handler) getBitbucketJobs(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var jobs []integrations.BitbucketJobDiagnostic
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		jobs, e = h.integrations.ListBitbucketJobs(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return e
	})
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"jobs": jobs})
}
func (h *Handler) retryBitbucketJob(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.BitbucketRetryJobInput
	if decodeJSONLimit(w, r, 2048, &in) != nil {
		writeBitbucketError(w, r, integrations.ErrInvalidRequest)
		return
	}
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		return h.integrations.RetryBitbucketJob(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), in)
	})
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
