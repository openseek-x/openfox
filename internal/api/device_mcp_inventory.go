package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/mcpconnect"
)

type deviceMCPEntry struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Source    string `json:"source"`
	Enabled   bool   `json:"enabled"`
}
type deviceMCPRuntime struct {
	Status  string           `json:"status"`
	Servers []deviceMCPEntry `json:"servers"`
}

func (h *Handler) reportDeviceMCPInventory(w http.ResponseWriter, r *http.Request) {
	p, sel, ok := h.deliveryDeviceAuth(w, r)
	if !ok {
		return
	}
	var input struct {
		Runtimes map[string]deviceMCPRuntime `json:"runtimes"`
	}
	if decodeJSON(w, r, &input) != nil || input.Runtimes == nil || len(input.Runtimes) > 4 {
		writeAgentInventoryInvalid(w, r)
		return
	}
	for runtime, v := range input.Runtimes {
		if !devices.ValidRuntimeProvider(runtime) || len(v.Servers) > 32 {
			writeAgentInventoryInvalid(w, r)
			return
		}
		switch v.Status {
		case "ok", "missing", "invalid", "unavailable", "too_large":
		default:
			writeAgentInventoryInvalid(w, r)
			return
		}
		if v.Status != "ok" && len(v.Servers) > 0 {
			writeAgentInventoryInvalid(w, r)
			return
		}
		seen := map[string]bool{}
		for _, s := range v.Servers {
			key := strings.ToLower(s.Name)
			if !mcpconnect.ValidName(s.Name) || s.Source != "USER_CONFIG" || seen[key] || (s.Transport != "HTTP" && s.Transport != "STDIO" && s.Transport != "SSE") {
				writeAgentInventoryInvalid(w, r)
				return
			}
			seen[key] = true
		}
	}
	err := h.runDeliveryDeviceTransaction(r, sel, func(ctx context.Context, tx pgx.Tx) error {
		// Capture inventory for the physical owner's own tenant only. Recheck the
		// issuing key, live session, device generation and active grant at write time.
		tag, e := tx.Exec(ctx, `INSERT INTO openfox.device_mcp_inventory (organization_id,device_id,owner_user_id,runtimes)
  SELECT g.organization_id,d.id,d.owner_user_id,$6 FROM openfox.physical_devices d
  JOIN openfox.device_grants g ON g.device_id=d.id AND g.organization_id=$1 AND g.status='ACTIVE'
  JOIN openfox.organization_members om ON om.organization_id=g.organization_id AND om.user_id=d.owner_user_id AND om.status='ACTIVE'
  JOIN openfox.device_sessions ds ON ds.device_id=d.id AND ds.id=$3 AND ds.revoked_at IS NULL AND ds.expires_at>clock_timestamp()
  JOIN openfox.device_keys dk ON dk.device_id=d.id AND dk.key_id=ds.key_id AND dk.status='ACTIVE' AND dk.generation=ds.key_generation
  WHERE d.id=$2 AND d.status='ACTIVE' AND d.generation=$4 AND ds.device_generation=d.generation AND dk.key_id=$5
  ON CONFLICT (organization_id,device_id) DO UPDATE SET owner_user_id=EXCLUDED.owner_user_id,runtimes=EXCLUDED.runtimes,checked_at=clock_timestamp()`, sel.OrganizationID, p.DeviceID, p.SessionID, p.DeviceGeneration, p.KeyID, input.Runtimes)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return deviceauth.ErrDeviceUnauthenticated
		}
		return nil
	})
	if err != nil {
		writeDispatchError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func writeAgentInventoryInvalid(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, 400, "INVALID_REQUEST", "runtime MCP inventory is invalid")
}
