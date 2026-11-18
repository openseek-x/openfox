package api

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
)

type inboxPreference struct {
	Key   string `json:"key"`
	Group string `json:"group"`
	Level string `json:"level"`
}

// Mutations touch only the selected key; a group mute never overwrites its children.
func (h *Handler) myInboxPreferences(w http.ResponseWriter, r *http.Request) {
	auth, ok := h.requireSession(w, r, r.Method != http.MethodGet)
	if !ok {
		return
	}
	var change struct {
		Key   string `json:"key"`
		Level string `json:"level"`
	}
	if r.Method == http.MethodPatch && (decodeJSON(w, r, &change) != nil || len(change.Key) > 80 || (change.Level != "off" && change.Level != "important" && change.Level != "normal")) {
		writeError(w, r, 400, "INVALID_REQUEST", "invalid inbox preference")
		return
	}
	uid := auth.principal.UserID
	list := []inboxPreference{}
	err := h.pool.WithTransaction(r.Context(), func(ctx context.Context, tx pgx.Tx) error {
		switch r.Method {
		case http.MethodPatch:
			tag, err := tx.Exec(ctx, `INSERT INTO openfox.user_inbox_preferences(user_id,key,level)
                SELECT $1,key,$3 FROM openfox.inbox_policy_keys WHERE key=$2 AND (group_key IS NOT NULL OR $3<>'important')
                ON CONFLICT (user_id,key) DO UPDATE SET level=EXCLUDED.level`, uid, change.Key, change.Level)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return pgx.ErrNoRows
			}
		case http.MethodDelete:
			if _, err := tx.Exec(ctx, `DELETE FROM openfox.user_inbox_preferences WHERE user_id=$1`, uid); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT k.key,coalesce(k.group_key,''),coalesce(p.level,k.default_level) FROM openfox.inbox_policy_keys k
            LEFT JOIN openfox.user_inbox_preferences p ON p.user_id=$1 AND p.key=k.key ORDER BY k.position`, uid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v inboxPreference
			if err := rows.Scan(&v.Key, &v.Group, &v.Level); err != nil {
				return err
			}
			list = append(list, v)
		}
		return rows.Err()
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, 400, "INVALID_REQUEST", "unknown event or invalid group preference")
		return
	}
	if err != nil {
		writeError(w, r, 500, "INTERNAL_ERROR", "request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, list)
}
