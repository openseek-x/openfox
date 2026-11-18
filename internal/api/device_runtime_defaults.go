package api

import (
	"net/http"
	"openfox/internal/devices"
)

func (h *DeviceManagementHandler) runtimeDefaults(w http.ResponseWriter, r *http.Request) {
	selection, _, ok := h.selectViewer(w, r, r.Method == http.MethodPatch)
	if !ok {
		return
	}
	store, ok := h.store.(devices.RuntimeDefaultsStore)
	if !ok {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "runtime defaults are not ready")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	var result devices.RuntimeDefaults
	var err error
	if r.Method == http.MethodPatch {
		var input devices.RuntimeDefaultsPatch
		if decodeJSON(w, r, &input) != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
			return
		}
		result, err = store.PatchRuntimeDefaults(r.Context(), selection, r.PathValue("deviceID"), r.PathValue("runtime"), input)
	} else {
		result, err = store.GetRuntimeDefaults(r.Context(), selection, r.PathValue("deviceID"), r.PathValue("runtime"))
	}
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
