package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/secrets"
)

func (h *Handler) registerBitbucketRepositoryRoutes(mux *http.ServeMux) {
	h.registerBitbucketJobRoutes(mux)
	mux.HandleFunc("POST /v1/hooks/git/bitbucket/{token}", h.receiveBitbucketWebhook)
	base := "/v1/organizations/{organizationID}/integrations/bitbucket/connections/{connectionID}"
	mux.HandleFunc("GET "+base+"/repositories", h.getBitbucketSelections)
	mux.HandleFunc("GET "+base+"/repositories/available", h.listBitbucketRepositories)
	mux.HandleFunc("PUT "+base+"/repositories", h.saveBitbucketRepository)
	mux.HandleFunc("DELETE "+base+"/repositories/{selectionID}", h.removeBitbucketRepository)
	mux.HandleFunc("GET "+base+"/webhook", h.getBitbucketWebhook)
	mux.HandleFunc("POST "+base+"/webhook/rotate", h.rotateBitbucketWebhook)
}
func (h *Handler) bitbucketRuntime() integrations.BitbucketRuntime {
	return integrations.BitbucketRuntime{Pool: h.pool, Service: h.integrations}
}

// This cursor is sealed with the existing application Box. Its budget and fence
// are server-authenticated; a client cannot reset the count while advancing next.
// It contains no OAuth credentials. A new first-page request starts a new browse.
type bitbucketRepoCursor struct {
	Next       string   `json:"next"`
	Count      int      `json:"count"`
	Pages      int      `json:"pages"`
	Seen       []string `json:"seen"`
	Workspace  string   `json:"workspace"`
	Version    uint64   `json:"version"`
	Generation uint64   `json:"generation"`
	Expires    int64    `json:"expires"`
}

func bitbucketCursorAAD(org, actor, connection string) string {
	return "bitbucket-repository-cursor-v1\x00" + org + "\x00" + actor + "\x00" + connection
}
func openBitbucketRepoCursor(box *secrets.Box, raw, aad string, access integrations.BitbucketAccess) (bitbucketRepoCursor, error) {
	if box == nil {
		return bitbucketRepoCursor{}, integrations.ErrNotConfigured
	}
	if raw == "" {
		return bitbucketRepoCursor{Workspace: access.WorkspaceID, Version: access.ConnectionVersion, Generation: access.CredentialGeneration, Expires: time.Now().Add(10 * time.Minute).Unix()}, nil
	}
	var out bitbucketRepoCursor
	if len(raw) > 32768 {
		return out, integrations.ErrInvalidRequest
	}
	data, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil {
		return out, integrations.ErrInvalidRequest
	}
	var sealed secrets.Sealed
	if json.Unmarshal(data, &sealed) != nil {
		return out, integrations.ErrInvalidRequest
	}
	plain, e := box.Open(sealed, aad)
	if e != nil || json.Unmarshal(plain, &out) != nil {
		return out, integrations.ErrInvalidRequest
	}
	if out.Expires <= time.Now().Unix() || out.Next == "" || len(out.Next) > 8192 || out.Count < 0 || out.Count >= 1000 || out.Pages < 1 || out.Pages >= 20 || len(out.Seen) != out.Pages {
		return out, integrations.ErrInvalidRequest
	}
	if out.Workspace != access.WorkspaceID || out.Version != access.ConnectionVersion || out.Generation != access.CredentialGeneration {
		return out, integrations.ErrVersionConflict
	}
	return out, nil
}
func sealBitbucketRepoCursor(box *secrets.Box, cur bitbucketRepoCursor, aad string) (string, error) {
	if box == nil {
		return "", integrations.ErrNotConfigured
	}
	raw, e := json.Marshal(cur)
	if e != nil {
		return "", e
	}
	sealed, e := box.SealOAuth(raw, aad)
	if e != nil {
		return "", e
	}
	raw, e = json.Marshal(sealed)
	if e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func advanceBitbucketRepoCursor(cur bitbucketRepoCursor, page bitbucket.Page[bitbucket.Repository]) (bitbucketRepoCursor, bool, error) {
	if len(page.Values) > 100 || cur.Count+len(page.Values) > 1000 || len(page.Next) > 8192 {
		return cur, false, bitbucket.ErrIncomplete
	}
	cur.Count += len(page.Values)
	cur.Pages++
	if page.Next != "" {
		sum := sha256.Sum256([]byte(page.Next))
		key := hex.EncodeToString(sum[:])
		for _, seen := range cur.Seen {
			if seen == key {
				return cur, false, bitbucket.ErrIncomplete
			}
		}
		cur.Seen = append(cur.Seen, key)
	}
	cur.Next = page.Next
	limited := page.Next != "" && (cur.Count >= 1000 || cur.Pages >= 20)
	if limited {
		cur.Next = ""
	}
	return cur, limited, nil
}
func (h *Handler) getBitbucketSelections(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out []integrations.BitbucketRepositorySelection
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.ListBitbucketSelections(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return e
	})
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"repositories": out})
}
func (h *Handler) listBitbucketRepositories(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var target integrations.BitbucketAccess
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		target, e = h.integrations.PrepareBitbucketRepositories(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		return e
	})
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	aad := bitbucketCursorAAD(call.organizationID, call.selected.Actor.UserID, target.ConnectionID)
	cursor, e := openBitbucketRepoCursor(h.integrations.Box, r.URL.Query().Get("cursor"), aad, target)
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	ctx, cancel := bitbucket.WithTraversalBudget(r.Context())
	defer cancel()
	var page bitbucket.Page[bitbucket.Repository]
	_, e = h.bitbucketRuntime().DoReadBatch(ctx, call.organizationID, target.ConnectionID, target.ConnectionVersion, target.CredentialGeneration, func(token string) error {
		var e error
		page, e = h.integrations.Bitbucket.Repositories(ctx, token, target.WorkspaceID, cursor.Next)
		return e
	})
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	// Recheck the initiating member after network I/O before disclosing repository metadata.
	e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		current, e := h.integrations.PrepareBitbucketRepositories(ctx, tx, call.organizationID, call.selected.Actor, target.ConnectionID)
		if e != nil {
			return e
		}
		if current.ConnectionVersion != target.ConnectionVersion || current.CredentialGeneration != target.CredentialGeneration {
			return integrations.ErrVersionConflict
		}
		return nil
	})
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	cursor, limited, e := advanceBitbucketRepoCursor(cursor, page)
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	next := ""
	if cursor.Next != "" {
		next, e = sealBitbucketRepoCursor(h.integrations.Box, cursor, aad)
		if e != nil {
			writeBitbucketError(w, r, e)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"repositories": page.Values, "next_cursor": next, "loaded": cursor.Count, "limit_reached": limited})
}
func (h *Handler) saveBitbucketRepository(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in integrations.BitbucketRepositoryInput
	if decodeJSONLimit(w, r, 16<<10, &in) != nil {
		writeBitbucketError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var target integrations.BitbucketAccess
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		target, e = h.integrations.PrepareBitbucketRepository(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), in)
		return e
	})
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	ctx, cancel := bitbucket.WithTraversalBudget(r.Context())
	defer cancel()
	var verified bitbucket.Repository
	_, e = h.bitbucketRuntime().DoReadBatch(ctx, call.organizationID, target.ConnectionID, target.ConnectionVersion, target.CredentialGeneration, func(token string) error {
		var e error
		verified, e = h.integrations.Bitbucket.Repository(ctx, token, target.WorkspaceID, in.RepositoryID)
		return e
	})
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	var out integrations.BitbucketRepositorySelection
	e = call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.SaveBitbucketRepository(ctx, tx, call.organizationID, call.selected.Actor, target, in, verified)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.repository_saved", "integration_connection", target.ConnectionID, "", out.Version, map[string]string{"source": "bitbucket", "result": out.RepositoryID})
	})
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
func (h *Handler) removeBitbucketRepository(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version uint64 `json:"version"`
	}
	if decodeJSONLimit(w, r, 1024, &in) != nil {
		writeBitbucketError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out integrations.BitbucketRepositorySelection
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.RemoveBitbucketRepository(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), r.PathValue("selectionID"), in.Version)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.repository_removed", "integration_connection", out.ConnectionID, "", out.Version, map[string]string{"source": "bitbucket", "result": out.RepositoryID})
	})
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
func (h *Handler) getBitbucketWebhook(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	var out integrations.Webhook
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.GetBitbucketWebhook(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"))
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.webhook_viewed", "integration_connection", out.ConnectionID, "", out.Version, map[string]string{"source": "bitbucket"})
	})
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
func (h *Handler) rotateBitbucketWebhook(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Version uint64 `json:"version"`
		Confirm bool   `json:"confirm"`
	}
	if decodeJSONLimit(w, r, 1024, &in) != nil || !in.Confirm {
		writeBitbucketError(w, r, integrations.ErrInvalidRequest)
		return
	}
	var out integrations.Webhook
	e := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		var e error
		out, e = h.integrations.RotateBitbucketWebhook(ctx, tx, call.organizationID, call.selected.Actor, r.PathValue("connectionID"), in.Version)
		if e != nil {
			return e
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor, "integration.webhook_rotated", "integration_connection", out.ConnectionID, "", out.Version, map[string]string{"source": "bitbucket"})
	})
	if e != nil {
		writeBitbucketError(w, r, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
