package api

import (
	"net/http"

	"openfox/internal/devices"
)

// 阶段 12e：一台电脑加入多个组织（D-09）。

func (h *DeviceManagementHandler) registerDeviceOrganizationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{organizationID}/devices/{deviceID}/organizations", h.listDeviceOrganizations)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/devices/{deviceID}/join", h.joinOrganization)
	mux.HandleFunc("GET /v1/device/organizations", h.listDeviceActiveOrganizations)
	mux.HandleFunc("GET /v1/organizations/{organizationID}/removed-devices", h.listRemovedDevices)
	mux.HandleFunc("POST /v1/organizations/{organizationID}/devices/{deviceID}/allow-rejoin", h.allowDeviceRejoin)
}

func (h *DeviceManagementHandler) organizationStore(w http.ResponseWriter, r *http.Request) (devices.OrganizationStore, bool) {
	store, ok := h.store.(devices.OrganizationStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "NOT_IMPLEMENTED", "multi-organization devices are not available")
		return nil, false
	}
	return store, true
}

func (h *DeviceManagementHandler) listDeviceOrganizations(w http.ResponseWriter, r *http.Request) {
	selection, viewer, ok := h.selectViewer(w, r, false)
	if !ok {
		return
	}
	store, ok := h.organizationStore(w, r)
	if !ok {
		return
	}
	result, err := store.ListDeviceOrganizations(r.Context(), selection, viewer, r.PathValue("deviceID"))
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// joinOrganization 把电脑加入路径里的组织（目标组织）。调用者必须是电脑所有者、且在目标组织是非访客 ACTIVE 成员。
func (h *DeviceManagementHandler) joinOrganization(w http.ResponseWriter, r *http.Request) {
	selection, viewer, ok := h.selectViewer(w, r, true)
	if !ok {
		return
	}
	store, ok := h.organizationStore(w, r)
	if !ok {
		return
	}
	device, created, err := store.JoinOrganization(r.Context(), selection, viewer.Role, r.PathValue("deviceID"))
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"device": device, "created": created})
}

func (h *DeviceManagementHandler) listDeviceActiveOrganizations(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticateDevice(r)
	if err != nil {
		writeDeviceError(w, r, err)
		return
	}
	store, ok := h.organizationStore(w, r)
	if !ok {
		return
	}
	organizations, err := store.DeviceActiveOrganizations(r.Context(), principal.DeviceID)
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"organization_ids": organizations})
}

func (h *DeviceManagementHandler) rejoinStore(w http.ResponseWriter, r *http.Request) (devices.RejoinStore, bool) {
	store, ok := h.store.(devices.RejoinStore)
	if !ok {
		writeError(w, r, http.StatusNotImplemented, "NOT_IMPLEMENTED", "multi-organization devices are not available")
		return nil, false
	}
	return store, true
}

// listRemovedDevices 列出被本组织 Owner/Admin 移除、尚未重新允许的电脑（仅 Owner/Admin）。
func (h *DeviceManagementHandler) listRemovedDevices(w http.ResponseWriter, r *http.Request) {
	selection, viewer, ok := h.selectViewer(w, r, false)
	if !ok {
		return
	}
	store, ok := h.rejoinStore(w, r)
	if !ok {
		return
	}
	removed, err := store.ListRemovedDevices(r.Context(), selection, viewer)
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": removed})
}

// allowDeviceRejoin 清除「被管理员移除」标记（仅 Owner/Admin）；不会自动把电脑加回组织。
func (h *DeviceManagementHandler) allowDeviceRejoin(w http.ResponseWriter, r *http.Request) {
	selection, viewer, ok := h.selectViewer(w, r, true)
	if !ok {
		return
	}
	store, ok := h.rejoinStore(w, r)
	if !ok {
		return
	}
	if err := store.AllowRejoin(r.Context(), selection, viewer, r.PathValue("deviceID")); err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
