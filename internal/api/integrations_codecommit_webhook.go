package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations"
	"openfox/internal/membership"
)

func (h *Handler) receiveCodeCommitBridge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = r.Body.Close(); _ = controller.SetReadDeadline(time.Time{}) }()
	bridge := r.PathValue("bridgeID")
	authorization := r.Header.Values("Authorization")
	if !membership.ValidateUUID(bridge) {
		writeCodeCommitError(w, r, integrations.ErrNotFound)
		return
	}
	if len(authorization) != 1 || !strings.HasPrefix(authorization[0], "Bearer ") || len(authorization[0]) != 50 {
		writeError(w, r, 401, "CODECOMMIT_BRIDGE_UNAUTHORIZED", "bridge authentication failed")
		return
	}
	token := strings.TrimPrefix(authorization[0], "Bearer ")
	// The bridge ID is public. Invalid credentials must not consume the real
	// bridge token's budget; the shared limiter bounds retained buckets.
	tokenHash := sha256.Sum256([]byte(token))
	decision := h.webhookLimiter.Allow("codecommit:" + bridge + ":" + hex.EncodeToString(tokenHash[:]))
	if !decision.Allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(decision.RetryAfter/time.Second)+1))
		writeError(w, r, 429, "CODECOMMIT_BRIDGE_RATE_LIMITED", "bridge rate limit reached")
		return
	}
	if h.pool == nil {
		writeError(w, r, 503, "UNAVAILABLE", "bridge is unavailable")
		return
	}
	var lookup integrations.CodeCommitBridgeLookup
	e := h.pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id::text,connection_id::text,repository_id::text FROM openfox_private.resolve_codecommit_bridge($1::uuid)`, bridge).Scan(&lookup.OrganizationID, &lookup.ConnectionID, &lookup.RepositoryID)
	})
	if errors.Is(e, pgx.ErrNoRows) {
		writeCodeCommitError(w, r, integrations.ErrNotFound)
		return
	}
	if e != nil {
		writeError(w, r, 503, "UNAVAILABLE", "bridge is unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, integrations.CodeCommitEnvelopeLimit)
	body, e := io.ReadAll(r.Body)
	if e != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(e, &tooLarge) {
			writeError(w, r, 413, "PAYLOAD_TOO_LARGE", "bridge envelope is too large")
		} else {
			writeError(w, r, 400, "INVALID_REQUEST", "bridge envelope could not be read")
		}
		return
	}
	var out integrations.CodeCommitReceipt
	e = h.runAsWorker(ctx, lookup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SET LOCAL lock_timeout='2500ms'`); e != nil {
			return e
		}
		var e error
		out, e = h.integrations.ReceiveCodeCommit(ctx, tx, lookup, bridge, token, body)
		return e
	})
	if errors.Is(e, integrations.ErrCodeCommitBridgeUnauthorized) {
		writeError(w, r, 401, "CODECOMMIT_BRIDGE_UNAUTHORIZED", "bridge authentication failed")
		return
	}
	if e != nil {
		writeCodeCommitError(w, r, e)
		return
	}
	// The organization transaction has committed; AWS is still read by the worker.
	writeJSON(w, 202, out)
}
