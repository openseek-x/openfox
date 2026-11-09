package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/market"
	"openfox/internal/membership"
)

type marketAgentTarget struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Avatar    json.RawMessage `json:"avatar"`
	SpaceID   string          `json:"space_id"`
	SpaceName string          `json:"space_name"`
}

// Keep other people's private Agents, archived Agents and the built-in assistant out. An explicit viewer
// (even an organization admin) cannot import into a space's skill library.
func (h *Handler) listMarketAgentTargets(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, false)
	if !ok {
		return
	}
	q, after := strings.TrimSpace(r.URL.Query().Get("q")), r.URL.Query().Get("after")
	if utf8.RuneCountInString(q) > 100 || (after != "" && !membership.ValidateUUID(after)) {
		writeCollaborationError(w, r, market.ErrInvalidQuery)
		return
	}
	const pageSize = 100
	var items []marketAgentTarget
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, _ collaboration.Service) error {
		rows, err := tx.Query(ctx, `
 SELECT a.id::text, a.name, a.avatar, s.id::text, s.name
 FROM openfox.agents a
 JOIN openfox.spaces s ON (s.organization_id,s.id)=(a.organization_id,a.space_id)
 LEFT JOIN openfox.space_members sm ON sm.organization_id=s.organization_id AND sm.space_id=s.id AND sm.user_id=openfox_private.current_actor_id()
 WHERE a.organization_id=openfox_private.current_organization_id()
 AND a.builtin_kind IS NULL AND a.archived_at IS NULL AND s.deleted_at IS NULL AND s.status='ACTIVE'
 AND (a.owner_user_id=openfox_private.current_actor_id() OR (a.visibility='SHARED' AND $1 IN ('OWNER','ADMIN')))
 AND ((sm.status='ACTIVE' AND sm.role IN ('OWNER','EDITOR','MEMBER')) OR (sm.user_id IS NULL AND s.visibility='SHARED' AND $1<>'GUEST'))
 AND (NULLIF($2,'') IS NULL OR a.id>NULLIF($2,'')::uuid)
 AND (strpos(lower(a.name),lower($3))>0 OR strpos(lower(s.name),lower($3))>0)
 ORDER BY a.id LIMIT $4`, strings.ToUpper(string(call.selected.Actor.Role)), after, q, pageSize+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, pgx.RowToStructByPos[marketAgentTarget])
		return err
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	next := ""
	if len(items) > pageSize {
		items = items[:pageSize]
		next = items[pageSize-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}
