package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/market"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/skills"
	"openfox/internal/tenancy"
)

func (h *Handler) lockMarketAgent(ctx context.Context, tx pgx.Tx, call taskCall, r *http.Request, service collaboration.Service) (agents.Agent, error) {
	space, agent := r.PathValue("spaceID"), r.PathValue("agentID")
	access, err := call.docAccess(ctx, tx, service, space, agent)
	if err == nil {
		err = access.RequireSetWrite()
	}
	if err != nil {
		return agents.Agent{}, err
	}
	return agents.LockEditableAgent(ctx, tx, call.organizationID, space, agent, h.skillAgentActor(call, r))
}

// Files are copied before the second transaction. Both transactions recheck access;
// the Agent row lock serializes installs, reorders and removals for the same target.
func (h *Handler) installAgentMarketSkill(w http.ResponseWriter, r *http.Request) {
	call, ok := h.beginTaskCall(w, r, true)
	if !ok {
		return
	}
	agent, space, source := r.PathValue("agentID"), r.PathValue("spaceID"), r.PathValue("marketSkillID")
	if !membership.ValidateUUID(agent) {
		writeAgentError(w, r, agents.ErrNotFound)
		return
	}
	if !market.IDPattern.MatchString(source) {
		writeError(w, r, 400, "INVALID_REQUEST", "invalid market skill ID")
		return
	}
	actor := call.selected.Actor
	check := func(ctx context.Context, tx pgx.Tx, svc collaboration.Service) (bool, error) {
		a, err := h.lockMarketAgent(ctx, tx, call, r, svc)
		if err != nil {
			return false, err
		}
		if a.OwnerUserID != actor.UserID && actor.Role != policy.OrganizationRoleOwner && actor.Role != policy.OrganizationRoleAdmin {
			return false, agents.ErrForbidden
		}
		var already bool
		var count int
		err = tx.QueryRow(ctx, `SELECT COALESCE(bool_or(agent_owned AND market_skill_id=$2),false), count(*)
      FROM openfox.agent_skill_loadout WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1::uuid`, agent, source).Scan(&already, &count)
		if err == nil && !already && count >= skills.MaxAgentSkills {
			return false, skills.ErrAgentLimit
		}
		return already, err
	}
	var src market.Source
	var already bool
	err := call.run(h, r, func(ctx context.Context, tx pgx.Tx, svc collaboration.Service) error {
		var err error
		already, err = check(ctx, tx, svc)
		if already || err != nil {
			return err
		}
		src, err = market.LoadSource(ctx, tx, source)
		return err
	})
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	if already {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if h.delivery.Storage == nil {
		writeError(w, r, 503, "STORAGE_UNAVAILABLE", "file storage is not configured")
		return
	}
	files, err := market.ReadFiles(r.Context(), h.delivery.Storage, src)
	if err != nil {
		writeError(w, r, 503, "STORAGE_UNAVAILABLE", "the market files could not be read")
		return
	}
	id, err := collaboration.NewID()
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	entries, keys, err := skills.StoreFiles(r.Context(), h.delivery.Storage, call.organizationID, id, files)
	if err != nil {
		writeError(w, r, 503, "STORAGE_UNAVAILABLE", "the skill files could not be stored")
		return
	}
	pkg := market.SpacePackage(src, files)
	err = call.run(h, r, func(ctx context.Context, tx pgx.Tx, svc collaboration.Service) error {
		var err error
		already, err = check(ctx, tx, svc)
		if already || err != nil {
			return err
		}
		return skills.InstallForAgent(ctx, tx, space, agent, actor.UserID, id, source, src.Version, market.SourceLabel(src), pkg, entries)
	})
	if already || (err != nil && !errors.Is(err, tenancy.ErrCommitTransaction)) {
		skills.DeleteKeys(r.Context(), h.delivery.Storage, keys)
	}
	if err != nil {
		writeSkillError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
