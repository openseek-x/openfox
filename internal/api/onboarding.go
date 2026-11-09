package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/collaboration"
	"openfox/internal/identity"
)

// maxOwnedOrganizations bounds self-service organization creation per user.
const maxOwnedOrganizations = 3

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

type organizationResponse struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	IconKind  *string   `json:"icon_kind"`
	Icon      *string   `json:"icon"`
	IconURL   *string   `json:"icon_url"`
}

type createOrganizationRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (h *Handler) registerOnboardingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/me/organizations", h.listMyOrganizations)
	mux.HandleFunc("POST /v1/organizations", h.createOrganization)
}

func (h *Handler) listMyOrganizations(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, false)
	if !ok {
		return
	}
	items := make([]organizationResponse, 0)
	err := h.pool.WithTransaction(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT organization_id::text, slug, name, role, created_at, icon_kind, COALESCE(icon, icon_upload_id::text)
			FROM openfox_private.list_user_organizations($1::uuid)
		`, auth.principal.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item organizationResponse
			if err := rows.Scan(&item.ID, &item.Slug, &item.Name, &item.Role, &item.CreatedAt, &item.IconKind, &item.Icon); err != nil {
				return err
			}
			if item.IconKind != nil && *item.IconKind == "upload" && item.Icon != nil {
				url := collaboration.IconURL(item.ID, *item.Icon)
				item.IconURL = &url
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"organizations": items})
}

// createOrganization lets a signed-in user create an organization they own.
// Retries with the same Idempotency-Key and body return the original result;
// the same key with a different body is rejected.
func (h *Handler) createOrganization(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, true)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !idempotencyKeyPattern.MatchString(key) {
		writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "a valid Idempotency-Key header is required")
		return
	}
	var input createOrganizationRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Slug = strings.ToLower(strings.TrimSpace(input.Slug))
	digest := sha256.Sum256([]byte(input.Name + "\x00" + input.Slug))
	requestID := collaborationRequestID(w, r)

	var item organizationResponse
	var replayed bool
	err := h.pool.WithTransaction(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT organization_id::text, slug, name, created_at, replayed
			FROM openfox_private.create_first_organization($1::uuid, $2, $3, $4, $5, $6, $7)
		`, auth.principal.UserID, input.Name, input.Slug, key, hex.EncodeToString(digest[:]), requestID,
			maxOwnedOrganizations).Scan(&item.ID, &item.Slug, &item.Name, &item.CreatedAt, &replayed)
	})
	if err != nil {
		writeOrganizationCreateError(w, r, err)
		return
	}
	item.Role = "OWNER"
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, item)
}

func writeOrganizationCreateError(w http.ResponseWriter, r *http.Request, err error) {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch {
		case pgError.ConstraintName == "organization_name_invalid":
			writeError(w, r, http.StatusUnprocessableEntity, "INVALID_NAME", "organization name must be 1-100 characters")
			return
		case pgError.ConstraintName == "organization_slug_invalid":
			writeError(w, r, http.StatusUnprocessableEntity, "INVALID_SLUG", "organization slug must be 3-40 lowercase letters, digits or hyphens")
			return
		case pgError.ConstraintName == "organizations_slug_unique":
			writeError(w, r, http.StatusConflict, "SLUG_TAKEN", "organization slug is already in use")
			return
		case pgError.ConstraintName == "organization_creations_key_conflict":
			writeError(w, r, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED", "idempotency key was used for a different request")
			return
		case pgError.ConstraintName == "organization_owned_limit":
			writeError(w, r, http.StatusConflict, "ORGANIZATION_LIMIT_REACHED", "owned organization limit reached")
			return
		}
	}
	writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
}

// requireSession authenticates the request and, for state-changing calls made
// with the session cookie, enforces the CSRF double-submit token.
func (h *Handler) requireSession(w http.ResponseWriter, r *http.Request, mutating bool) (authContext, bool) {
	auth, err := h.authenticate(r)
	if err != nil {
		if errors.Is(err, identity.ErrSessionNotFound) {
			writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
		} else {
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
		}
		return authContext{}, false
	}
	if mutating && auth.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return authContext{}, false
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return authContext{}, false
	}
	return auth, true
}
