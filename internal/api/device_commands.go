package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"openfox/internal/devices"
	"openfox/internal/tenancy"
)

// 远程命令接口：用户侧仅电脑所有者；设备侧用设备会话令牌，命令只在本设备范围内可见。

type deviceCommandCreateRequest struct {
	Kind   devices.CommandKind `json:"kind"`
	Params json.RawMessage     `json:"params"`
}

type deviceCommandResultRequest struct {
	Status    string          `json:"status"`
	Result    json.RawMessage `json:"result"`
	ErrorCode string          `json:"error_code"`
}

type deviceCommandEnvelope struct {
	Command devices.Command `json:"command"`
}

func (h *DeviceManagementHandler) createCommand(w http.ResponseWriter, r *http.Request) {
	selection, _, ok := h.selectViewer(w, r, true)
	if !ok {
		return
	}
	if h.commands == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "device commands are not ready")
		return
	}
	var input deviceCommandCreateRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	command, created, err := h.commands.CreateCommand(r.Context(), selection, r.PathValue("deviceID"), input.Kind, input.Params)
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	// 提交后唤醒电脑；复用的 PENDING 命令也再推一次（电脑可能错过了上一次），重复唤醒无害。
	if h.hub != nil && command.Status == devices.CommandPending {
		h.hub.PublishCommand(command.DeviceID, selection.OrganizationID, command.ID, string(command.Kind))
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(w, status, deviceCommandEnvelope{Command: command})
}

func (h *DeviceManagementHandler) getCommand(w http.ResponseWriter, r *http.Request) {
	selection, _, ok := h.selectViewer(w, r, false)
	if !ok {
		return
	}
	if h.commands == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "device commands are not ready")
		return
	}
	command, err := h.commands.GetCommand(r.Context(), selection, r.PathValue("deviceID"), r.PathValue("commandID"))
	if err != nil {
		writeDeviceManagementError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deviceCommandEnvelope{Command: command})
}

func (h *DeviceManagementHandler) listDeviceCommands(w http.ResponseWriter, r *http.Request) {
	selection, ok := h.selectDevice(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	if status := strings.TrimSpace(query.Get("status")); status != "" && status != devices.CommandPending {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "status is invalid")
		return
	}
	limit := devices.DefaultCommandLimit
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > devices.MaxCommandLimit {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "limit is invalid")
			return
		}
		limit = parsed
	}
	commands, err := h.commands.ListPendingCommands(r.Context(), selection, limit)
	if err != nil {
		writeDeviceCommandError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": commands})
}

func (h *DeviceManagementHandler) claimDeviceCommand(w http.ResponseWriter, r *http.Request) {
	selection, ok := h.selectDevice(w, r)
	if !ok {
		return
	}
	// 请求体可省略；带了也只接受空对象。
	var ignored struct{}
	if err := decodeJSON(w, r, &ignored); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	command, err := h.commands.ClaimCommand(r.Context(), selection, r.PathValue("commandID"))
	if err != nil {
		writeDeviceCommandError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deviceCommandEnvelope{Command: command})
}

func (h *DeviceManagementHandler) reportDeviceCommandResult(w http.ResponseWriter, r *http.Request) {
	selection, ok := h.selectDevice(w, r)
	if !ok {
		return
	}
	var input deviceCommandResultRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is invalid")
		return
	}
	command, err := h.commands.CompleteCommand(r.Context(), selection, r.PathValue("commandID"), devices.CommandResultInput{
		Status: input.Status, Result: input.Result, ErrorCode: input.ErrorCode,
	})
	if err != nil {
		writeDeviceCommandError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deviceCommandEnvelope{Command: command})
}

// selectDevice 完成设备认证并选择组织（授权必须 ACTIVE），返回以设备为主体的租户选择。
func (h *DeviceManagementHandler) selectDevice(w http.ResponseWriter, r *http.Request) (tenancy.OrganizationSelection, bool) {
	principal, err := h.authenticateDevice(r)
	if err != nil {
		writeDeviceError(w, r, err)
		return tenancy.OrganizationSelection{}, false
	}
	if h.commands == nil {
		writeError(w, r, http.StatusServiceUnavailable, "NOT_READY", "device commands are not ready")
		return tenancy.OrganizationSelection{}, false
	}
	selection, err := h.deviceAuth.SelectOrganization(r.Context(), principal.DeviceID, r.PathValue("organizationID"))
	if err != nil {
		writeDeviceSelectionError(w, r, err)
		return tenancy.OrganizationSelection{}, false
	}
	return selection, true
}

func writeDeviceCommandError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, tenancy.ErrOrganizationSelection) {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "resource is not available")
		return
	}
	writeDeviceManagementError(w, r, err)
}
