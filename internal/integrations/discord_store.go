package integrations

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/integrations/discord"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

func (s Service) discordClient() *discord.Client {
	if s.Discord != nil {
		return s.Discord
	}
	return discord.NewClient(nil)
}
func discordAAD(org, kind, id string) string { return "discord:" + kind + ":" + org + ":" + id }

// discordActor takes no target/Agent/Run locks. Call after control/epoch/identity
// for the final current member proof; never turn transient PG failures into ACL.
func discordActor(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, lock bool) error {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		if errors.Is(err, membership.ErrAuthorizationStale) {
			return ErrForbidden
		}
		return err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return ErrForbidden
	}
	if !lock {
		return nil
	}
	var role, status string
	var version uint64
	err := tx.QueryRow(ctx, `SELECT role,status,authorization_version FROM openfox.organization_members
 WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, actor.UserID).Scan(&role, &status, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	if status != "ACTIVE" || !strings.EqualFold(role, string(actor.Role)) || version != actor.AuthorizationVersion {
		return ErrForbidden
	}
	return nil
}

const discordControlSelect = `SELECT b.id::text,b.connection_id::text,b.kind,a.id::text,a.name,
 COALESCE(a.owner_user_id::text,''),a.space_id::text,COALESCE(sp.name,''),a.visibility,t.name,c.status,c.credentials_sealed,
 t.remote_bot_id::text,t.application_id::text,t.username,t.state,t.last_error,t.version,t.authorization_generation,t.token_version,
 t.message_content_enabled,COALESCE(st.state,'DISCONNECTED'),COALESCE(gs.gap_count,0),gs.gap_started_at,gs.gap_finished_at,COALESCE(gs.gap_reason,'')
 FROM openfox.discord_bot_controls t JOIN openfox.im_bots b ON b.organization_id=t.organization_id AND b.id=t.bot_id
 JOIN openfox.integration_connections c ON c.organization_id=b.organization_id AND c.id=b.connection_id
 JOIN openfox.agents a ON a.organization_id=b.organization_id AND a.id=b.agent_id
 LEFT JOIN openfox.spaces sp ON sp.organization_id=b.organization_id AND sp.id=b.space_id
 LEFT JOIN openfox.im_connection_states st ON st.organization_id=c.organization_id AND st.connection_id=c.id
 LEFT JOIN openfox.discord_gateway_sessions gs ON gs.organization_id=c.organization_id AND gs.connection_id=c.id
 WHERE t.organization_id=openfox_private.current_organization_id() AND t.bot_id=$1::uuid AND c.provider='discord'`

func loadDiscordControl(ctx context.Context, tx pgx.Tx, botID string, lock bool) (discordControl, error) {
	var r discordControl
	if !membership.ValidateUUID(botID) {
		return r, ErrNotFound
	}
	if lock {
		var conn string
		err := tx.QueryRow(ctx, `SELECT b.connection_id::text FROM openfox.im_bots b
   JOIN openfox.discord_bot_controls t ON t.organization_id=b.organization_id AND t.bot_id=b.id
   WHERE b.organization_id=openfox_private.current_organization_id() AND b.id=$1::uuid FOR UPDATE OF b`, botID).Scan(&conn)
		if errors.Is(err, pgx.ErrNoRows) {
			return r, ErrNotFound
		}
		if err != nil {
			return r, err
		}
		var id string
		if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, conn).Scan(&id); err != nil {
			return r, err
		}
		if err = tx.QueryRow(ctx, `SELECT bot_id::text FROM openfox.discord_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid FOR UPDATE`, botID).Scan(&id); err != nil {
			return r, err
		}
	}
	return scanDiscordControl(tx.QueryRow(ctx, discordControlSelect, botID))
}
func scanDiscordControl(row pgx.Row) (discordControl, error) {
	var r discordControl
	var spaceName string
	err := row.Scan(&r.bot.ID, &r.bot.ConnectionID, &r.bot.Kind, &r.bot.Agent.ID, &r.bot.Agent.Name,
		&r.owner, &r.spaceID, &spaceName, &r.visibility, &r.bot.Name, &r.parentStatus, &r.credentials, &r.bot.RemoteBotID, &r.bot.ApplicationID,
		&r.bot.Username, &r.bot.State, &r.bot.LastError, &r.bot.Version, &r.authGeneration, &r.tokenVersion, &r.bot.MessageContentEnabled,
		&r.bot.GatewayState, &r.bot.GapCount, &r.bot.GapStartedAt, &r.bot.GapFinishedAt, &r.bot.GapReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if r.bot.Kind == BotKindTaskAssistant {
		r.bot.Space = &FeishuBotRef{ID: r.spaceID, Name: spaceName}
	}
	r.bot.InstallationURL, _ = discord.InstallationURL(r.bot.ApplicationID)
	return r, nil
}
func discordCanManage(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, r discordControl) (bool, error) {
	if r.visibility == "PRIVATE" && r.owner != actor.UserID {
		return false, nil
	}
	canRead, err := (conversations.Service{}).CanReadSpace(ctx, tx, org, conversations.Actor(actor), r.spaceID)
	if err != nil || !canRead {
		return false, err
	}
	if r.bot.Kind == BotKindAgent {
		return isAdminRole(actor.Role) || r.owner == actor.UserID, nil
	}
	return agents.CanManageSpace(ctx, tx, org, r.spaceID, agentActor(actor))
}
func managedDiscord(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, lock bool) (discordControl, error) {
	if err := discordActor(ctx, tx, org, actor, false); err != nil {
		return discordControl{}, err
	}
	r, err := loadDiscordControl(ctx, tx, id, lock)
	if err != nil {
		return r, err
	}
	can, err := discordCanManage(ctx, tx, org, actor, r)
	if err != nil {
		return r, err
	}
	if !can {
		return r, ErrForbidden
	}
	// The caller takes the final member lock after its epoch/identity locks.
	r.bot.CanManage = true
	if r.bot.Kind == BotKindTaskAssistant {
		if err = tx.QueryRow(ctx, `SELECT extra_prompt FROM openfox.im_bots WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, r.bot.ID).Scan(&r.bot.ExtraPrompt); err != nil {
			return r, err
		}
		runtime, err := agents.LoadTaskAssistantRuntime(ctx, tx, org, r.bot.Agent.ID)
		if err != nil {
			return r, err
		}
		if runtime != nil {
			r.bot.Runtime = &FeishuBotRuntime{ComputerID: runtime.ComputerID, ComputerName: runtime.ComputerName, GrantID: runtime.GrantID, Runtime: runtime.Runtime, Model: runtime.Model}
		}
	}
	return r, nil
}
func (s Service) GetDiscordBot(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (DiscordBot, error) {
	r, err := managedDiscord(ctx, tx, org, actor, id, false)
	return r.bot, err
}
func (s Service) DiscordBots(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) (DiscordBotsView, error) {
	out := DiscordBotsView{Bots: []DiscordBot{}}
	if err := discordActor(ctx, tx, org, actor, false); err != nil {
		return out, err
	}
	query := strings.Replace(discordControlSelect, "AND t.bot_id=$1::uuid", "", 1) + " ORDER BY t.created_at,t.bot_id LIMIT 201"
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return out, err
	}
	records := []discordControl{}
	for rows.Next() {
		r, e := scanDiscordControl(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		records = append(records, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(records) > 200 {
		return out, ErrInvalidRequest
	}
	spacePermissions := map[string]bool{}
	for _, r := range records {
		can, known := spacePermissions[r.spaceID]
		if r.bot.Kind == BotKindAgent || !known {
			can, err = discordCanManage(ctx, tx, org, actor, r)
			if err != nil {
				return out, err
			}
			if r.bot.Kind == BotKindTaskAssistant {
				spacePermissions[r.spaceID] = can
			}
		}
		if can {
			r.bot.CanManage = true
			out.Bots = append(out.Bots, r.bot)
		}
	}

	return out, nil
}

// Final control settlement rechecks the same manager and current target after
// HTTP. A valid response cannot revive a grant revoked while the call ran.
func discordControlManager(ctx context.Context, tx pgx.Tx, org string, r discordControl, lock bool) (collaboration.Actor, error) {
	var id string
	var expected uint64
	err := tx.QueryRow(ctx, `SELECT managed_by::text,managed_version FROM openfox.discord_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, r.bot.ID).Scan(&id, &expected)
	if err != nil {
		return collaboration.Actor{}, err
	}
	member, err := (membership.Service{}).LoadActiveActor(ctx, tx, org, id)
	if errors.Is(err, membership.ErrAuthorizationStale) {
		return collaboration.Actor{}, ErrForbidden
	}
	if err != nil {
		return collaboration.Actor{}, err
	}
	actor := collaboration.Actor{UserID: member.UserID, Role: member.Role, AuthorizationVersion: member.AuthorizationVersion}
	if member.AuthorizationVersion != expected {
		return actor, ErrForbidden
	}
	var targetActive bool
	err = tx.QueryRow(ctx, `SELECT a.archived_at IS NULL AND a.status='ACTIVE' AND s.status='ACTIVE' FROM openfox.agents a
 JOIN openfox.spaces s ON s.organization_id=a.organization_id AND s.id=a.space_id
 WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1::uuid`, r.bot.Agent.ID).Scan(&targetActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return actor, ErrForbidden
	}
	if err != nil {
		return actor, err
	}
	if !targetActive {
		return actor, ErrForbidden
	}
	can, err := discordCanManage(ctx, tx, org, actor, r)
	if err != nil {
		return actor, err
	}
	if !can {
		return actor, ErrForbidden
	}
	if lock {
		return actor, discordFinalManager(ctx, tx, org, actor, r)
	}
	return actor, nil
}

// discordFinalManager fences existing organization and space membership rows.
// Call after epoch/identity locks, never acquire Agent/conversation/Run here.
func discordFinalManager(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, r discordControl) error {
	if err := discordActor(ctx, tx, org, actor, true); err != nil {
		return err
	}
	var member string
	err := tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.space_members WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND user_id=$2::uuid FOR SHARE`, r.spaceID, actor.UserID).Scan(&member)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	allowed, err := discordCanManage(ctx, tx, org, actor, r)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func loadDiscordTargetAccess(ctx context.Context, tx pgx.Tx, id string) (discordControl, error) {
	r := discordControl{bot: DiscordBot{Kind: BotKindAgent}}
	err := tx.QueryRow(ctx, `SELECT space_id::text,visibility,COALESCE(owner_user_id::text,'') FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&r.spaceID, &r.visibility, &r.owner)
	return r, err
}

// Personal targets deliberately expose only active bots whose target the
// current member can read. Managing a bot is not required to bind oneself.
func (s Service) DiscordPersonalTargets(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) (DiscordBotsView, error) {
	out := DiscordBotsView{Bots: []DiscordBot{}}
	if e := discordActor(ctx, tx, org, actor, false); e != nil {
		return out, e
	}
	query := strings.Replace(discordControlSelect, "AND t.bot_id=$1::uuid", "AND t.state='ACTIVE' AND c.status='ACTIVE'", 1) + " ORDER BY t.created_at,t.bot_id LIMIT 201"
	rows, e := tx.Query(ctx, query)
	if e != nil {
		return out, e
	}
	var records []discordControl
	for rows.Next() {
		r, e := scanDiscordControl(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		records = append(records, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(records) > 200 {
		return out, ErrInvalidRequest
	}
	for _, r := range records {
		e := discordCheckBindingTarget(ctx, tx, org, actor, r)
		if errors.Is(e, ErrForbidden) {
			continue
		}
		if e != nil {
			return out, e
		}
		out.Bots = append(out.Bots, r.bot)
	}
	return out, nil
}
