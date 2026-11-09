package api

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/deviceauth"
	"openfox/internal/identity"
	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

// DeviceHandler owns routes that deliberately use a device principal or a
// user principal. It is registered separately so the parent handler can wire
// the device service without changing the existing user handler.
type DeviceHandler struct {
	userAuth   identity.Service
	deviceAuth deviceauth.Service
	pool       *postgres.Pool
}

func NewDeviceHandler(userAuth identity.Service, deviceAuth deviceauth.Service, pool *postgres.Pool) *DeviceHandler {
	return &DeviceHandler{userAuth: userAuth, deviceAuth: deviceAuth, pool: pool}
}

// RegisterDeviceRoutes mounts the WP-03 routes into the caller's /v1 mux.
// The parent application remains responsible for calling this from New.
func RegisterDeviceRoutes(mux *http.ServeMux, userAuth identity.Service, deviceAuth deviceauth.Service, pool *postgres.Pool) {
	if mux == nil {
		return
	}
	h := NewDeviceHandler(userAuth, deviceAuth, pool)
	mux.HandleFunc("POST /v1/device/auth/challenge", h.challenge)
	mux.HandleFunc("POST /v1/device/auth/token", h.token)
	mux.HandleFunc("GET /v1/device/organizations/{organizationID}/agents", h.listDeviceAgents)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/devices", h.registerDevice)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/devices/{deviceID}/revoke", h.revokeDeviceGrant)
}

type deviceChallengeRequest struct {
	DeviceID string `json:"device_id"`
	KeyID    string `json:"key_id"`
}

type deviceTokenRequest struct {
	ChallengeID string `json:"challenge_id"`
	DeviceID    string `json:"device_id"`
	KeyID       string `json:"key_id"`
	Signature   string `json:"signature"`
}

type deviceRegistrationRequest struct {
	DeviceKey string `json:"device_key"`
	Name      string `json:"name"`
	InstallID string `json:"install_id"`
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	MaxSlots  int    `json:"max_slots"`
}

type deviceChallengeResponse struct {
	ChallengeID string    `json:"challenge_id"`
	DeviceID    string    `json:"device_id"`
	KeyID       string    `json:"key_id"`
	Nonce       string    `json:"nonce"`
	Challenge   string    `json:"challenge"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type deviceTokenResponse struct {
	DeviceID  string    `json:"device_id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type deviceRegistrationResponse struct {
	DeviceID       string `json:"device_id"`
	OrganizationID string `json:"organization_id"`
	KeyID          string `json:"key_id"`
}

type deviceAgentResponse struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	Generation    int64  `json:"generation"`
	MaxSlots      int    `json:"max_slots"`
	ReservedSlots int    `json:"reserved_slots"`
}

type userAuthContext struct {
	principal identity.Principal
	viaCookie bool
}

func (h *DeviceHandler) challenge(w http.ResponseWriter, r *http.Request) {
	var input deviceChallengeRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	result, err := h.deviceAuth.IssueChallenge(r.Context(), deviceauth.ChallengeRequest{
		DeviceID: input.DeviceID,
		KeyID:    input.KeyID,
	})
	if err != nil {
		writeDeviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, deviceChallengeResponse{
		ChallengeID: result.ID,
		DeviceID:    result.DeviceID,
		KeyID:       result.KeyID,
		Nonce:       deviceauth.EncodeBase64URL(result.Nonce),
		Challenge:   deviceauth.EncodeBase64URL(result.Challenge),
		ExpiresAt:   result.ExpiresAt,
	})
}

func (h *DeviceHandler) token(w http.ResponseWriter, r *http.Request) {
	var input deviceTokenRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	signature, err := deviceauth.DecodeBase64URL(input.Signature, ed25519.SignatureSize)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	result, err := h.deviceAuth.Verify(r.Context(), deviceauth.VerifyRequest{
		ChallengeID: input.ChallengeID,
		DeviceID:    input.DeviceID,
		KeyID:       input.KeyID,
		Signature:   signature,
	})
	if err != nil {
		writeDeviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, deviceTokenResponse{
		DeviceID:  result.DeviceID,
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
	})
}

func (h *DeviceHandler) listDeviceAgents(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticateDevice(r)
	if err != nil {
		writeDeviceError(w, r, err)
		return
	}
	if h.pool == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
		return
	}
	organizationID := r.PathValue("organizationID")
	selection, err := h.deviceAuth.SelectOrganization(r.Context(), principal.DeviceID, organizationID)
	if err != nil {
		writeDeviceSelectionError(w, r, err)
		return
	}
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
	agents := make([]deviceAgentResponse, 0)
	err = h.pool.RunForOrganization(r.Context(), organizationID, tenancy.SourceDeviceRequest, selector,
		func(ctx context.Context, tx pgx.Tx) error {
			// Recheck and lock the grant after tenant context is installed. The
			// control-plane selector can race with a concurrent grant revoke.
			var permissionVersion int64
			if err := tx.QueryRow(ctx, `
				SELECT permission_version
				  FROM openfox.device_grants
				 WHERE organization_id = openfox_private.current_organization_id()
				   AND device_id = $1::uuid
				   AND status = 'ACTIVE'
				 FOR SHARE
			`, principal.DeviceID).Scan(&permissionVersion); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return deviceauth.ErrOrganizationNotFound
				}
				return err
			}
			if permissionVersion <= 0 || uint64(permissionVersion) != selection.AuthorizationVersion {
				return deviceauth.ErrOrganizationNotFound
			}
			rows, err := tx.Query(ctx, `
				SELECT id::text, status, generation, max_slots, reserved_slots
				  FROM openfox.agents
				 WHERE organization_id = $1::uuid
				   AND device_id = $2::uuid
				   AND status = 'ACTIVE'
				 ORDER BY id
			`, organizationID, principal.DeviceID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var agent deviceAgentResponse
				if err := rows.Scan(&agent.ID, &agent.Status, &agent.Generation, &agent.MaxSlots, &agent.ReservedSlots); err != nil {
					return err
				}
				agents = append(agents, agent)
			}
			return rows.Err()
		})
	if err != nil {
		writeDeviceSelectionError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (h *DeviceHandler) registerDevice(w http.ResponseWriter, r *http.Request) {
	authContext, err := h.authenticateUser(r)
	if err != nil {
		writeUserDeviceError(w, r, err)
		return
	}
	if authContext.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return
	}
	var input deviceRegistrationRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	publicKey, err := deviceauth.DecodeBase64URL(input.PublicKey, ed25519.PublicKeySize)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	organizationID := r.PathValue("organizationID")
	selection, role, err := h.userAuth.SelectOrganization(r.Context(), authContext.principal.UserID, organizationID)
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return
	}
	if !canManageDevice(role) {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "action is not allowed")
		return
	}
	deviceID, err := h.deviceAuth.RegisterDevice(r.Context(), deviceauth.Registration{
		Organization: selection,
		DeviceKey:    input.DeviceKey,
		Name:         input.Name,
		InstallID:    input.InstallID,
		KeyID:        input.KeyID,
		PublicKey:    ed25519.PublicKey(publicKey),
		MaxSlots:     input.MaxSlots,
	})
	if err != nil {
		writeDeviceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, deviceRegistrationResponse{
		DeviceID:       deviceID,
		OrganizationID: selection.OrganizationID,
		KeyID:          strings.TrimSpace(input.KeyID),
	})
}

func (h *DeviceHandler) revokeDeviceGrant(w http.ResponseWriter, r *http.Request) {
	authContext, err := h.authenticateUser(r)
	if err != nil {
		writeUserDeviceError(w, r, err)
		return
	}
	if authContext.viaCookie && !validCSRF(r) {
		writeError(w, r, http.StatusForbidden, "CSRF_REQUIRED", "a CSRF token is required")
		return
	}
	organizationID := r.PathValue("organizationID")
	selection, role, err := h.userAuth.SelectOrganization(r.Context(), authContext.principal.UserID, organizationID)
	if err != nil {
		writeOrganizationSelectionError(w, r, err)
		return
	}
	if !canManageDevice(role) {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "action is not allowed")
		return
	}
	err = h.deviceAuth.RevokeGrant(r.Context(), selection, r.PathValue("deviceID"), time.Now().UTC())
	if err != nil {
		writeDeviceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *DeviceHandler) authenticateDevice(r *http.Request) (deviceauth.Principal, error) {
	token := bearerToken(r)
	if token == "" {
		return deviceauth.Principal{}, deviceauth.ErrSessionNotFound
	}
	return h.deviceAuth.Authenticate(r.Context(), token)
}

func (h *DeviceHandler) authenticateUser(r *http.Request) (userAuthContext, error) {
	token, viaCookie := bearerToken(r), false
	if token == "" {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			token, viaCookie = cookie.Value, true
		}
	}
	if token == "" {
		return userAuthContext{}, identity.ErrSessionNotFound
	}
	principal, err := h.userAuth.AuthenticateSession(r.Context(), token)
	if err != nil {
		return userAuthContext{}, err
	}
	return userAuthContext{principal: principal, viaCookie: viaCookie}, nil
}

func canManageDevice(role string) bool {
	switch strings.ToUpper(strings.TrimSpace(role)) {
	case "OWNER", "ADMIN":
		return true
	default:
		return false
	}
}

func writeDeviceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, deviceauth.ErrInvalidRequest):
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request is invalid")
	case errors.Is(err, deviceauth.ErrChallengeConflict):
		writeError(w, r, http.StatusConflict, "CHALLENGE_CONFLICT", "challenge is stale or already consumed")
	case errors.Is(err, deviceauth.ErrInvalidSignature),
		errors.Is(err, deviceauth.ErrDeviceUnauthenticated),
		errors.Is(err, deviceauth.ErrChallengeNotFound),
		errors.Is(err, deviceauth.ErrSessionNotFound):
		writeError(w, r, http.StatusUnauthorized, "DEVICE_UNAUTHENTICATED", "device authentication is required")
	case errors.Is(err, deviceauth.ErrOrganizationNotFound):
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
	case errors.Is(err, deviceauth.ErrDeviceAlreadyRegistered):
		writeError(w, r, http.StatusConflict, "DEVICE_CONFLICT", "device registration conflicts with an existing device")
	case errors.Is(err, postgres.ErrNilPool):
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "database is not ready")
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "request could not be completed")
	}
}

func writeDeviceSelectionError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, deviceauth.ErrOrganizationNotFound) || errors.Is(err, postgres.ErrOrganizationNotFound) || errors.Is(err, tenancy.ErrOrganizationSelection) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
		return
	}
	writeDeviceError(w, r, err)
}

func writeUserDeviceError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, identity.ErrSessionNotFound) {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "authentication is required")
		return
	}
	writeDeviceError(w, r, err)
}
