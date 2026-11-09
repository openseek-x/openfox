package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

type gitSettingsSnapshot struct {
	Author   *GitAuthor `json:"author"`
	CoAuthor *GitAuthor `json:"co_author"`
}

func inheritGitAuthor(nameMode, name, emailMode, email, agent, actor, owner, inheritedNameMode, inheritedName, inheritedEmailMode, inheritedEmail string) *GitAuthor {
	if nameMode == "INHERIT" {
		nameMode, name = inheritedNameMode, inheritedName
	}
	if emailMode == "INHERIT" {
		emailMode, email = inheritedEmailMode, inheritedEmail
	}
	return resolveGitAuthor(nameMode, name, emailMode, email, agent, actor, owner)
}

func snapshotRunGitSettings(ctx context.Context, tx pgx.Tx, runID string) error {
	var nameMode, name, emailMode, email, agent, actor, owner, inheritedNameMode, inheritedName, inheritedEmailMode, inheritedEmail string
	var coAuthor bool
	err := tx.QueryRow(ctx, `SELECT c.git_author_name_mode,c.git_author_name,c.git_author_email_mode,c.git_author_email,
		a.name,COALESCE(actor.email,''),COALESCE(owner.email,actor.email,''),
		COALESCE(NULLIF(s.author_name_mode,'INHERIT'),o.author_name_mode,'USER_AND_AGENT'),
		CASE WHEN s.author_name_mode <> 'INHERIT' THEN s.author_name ELSE COALESCE(o.author_name,'') END,
		COALESCE(NULLIF(s.author_email_mode,'INHERIT'),o.author_email_mode,'USER'),
		CASE WHEN s.author_email_mode <> 'INHERIT' THEN s.author_email ELSE COALESCE(o.author_email,'') END,
		COALESCE(c.git_co_author_enabled,s.co_author_enabled,o.co_author_enabled,false)
		FROM openfox.runs r
		JOIN openfox.agent_config_versions c ON c.organization_id=r.organization_id AND c.agent_id=r.agent_id AND c.config_version=r.config_version
		JOIN openfox.agents a ON a.organization_id=r.organization_id AND a.id=r.agent_id
		LEFT JOIN openfox.users actor ON actor.id=r.actor_id
		LEFT JOIN openfox.users owner ON owner.id=a.owner_user_id
		LEFT JOIN openfox.organization_git_settings o ON o.organization_id=r.organization_id
		LEFT JOIN openfox.space_git_settings s ON s.organization_id=r.organization_id AND s.space_id=r.space_id
		WHERE r.organization_id=openfox_private.current_organization_id() AND r.id=$1::uuid`, runID).
		Scan(&nameMode, &name, &emailMode, &email, &agent, &actor, &owner, &inheritedNameMode, &inheritedName, &inheritedEmailMode, &inheritedEmail, &coAuthor)
	if err != nil {
		return err
	}
	snapshot := gitSettingsSnapshot{Author: inheritGitAuthor(nameMode, name, emailMode, email, agent, actor, owner, inheritedNameMode, inheritedName, inheritedEmailMode, inheritedEmail)}
	if coAuthor && agent != "" && owner != "" && !strings.ContainsAny(agent+owner, "<>") && strings.IndexFunc(agent+owner, unicode.IsControl) < 0 {
		snapshot.CoAuthor = &GitAuthor{Name: agent, Email: owner}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.runs SET git_settings_snapshot=$2::jsonb
		WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, runID, encoded)
	return err
}

func applyRunGitSnapshot(ctx context.Context, tx pgx.Tx, runID string, result *RunContext) error {
	var encoded []byte
	if err := tx.QueryRow(ctx, `SELECT git_settings_snapshot FROM openfox.runs
		WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, runID).Scan(&encoded); err != nil {
		return err
	}
	if len(encoded) == 0 {
		return nil
	}
	var snapshot gitSettingsSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		return err
	}
	result.GitAuthor = snapshot.Author
	if snapshot.CoAuthor != nil {
		result.Instructions += "\n\n## Git commit attribution\nWhen creating a Git commit, include the following trailer exactly once, without replacing existing contributors:\nCo-authored-by: " + snapshot.CoAuthor.Name + " <" + snapshot.CoAuthor.Email + ">"
	}
	return nil
}
