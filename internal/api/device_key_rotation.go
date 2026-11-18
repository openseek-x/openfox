package api

import (
	"errors"
	"net/http"
	"time"

	"openfox/internal/deviceauth"
	"openfox/internal/devices"
)

// 阶段 12e：设备侧两步式密钥轮换（D-01）。两条路由都只接受设备会话令牌。

func (h *DeviceManagementHandler) registerKeyRotationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/device/keys/rotation", h.registerNextDeviceKey)
	mux.HandleFunc("POST /v1/device/keys/rotation/confirm", h.confirmDeviceKeyRotation)
}

type nextDeviceKeyRequest struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
	// CurrentSignature 是当前（旧）私钥对同一内容的签名：只有会话令牌、没有私钥的调用方不能登记新钥。
	CurrentSignature string `json:"current_signature"`
}

type confirmDeviceKeyRequest struct {
	PreviousKeyID string `json:"previous_key_id"`
}

func (h *DeviceManagementHandler) keyRotationStore(w http.ResponseWriter, r *http.Request) (devices.KeyRotationStore, bool) {
	store, ok := h.store.(devices.KeyRotationStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "NOT_IMPLEMENTED", "device key rotation is not available")
		return nil, false
	}
	return store, true
}

func (h *DeviceManagementHandler) registerNextDeviceKey(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticateDevice(r)
	if err != nil {
		writeDeviceError(w, r, err)
		return
	}
	store, ok := h.keyRotationStore(w, r)
	if !ok {
		return
	}
	var input nextDeviceKeyRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	publicKey, err := deviceauth.DecodeBase64URL(input.PublicKey, 32)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	signature, err := deviceauth.DecodeBase64URL(input.Signature, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	currentSignature, err := deviceauth.DecodeBase64URL(input.CurrentSignature, 64)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	result, err := store.RegisterNextKey(r.Context(), principal, devices.NextKeyInput{
		KeyID: input.KeyID, PublicKey: publicKey, Signature: signature, CurrentSignature: currentSignature,
	}, time.Now().UTC())
	if err != nil {
		writeKeyRotationError(w, r, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, result)
}

func (h *DeviceManagementHandler) confirmDeviceKeyRotation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticateDevice(r)
	if err != nil {
		writeDeviceError(w, r, err)
		return
	}
	store, ok := h.keyRotationStore(w, r)
	if !ok {
		return
	}
	var input confirmDeviceKeyRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	result, err := store.ConfirmKeyRotation(r.Context(), principal, input.PreviousKeyID, time.Now().UTC())
	if err != nil {
		writeKeyRotationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// writeKeyRotationError：签名不符按 400（不是 401，避免 daemon 误以为会话失效而反复续期）。
func writeKeyRotationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, deviceauth.ErrInvalidSignature):
		writeError(w, r, http.StatusBadRequest, "INVALID_SIGNATURE", "the new key proof is invalid")
	case errors.Is(err, devices.ErrKeyConflict):
		writeError(w, r, http.StatusConflict, "KEY_CONFLICT", "the new key id is already used")
	case errors.Is(err, devices.ErrKeyRotationInvalid):
		writeError(w, r, http.StatusConflict, "KEY_ROTATION_INVALID", "the rotation does not match the current session")
	default:
		writeDeviceManagementError(w, r, err)
	}
}
