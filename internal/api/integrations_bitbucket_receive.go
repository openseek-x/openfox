package api

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations"
)

func readBitbucketWebhookBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(2 * time.Second))
	r.Body = http.MaxBytesReader(w, r.Body, integrations.MaxBitbucketWebhookBytes)
	defer func() {
		// Close can drain an unread request body. Keep the deadline until it finishes.
		_ = r.Body.Close()
		_ = controller.SetReadDeadline(time.Time{})
	}()
	return io.ReadAll(r.Body)
}
func (h *Handler) receiveBitbucketWebhook(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if len(token) < minGitWebhookToken || len(token) > 128 {
		writeError(w, r, 404, "NOT_FOUND", "webhook is not available")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, r, 415, "UNSUPPORTED_MEDIA_TYPE", "webhook requires application/json")
		return
	}
	hash := integrations.TokenHash(token)
	if !h.allowKey(w, r, h.webhookLimiter, hex.EncodeToString(hash)) {
		return
	}
	body, err := readBitbucketWebhookBody(w, r)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			writeError(w, r, 413, "PAYLOAD_TOO_LARGE", "webhook body exceeds 5 MiB")
		} else {
			writeError(w, r, 400, "INVALID_REQUEST", "webhook body is incomplete or invalid")
		}
		return
	}
	if h.pool == nil {
		writeError(w, r, 503, "NOT_READY", "database is not ready")
		return
	}
	var lookup integrations.GitWebhookLookup
	var provider string
	err = h.pool.RunControlPlane(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id::text,connection_id::text,provider FROM openfox_private.resolve_git_webhook($1)`, hash).Scan(&lookup.OrganizationID, &lookup.ConnectionID, &provider)
	})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && provider != integrations.ProviderBitbucket {
		writeError(w, r, 404, "NOT_FOUND", "webhook is not available")
		return
	}
	if err != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	var outcome integrations.GitWebhookOutcome
	err = h.runAsWorker(r.Context(), lookup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		outcome, e = h.integrations.ReceiveBitbucket(ctx, tx, lookup, token, r.Header, body)
		return e
	})
	if err != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	switch {
	case !outcome.Found:
		writeError(w, r, 404, "NOT_FOUND", "webhook is not available")
	case outcome.Status == 401:
		writeError(w, r, 401, "SIGNATURE_REJECTED", "signature is missing or invalid")
	case outcome.Status == 403:
		writeError(w, r, 403, "REPOSITORY_NOT_ALLOWED", "webhook repository scope is not allowed")
	case outcome.Status == 409:
		writeError(w, r, 409, "DELIVERY_CONFLICT", "delivery key was already used for different content")
	case outcome.Status == 400:
		writeError(w, r, 400, "INVALID_REQUEST", "webhook delivery is invalid")
	case outcome.Status != 200:
		writeError(w, r, 500, "INTERNAL_ERROR", "request could not be completed")
	default:
		writeJSON(w, 200, map[string]any{"accepted": true, "duplicate": outcome.Duplicate})
	}
}
