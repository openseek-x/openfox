package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/conversations"
	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/executiondelivery"
	"openfox/internal/identity"
	"openfox/internal/membership"
	"openfox/internal/runtimeexports"
)

var (
	errRuntimeSessionUnavailable = errors.New("api: runtime session export unavailable")
	errRuntimeSessionNotFound    = errors.New("api: runtime session not found")
	errRuntimeSessionBusy        = errors.New("api: runtime session export busy")
)

type runtimeSessionExportResponse struct {
	ID         string    `json:"id"`
	Runtime    string    `json:"runtime"`
	Status     string    `json:"status"`
	ExpiresAt  time.Time `json:"expires_at"`
	WrappedKey string    `json:"wrapped_key,omitempty"`
	ErrorCode  string    `json:"error_code,omitempty"`
}

func (h *Handler) startRuntimeSessionExport(w http.ResponseWriter, r *http.Request) {
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	if !requireConversationCSRF(w, r, auth) {
		return
	}
	var input struct {
		PublicKey string `json:"public_key"`
	}
	if decodeJSON(w, r, &input) != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	if _, err := runtimeexports.ParsePublicKey(input.PublicKey); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PUBLIC_KEY", "browser encryption key is invalid")
		return
	}
	if h.storage == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "encrypted file storage is not configured")
		return
	}
	exportID, err := newUUID()
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var response runtimeSessionExportResponse
	err = h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := (conversations.Service{}).Get(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor); err != nil {
			return err
		}
		var runID, deviceID, provider, sessionID string
		err := tx.QueryRow(ctx, `
			SELECT r.id::text, r.computer_id::text, r.runtime,
			       COALESCE(NULLIF(ev.safe_payload #>> '{data,session_id}', ''),
			                NULLIF(ev.safe_payload #>> '{data,thread_id}', ''))
			  FROM openfox.runs r
			  JOIN LATERAL (
				SELECT e.safe_payload
				  FROM openfox.run_events e
				 WHERE e.organization_id = r.organization_id AND e.run_id = r.id
				   AND e.generation = r.generation AND e.type = 'started'
				 ORDER BY e.sequence DESC LIMIT 1
			  ) ev ON true
			 WHERE r.organization_id = openfox_private.current_organization_id()
			   AND r.conversation_id = $1::uuid
			   AND r.state IN ('SUCCEEDED','FAILED','CANCELED')
			   AND r.runtime IN ('codex','claude-code','opencode','pi')
			   AND COALESCE(NULLIF(ev.safe_payload #>> '{data,session_id}', ''),
			                NULLIF(ev.safe_payload #>> '{data,thread_id}', '')) IS NOT NULL
			 ORDER BY COALESCE(r.finished_at, r.updated_at) DESC, r.id DESC
			 LIMIT 1`, r.PathValue("conversationID")).Scan(&runID, &deviceID, &provider, &sessionID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && sessionID == "") {
			return errRuntimeSessionNotFound
		}
		if err != nil {
			return err
		}
		response.Runtime = provider
		var ownerID sql.NullString
		var deviceReady bool
		err = tx.QueryRow(ctx, `
			SELECT pd.owner_user_id::text,
			       pd.status = 'ACTIVE'
			       AND pd.last_seen_at >= clock_timestamp() - interval '3 minutes'
			       AND pd.capabilities->>'runtime_session_export' = 'true'
			       AND pd.capabilities->'runtimes'->$2->>'runnable' = 'true'
			       AND EXISTS (
					SELECT 1 FROM openfox.device_grants dg
					 WHERE dg.organization_id = openfox_private.current_organization_id()
					   AND dg.device_id = pd.id AND dg.status = 'ACTIVE'
			       )
			  FROM openfox.physical_devices pd
			 WHERE pd.id = $1::uuid`, deviceID, provider).Scan(&ownerID, &deviceReady)
		if errors.Is(err, pgx.ErrNoRows) {
			return errRuntimeSessionUnavailable
		}
		if err != nil {
			return err
		}
		if !ownerID.Valid || ownerID.String != auth.principal.UserID {
			return conversations.ErrForbidden
		}
		if !deviceReady {
			return errRuntimeSessionUnavailable
		}
		params, err := json.Marshal(devices.RuntimeSessionExportParams{
			ExportID: exportID, RunID: runID, Runtime: provider, SessionID: sessionID, PublicKey: input.PublicKey,
		})
		if err != nil {
			return err
		}
		command, created, err := devices.CreateCommandInTx(ctx, tx, selection, deviceID, devices.CommandRuntimeSessionExport, params)
		if err != nil {
			return err
		}
		if !created {
			return errRuntimeSessionBusy
		}
		return tx.QueryRow(ctx, `
			INSERT INTO openfox.runtime_session_exports (
				organization_id, id, conversation_id, run_id, device_id, requested_by_user_id,
				command_id, runtime, session_id, expires_at
			) VALUES (
				openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4::uuid,
				$5::uuid, $6::uuid, $7, $8, clock_timestamp() + interval '1 hour'
			) RETURNING expires_at`, exportID, r.PathValue("conversationID"), runID, deviceID,
			auth.principal.UserID, command.ID, provider, sessionID).Scan(&response.ExpiresAt)
	})
	if err != nil {
		writeRuntimeSessionExportError(w, r, err)
		return
	}
	response.ID, response.Status = exportID, "PENDING"
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusAccepted, response)
}

func (h *Handler) getRuntimeSessionExport(w http.ResponseWriter, r *http.Request) {
	if !membership.ValidateUUID(r.PathValue("exportID")) {
		writeDeliveryError(w, r, executiondelivery.ErrNotFound)
		return
	}
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var response runtimeSessionExportResponse
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := (conversations.Service{}).Get(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor); err != nil {
			return err
		}
		var wrapped []byte
		var commandStatus, commandError string
		err := tx.QueryRow(ctx, `
			SELECT e.runtime, e.status, e.expires_at, e.wrapped_key, e.error_code,
			       c.status, c.error_code
			  FROM openfox.runtime_session_exports e
			  LEFT JOIN openfox.device_commands c
			    ON c.organization_id = e.organization_id AND c.id = e.command_id
			 WHERE e.organization_id = openfox_private.current_organization_id()
			   AND e.id = $1::uuid AND e.conversation_id = $2::uuid
			   AND e.requested_by_user_id = $3::uuid AND e.expires_at > clock_timestamp()`,
			r.PathValue("exportID"), r.PathValue("conversationID"), auth.principal.UserID).
			Scan(&response.Runtime, &response.Status, &response.ExpiresAt, &wrapped, &response.ErrorCode, &commandStatus, &commandError)
		if errors.Is(err, pgx.ErrNoRows) {
			return executiondelivery.ErrNotFound
		}
		if err != nil {
			return err
		}
		if response.Status == "PENDING" && (commandStatus == devices.CommandFailed || commandStatus == devices.CommandExpired) {
			response.Status = "FAILED"
			response.ErrorCode = commandError
			if response.ErrorCode == "" {
				response.ErrorCode = "DEVICE_COMMAND_FAILED"
			}
		}
		if len(wrapped) > 0 {
			response.WrappedKey = base64.RawURLEncoding.EncodeToString(wrapped)
		}
		return nil
	})
	if err != nil {
		writeRuntimeSessionExportError(w, r, err)
		return
	}
	response.ID = r.PathValue("exportID")
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) downloadRuntimeSessionExport(w http.ResponseWriter, r *http.Request) {
	if !membership.ValidateUUID(r.PathValue("exportID")) {
		writeDeliveryError(w, r, executiondelivery.ErrNotFound)
		return
	}
	auth, selection, role, ok := h.conversationAuth(w, r)
	if !ok {
		return
	}
	actor := conversationActor(auth, role, selection.AuthorizationVersion)
	var storageKey string
	var size int64
	var digest string
	err := h.runDeliveryUserTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := (conversations.Service{}).Get(ctx, tx, selection.OrganizationID, r.PathValue("conversationID"), actor); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT storage_key, size_bytes, sha256
			FROM openfox.runtime_session_exports
			WHERE organization_id = openfox_private.current_organization_id()
			  AND id = $1::uuid AND conversation_id = $2::uuid AND requested_by_user_id = $3::uuid
			  AND status = 'READY' AND expires_at > clock_timestamp()`,
			r.PathValue("exportID"), r.PathValue("conversationID"), auth.principal.UserID).Scan(&storageKey, &size, &digest)
		if errors.Is(err, pgx.ErrNoRows) {
			return executiondelivery.ErrNotFound
		}
		return err
	})
	if err != nil {
		writeRuntimeSessionExportError(w, r, err)
		return
	}
	if h.storage == nil {
		writeDeliveryError(w, r, executiondelivery.ErrStorageUnavailable)
		return
	}
	content, err := h.storage.Open(r.Context(), storageKey)
	if err != nil {
		writeDeliveryError(w, r, executiondelivery.ErrStorageUnavailable)
		return
	}
	defer content.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "openfox-runtime-session-" + r.PathValue("exportID") + ".ofpkg.enc"}))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Content-SHA256", digest)
	_, _ = io.Copy(w, content)
}

func (h *Handler) uploadRuntimeSessionExport(w http.ResponseWriter, r *http.Request) {
	principal, selection, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	if h.storage == nil {
		writeDeliveryError(w, r, executiondelivery.ErrStorageUnavailable)
		return
	}
	exportID := r.PathValue("exportID")
	if !membership.ValidateUUID(exportID) {
		writeDeliveryError(w, r, executiondelivery.ErrNotFound)
		return
	}
	if r.ContentLength > artifacts.MaxArtifactBytes {
		writeDeliveryError(w, r, executiondelivery.ErrResourceLimit)
		return
	}
	wrappedKey, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(r.Header.Get("X-Runtime-Session-Wrapped-Key")))
	if err != nil || len(wrappedKey) < 256 || len(wrappedKey) > 512 {
		writeDeliveryError(w, r, executiondelivery.ErrInvalidRequest)
		return
	}
	var authorized bool
	err = h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM openfox.runtime_session_exports e
			JOIN openfox.device_commands c ON c.organization_id=e.organization_id AND c.id=e.command_id
			WHERE e.organization_id=openfox_private.current_organization_id() AND e.id=$1::uuid
			  AND e.device_id=$2::uuid AND e.status='PENDING' AND e.expires_at>clock_timestamp()
			  AND c.status='DELIVERED' AND c.kind='runtime_session_export'
			  AND c.params->>'export_id'=e.id::text
		)`, exportID, principal.DeviceID).Scan(&authorized)
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if !authorized {
		writeDeliveryError(w, r, executiondelivery.ErrNotFound)
		return
	}
	storageKey := "runtime-session-exports/" + selection.OrganizationID + "/" + exportID
	stored, err := h.storage.Put(r.Context(), storageKey, r.Body, artifacts.MaxArtifactBytes)
	if err != nil {
		if errors.Is(err, artifacts.ErrTooLarge) {
			writeDeliveryError(w, r, executiondelivery.ErrResourceLimit)
		} else {
			writeDeliveryError(w, r, executiondelivery.ErrStorageUnavailable)
		}
		return
	}
	_, digestErr := hex.DecodeString(stored.SHA256)
	if stored.Size < 1 || stored.Size > artifacts.MaxArtifactBytes || len(stored.SHA256) != 64 || digestErr != nil || strings.ToLower(stored.SHA256) != stored.SHA256 {
		_ = h.storage.Delete(context.Background(), storageKey)
		writeDeliveryError(w, r, executiondelivery.ErrInvalidRequest)
		return
	}
	updated := false
	err = h.runDeliveryDeviceTransaction(r, selection, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.runtime_session_exports e
			   SET status='READY', storage_key=$3, size_bytes=$4, sha256=$5, wrapped_key=$6
			 WHERE e.organization_id=openfox_private.current_organization_id() AND e.id=$1::uuid
			   AND e.device_id=$2::uuid AND e.status='PENDING' AND e.expires_at>clock_timestamp()
			   AND EXISTS (SELECT 1 FROM openfox.device_commands c
				 WHERE c.organization_id=e.organization_id AND c.id=e.command_id
				   AND c.device_id=e.device_id AND c.status='DELIVERED'
				   AND c.params->>'export_id'=e.id::text)`,
			exportID, principal.DeviceID, storageKey, stored.Size, stored.SHA256, wrappedKey)
		if err != nil {
			return err
		}
		updated = tag.RowsAffected() == 1
		return nil
	})
	if err != nil || !updated {
		_ = h.storage.Delete(context.Background(), storageKey)
		if err != nil {
			writeDeliveryError(w, r, err)
		} else {
			writeDeliveryError(w, r, executiondelivery.ErrNotFound)
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"ready": true})
}

func writeRuntimeSessionExportError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errRuntimeSessionNotFound):
		writeError(w, r, http.StatusNotFound, "SESSION_NOT_EXPORTABLE", "no completed Runtime session is available for export")
	case errors.Is(err, errRuntimeSessionUnavailable):
		writeError(w, r, http.StatusConflict, "EXPORT_DEVICE_UNAVAILABLE", "the computer is offline or cannot export this Runtime session")
	case errors.Is(err, errRuntimeSessionBusy), errors.Is(err, devices.ErrCommandConflict):
		writeError(w, r, http.StatusConflict, "EXPORT_BUSY", "another Runtime session export is already in progress on this computer")
	case errors.Is(err, executiondelivery.ErrNotFound), errors.Is(err, devices.ErrCommandNotFound):
		writeDeliveryError(w, r, executiondelivery.ErrNotFound)
	case errors.Is(err, conversations.ErrNotFound), errors.Is(err, conversations.ErrForbidden):
		writeConversationError(w, r, err)
	case errors.Is(err, identity.ErrSessionNotFound), errors.Is(err, deviceauth.ErrSessionNotFound):
		writeDeliveryError(w, r, err)
	default:
		writeDeliveryError(w, r, err)
	}
}
