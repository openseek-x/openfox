package agents

import (
	"context"
	"encoding/json"
	"errors"
	"openfox/internal/devices"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/mcpconnect"
	"openfox/internal/tenancy"
)

type MCPInventoryServer struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Source    string `json:"source"`
	Enabled   bool   `json:"enabled"`
	Selected  bool   `json:"selected"`
}
type MCPInventory struct {
	CheckedAt  *time.Time           `json:"checked_at,omitempty"`
	Online     bool                 `json:"online"`
	LastSeenAt *time.Time           `json:"last_seen_at,omitempty"`
	DeviceID   string               `json:"device_id"`
	Runtime    string               `json:"runtime"`
	Status     string               `json:"status"`
	Servers    []MCPInventoryServer `json:"servers"`
}
type MCPInheritInput struct {
	Enabled  bool   `json:"enabled"`
	DeviceID string `json:"device_id"`
	Runtime  string `json:"runtime"`
}

func (s PostgresStore) MCPInventory(ctx context.Context, sel tenancy.OrganizationSelection, actor Actor, space, agent, name string, change *MCPInheritInput) (MCPInventory, error) {
	r := MCPInventory{Status: "unavailable", Servers: []MCPInventoryServer{}}
	err := s.mcpTx(ctx, sel, actor, space, agent, change != nil, func(tx pgx.Tx, _ Agent, _ Actor) error {
		d, e := ownMCPComputer(ctx, tx, sel.OrganizationID, agent, actor)
		if e != nil {
			return e
		}
		if d.ID == "" {
			return ErrForbidden
		}
		r.DeviceID, r.Runtime = d.ID, d.Runtime
		if change != nil && (change.DeviceID != d.ID || change.Runtime != d.Runtime || !mcpconnect.ValidName(name)) {
			return ErrInvalidRequest
		}
		var payload []byte
		e = tx.QueryRow(ctx, `SELECT i.runtimes->$2::text,d.last_seen_at,COALESCE(d.last_seen_at>=clock_timestamp()-make_interval(secs=>$3),false),i.checked_at
   FROM openfox.physical_devices d LEFT JOIN openfox.device_mcp_inventory i ON i.organization_id=$4 AND i.device_id=d.id AND i.owner_user_id=$5 AND d.owner_user_id=i.owner_user_id
   WHERE d.id=$1`, d.ID, mcpRuntime(d.Runtime), devices.OnlineAfter.Seconds(), sel.OrganizationID, actor.UserID).Scan(&payload, &r.LastSeenAt, &r.Online, &r.CheckedAt)
		if e != nil {
			return e
		}
		var inventory struct {
			Status  string
			Servers []MCPInventoryServer
		}
		if len(payload) > 0 && len(payload) <= 65536 && json.Unmarshal(payload, &inventory) == nil {
			switch inventory.Status {
			case "ok", "missing", "unavailable", "invalid", "too_large":
				r.Status = inventory.Status
			}
		}
		seen := map[string]bool{}
		if r.Status == "ok" {
			if len(inventory.Servers) > 64 {
				return ErrInvalidConfig
			}
			for _, item := range inventory.Servers {
				if !mcpconnect.ValidName(item.Name) || seen[item.Name] || item.Source != "USER_CONFIG" || (item.Transport != "STDIO" && item.Transport != "HTTP" && item.Transport != "SSE") {
					continue
				}
				seen[item.Name] = true
				item.Selected = false
				r.Servers = append(r.Servers, item)
			}
		}
		rows, e := tx.Query(ctx, `SELECT name,id::text FROM openfox.agent_mcp_servers WHERE organization_id=$1 AND agent_id=$2 AND device_id=$3 AND owner_user_id=$4 AND runtime=$5 AND config->>'transport'='inherited'`, sel.OrganizationID, agent, d.ID, actor.UserID, d.Runtime)
		if e != nil {
			return e
		}
		selected := map[string]string{}
		for rows.Next() {
			var n, id string
			if e = rows.Scan(&n, &id); e != nil {
				rows.Close()
				return e
			}
			selected[n] = id
		}
		rows.Close()
		if e = rows.Err(); e != nil {
			return e
		}
		if change != nil {
			exists := selected[name] != ""
			if change.Enabled && !exists {
				available := false
				for _, item := range r.Servers {
					if item.Name == name && item.Enabled {
						available = true
					}
				}
				if !available {
					return ErrInvalidRequest
				}
				var id string
				if e = tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&id); e != nil {
					return e
				}
				v, e := s.mcpSave(ctx, tx, sel.OrganizationID, agent, id, actor, MCPServer{}, mcpconnect.Config{Name: name, Scope: mcpconnect.DeviceOwner, Transport: "inherited"}, mcpconnect.EmptyManifest(), nil)
				if e != nil {
					return e
				}
				selected[name] = v.ID
			}
			if !change.Enabled && exists {
				tag, e := tx.Exec(ctx, `DELETE FROM openfox.agent_mcp_servers WHERE organization_id=$1 AND agent_id=$2 AND id=$3`, sel.OrganizationID, agent, selected[name])
				if e != nil {
					return e
				}
				if tag.RowsAffected() != 1 {
					return ErrVersionConflict
				}
				delete(selected, name)
			}
			if change.Enabled != exists {
				if e := recordAudit(ctx, tx, sel, actor, "agent.mcp_inheritance_updated", agent, space, 1); e != nil {
					return e
				}
			}
		}
		for i := range r.Servers {
			r.Servers[i].Selected = selected[r.Servers[i].Name] != ""
		}
		// A removed/disabled server must remain detachable from this Agent.
		for n := range selected {
			if !seen[n] {
				r.Servers = append(r.Servers, MCPInventoryServer{Name: n, Source: "USER_CONFIG", Selected: true})
			}
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return r, err
}
