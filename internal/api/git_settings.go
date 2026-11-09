package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/policy"
)

func (h *Handler) gitSettings(w http.ResponseWriter, r *http.Request) {
	write := r.Method == http.MethodPut
	call, ok := h.beginTaskCall(w, r, write)
	if !ok {
		return
	}
	spaceID := r.PathValue("spaceID")
	var input collaboration.GitSettingsInput
	if write {
		var body struct {
			Version         uint64          `json:"version"`
			AuthorName      *string         `json:"author_name"`
			AuthorEmail     *string         `json:"author_email"`
			CoAuthorEnabled json.RawMessage `json:"co_author_enabled"`
			AuthorNameMode  json.RawMessage `json:"author_name_mode"`
			AuthorEmailMode json.RawMessage `json:"author_email_mode"`
		}
		if err := decodeJSON(w, r, &body); err != nil || body.AuthorName == nil || body.AuthorEmail == nil || len(body.CoAuthorEnabled) == 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "complete Git settings are required")
			return
		}
		if err := json.Unmarshal(body.CoAuthorEnabled, &input.CoAuthorEnabled); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "invalid co-author setting")
			return
		}
		input.Version, input.AuthorName, input.AuthorEmail = body.Version, *body.AuthorName, *body.AuthorEmail
		if len(body.AuthorNameMode) > 0 {
			if err := json.Unmarshal(body.AuthorNameMode, &input.AuthorNameMode); err != nil || input.AuthorNameMode == "" {
				writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "invalid Git name source")
				return
			}
		}
		if len(body.AuthorEmailMode) > 0 {
			if err := json.Unmarshal(body.AuthorEmailMode, &input.AuthorEmailMode); err != nil || input.AuthorEmailMode == "" {
				writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "invalid Git email source")
				return
			}
		}
	}
	var item collaboration.GitSettings
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, service collaboration.Service) error {
		manager := call.selected.Actor.Role == policy.OrganizationRoleOwner || call.selected.Actor.Role == policy.OrganizationRoleAdmin
		if spaceID != "" {
			access, err := service.LoadSpaceAccess(ctx, tx, call.organizationID, spaceID, call.selected.Actor.UserID)
			if err != nil {
				return err
			}
			action := policy.ActionReadSpace
			if write {
				action = policy.ActionManageSpace
			}
			decision := collaborationSpaceDecision(action, call.organizationID, call.selected, access, "")
			if !decision.Allowed() {
				return decisionError{decision: decision}
			}
			if write {
				if err := access.RequireWritable(); err != nil {
					return err
				}
			}
			manager = access.RequireWritable() == nil && collaborationSpaceDecision(policy.ActionManageSpace, call.organizationID, call.selected, access, "").Allowed()
		}
		var err error
		if write {
			item, err = service.PutGitSettings(ctx, tx, call.organizationID, spaceID, call.selected.Actor, input)
		} else {
			item, err = service.GetGitSettings(ctx, tx, call.organizationID, spaceID, call.selected.Actor)
		}
		if err != nil {
			return err
		}
		item.CanManage = manager
		if !write {
			return nil
		}
		objectID := call.organizationID
		if spaceID != "" {
			objectID = spaceID
		}
		return recordCollaborationAudit(ctx, tx, collaborationRequestID(w, r), call.organizationID, call.selected.Actor,
			"git_settings.updated", "git_settings", objectID, spaceID, call.selected.Actor.AuthorizationVersion,
			map[string]string{"version": fmt.Sprint(item.Version)})
	})
	if err != nil {
		writeCollaborationError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
