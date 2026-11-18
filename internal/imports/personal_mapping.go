package imports

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/membership"
)

// VerifiedBinding is an automatic suggestion, separate from the legacy member
// suggestion so older clients cannot silently turn it into a permanent mapping.
type VerifiedBinding struct {
	ID      string `json:"id"`
	UserID  string `json:"user_id"`
	Version uint64 `json:"version"`
}

// loadVerifiedBindings uses explicit provider identity namespaces; a display
// source_label is never sufficient. Parent then child lock order matches
// parent disconnect.
func loadVerifiedBindings(ctx context.Context, tx pgx.Tx, origin ProjectOrigin, provider string, ids []string, lock bool) (map[string]VerifiedBinding, error) {
	out := map[string]VerifiedBinding{}
	if provider == "azure_devops" && membership.ValidateUUID(origin.Connection) && origin.Project != "" && origin.Instance != "" && len(ids) > 0 {
		return loadAzureVerifiedBindings(ctx, tx, origin, ids, lock)
	}
	if (provider != "teambition" && provider != "tapd" && provider != "yunxiao" && provider != "asana" && provider != "shortcut" && provider != "zoho_bugtracker") || !membership.ValidateUUID(origin.Connection) || origin.Project == "" || origin.Instance == "" || len(ids) == 0 {
		return out, nil
	}
	if provider == "tapd" {
		return loadTAPDVerifiedBindings(ctx, tx, origin, ids, lock)
	}
	parentLock := ""
	if lock {
		parentLock = " FOR SHARE"
	}
	var company string
	var parentVersion uint64
	err := tx.QueryRow(ctx, `SELECT external_id,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider=$2 AND scope='ORGANIZATION' AND status='ACTIVE'`+parentLock, origin.Connection, provider).Scan(&company, &parentVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	childLock := ""
	if lock {
		childLock = " FOR SHARE OF c,m,u"
	}
	rows, err := tx.Query(ctx, `SELECT c.external_id,c.id::text,c.owner_user_id::text,c.version
 FROM openfox.integration_connections c
 JOIN openfox.organization_members m ON m.organization_id=c.organization_id AND m.user_id=c.owner_user_id
 JOIN openfox.users u ON u.id=m.user_id
 WHERE c.organization_id=openfox_private.current_organization_id() AND c.provider=$1 AND c.scope='PERSONAL'
 AND c.parent_connection_id=$2::uuid AND c.status='ACTIVE' AND c.account_kind='identity'
 AND c.settings->>'auth_method'=($1 || '_identity') AND c.settings->>'company_id'=$3
 AND c.settings->>'parent_version'=$4 AND c.settings->>'verified_email'=u.normalized_email
 AND m.status='ACTIVE' AND m.role<>'GUEST' AND c.external_id=ANY($5::text[])
		ORDER BY c.id`+childLock, provider, origin.Connection, company, strconv.FormatUint(parentVersion, 10), ids)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var source string
		var b VerifiedBinding
		if err = rows.Scan(&source, &b.ID, &b.UserID, &b.Version); err != nil {
			return out, err
		}
		out[source] = b
	}
	return out, rows.Err()
}

func jobPersonalBindings(ctx context.Context, tx pgx.Tx, jobID, source string, ids []string, lock bool) (map[string]VerifiedBinding, error) {
	if source != "teambition_api" && source != "tapd_api" && source != "yunxiao_api" && source != SourceAsana && source != SourceAzure && source != "shortcut_api" && source != SourceZoho {
		return map[string]VerifiedBinding{}, nil
	}
	var origin ProjectOrigin
	err := tx.QueryRow(ctx, `SELECT coalesce(preview->'project_origin'->>'connection',''),coalesce(preview->'project_origin'->>'project',''),coalesce(preview->'project_origin'->>'instance','') FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, jobID).Scan(&origin.Connection, &origin.Project, &origin.Instance)
	if err != nil {
		return nil, err
	}
	return loadVerifiedBindings(ctx, tx, origin, sourceSystem(source), ids, lock)
}

func applyVerifiedSuggestions(ctx context.Context, tx pgx.Tx, jobID, source string, a *Analysis) error {
	ids := make([]string, 0, len(a.Users))
	for _, u := range a.Users {
		ids = append(ids, u.SourceID)
	}
	bindings, err := jobPersonalBindings(ctx, tx, jobID, source, ids, false)
	if err != nil {
		return err
	}
	for i := range a.Users {
		if binding, ok := bindings[a.Users[i].SourceID]; ok {
			a.Users[i].VerifiedBinding = &binding
			a.Users[i].SuggestedUserID = nil
			a.Users[i].SuggestReason = ""
		}
	}
	return nil
}

func validateVerifiedMappings(ctx context.Context, tx pgx.Tx, jobID, source string, cfg Config) error {
	ids := []string{}
	for _, u := range cfg.Users {
		if u.VerifiedBindingID != "" {
			ids = append(ids, u.SourceID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	bindings, err := jobPersonalBindings(ctx, tx, jobID, source, ids, false)
	if err != nil {
		return err
	}
	for _, u := range cfg.Users {
		if u.VerifiedBindingID != "" {
			b, ok := bindings[u.SourceID]
			if !ok || b.ID != u.VerifiedBindingID || b.UserID != u.UserID {
				return invalidConfig("verified identity no longer matches this source")
			}
		}
	}
	return nil
}

// Refresh each actual write batch (including continuous-sync new items). Only
// explicit binding references or previously unseen sources use automatic
// identity. Manual member/skip/external choices retain their original meaning.
func (w *Worker) refreshVerifiedUsers(ctx context.Context, tx pgx.Tx, b *batchCtx, infos []rowInfo) error {
	if b.job.Source != "teambition_api" && b.job.Source != "tapd_api" && b.job.Source != "yunxiao_api" && b.job.Source != SourceAsana && b.job.Source != SourceAzure && b.job.Source != "shortcut_api" && b.job.Source != SourceZoho {
		return nil
	}
	modes := map[string]UserMapping{}
	for _, m := range b.job.Cfg.Users {
		modes[m.SourceID] = m
	}
	users := collectUsers(infos, b.settingsLocation())
	ids := []string{}
	for _, u := range users {
		m, exists := modes[u.SourceID]
		if !exists || m.VerifiedBindingID != "" {
			ids = append(ids, u.SourceID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	provider := sourceSystem(b.job.Source)
	bindings, err := loadVerifiedBindings(ctx, tx, b.job.Preview.Origin, provider, ids, true)
	if err != nil {
		return err
	}
	// Avoid mutating preview.resolved through its shared map: it is an old
	// snapshot, not authorization. This runtime map is rebuilt on the next batch.
	resolved := make(map[string]ResolvedUser, len(b.resolved.Users))
	for k, v := range b.resolved.Users {
		resolved[k] = v
	}
	b.resolved.Users = resolved
	// Preserve the external identity even when binding disappears after validation.
	if _, err = tx.Exec(ctx, `INSERT INTO openfox.external_accounts(organization_id,provider,external_user_id,display_name,created_by)
 SELECT openfox_private.current_organization_id(),$3,source,source,$2::uuid FROM unnest($1::text[]) source
 ON CONFLICT(organization_id,provider,lower(external_user_id)) DO NOTHING`, ids, b.actor.UserID, provider); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT external_user_id,id::text,coalesce(linked_user_id::text,'') FROM openfox.external_accounts WHERE organization_id=openfox_private.current_organization_id() AND provider=$2 AND lower(external_user_id)=ANY($1::text[])`, lowerStrings(ids), provider)
	if err != nil {
		return err
	}
	type account struct{ id, linked string }
	accounts := map[string]account{}
	for rows.Next() {
		var id string
		var a account
		if err = rows.Scan(&id, &a.id, &a.linked); err != nil {
			rows.Close()
			return err
		}
		accounts[strings.ToLower(id)] = a
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, u := range users {
		m, explicit := modes[u.SourceID]
		if explicit && m.VerifiedBindingID == "" {
			continue
		}
		a := accounts[strings.ToLower(u.SourceID)]
		current := ResolvedUser{ExternalAccountID: a.id, Name: u.Name}
		if a.linked != "" {
			current = ResolvedUser{UserID: a.linked, Name: u.Name}
		} else if binding, ok := bindings[u.SourceID]; ok && (!explicit || binding.ID == m.VerifiedBindingID && binding.UserID == m.UserID) {
			current = ResolvedUser{UserID: binding.UserID, Name: u.Name}
		} else if explicit {
			b.warns.add("verified_identity_unavailable", "已验证身份已失效，后续归属保留为来源外部账号")
		}
		b.resolved.Users[u.SourceID] = current
	}
	return nil
}

func lowerStrings(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}
