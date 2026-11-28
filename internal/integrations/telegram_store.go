package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/telegram"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

func (s Service) telegramClient() *telegram.Client {
	if s.Telegram != nil {
		return s.Telegram
	}
	return telegram.NewClient(nil)
}
func telegramAAD(org, kind, id string) string { return "telegram:" + kind + ":" + org + ":" + id }

// telegramActor takes no target/Agent/Run locks. Call after control/epoch/identity
// for the final current member proof; never turn transient PG failures into ACL.
func telegramActor(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, lock bool) error {
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

const telegramControlSelect = `SELECT b.id::text,b.connection_id::text,b.kind,a.id::text,a.name,
 COALESCE(a.owner_user_id::text,''),a.space_id::text,COALESCE(sp.name,''),c.display_name,c.status,c.credentials_sealed,
 t.remote_bot_id,t.username,t.state,t.control_status,t.last_error,t.version,
 t.authorization_generation,t.webhook_generation,t.token_version,t.webhook_sealed,t.configured_at,t.first_update_at,t.unsupported_topic_count
 FROM openfox.telegram_bot_controls t JOIN openfox.im_bots b ON b.organization_id=t.organization_id AND b.id=t.bot_id
 JOIN openfox.integration_connections c ON c.organization_id=b.organization_id AND c.id=b.connection_id
 JOIN openfox.agents a ON a.organization_id=b.organization_id AND a.id=b.agent_id
 LEFT JOIN openfox.spaces sp ON sp.organization_id=b.organization_id AND sp.id=b.space_id
 WHERE t.organization_id=openfox_private.current_organization_id() AND t.bot_id=$1::uuid AND c.provider='telegram'`

func loadTelegramControl(ctx context.Context, tx pgx.Tx, botID string, lock bool) (telegramControl, error) {
	var r telegramControl
	if !membership.ValidateUUID(botID) {
		return r, ErrNotFound
	}
	if lock {
		var conn string
		err := tx.QueryRow(ctx, `SELECT b.connection_id::text FROM openfox.im_bots b
   JOIN openfox.telegram_bot_controls t ON t.organization_id=b.organization_id AND t.bot_id=b.id
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
		if err = tx.QueryRow(ctx, `SELECT bot_id::text FROM openfox.telegram_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid FOR UPDATE`, botID).Scan(&id); err != nil {
			return r, err
		}
	}
	return scanTelegramControl(tx.QueryRow(ctx, telegramControlSelect, botID))
}
func scanTelegramControl(row pgx.Row) (telegramControl, error) {
	var r telegramControl
	var spaceName string
	err := row.Scan(&r.bot.ID, &r.bot.ConnectionID, &r.bot.Kind, &r.bot.Agent.ID, &r.bot.Agent.Name,
		&r.owner, &r.spaceID, &spaceName, &r.bot.Name, &r.parentStatus, &r.credentials, &r.bot.RemoteBotID, &r.bot.Username, &r.bot.State,
		&r.bot.ControlStatus, &r.bot.LastError, &r.bot.Version, &r.authGeneration, &r.webhookGeneration, &r.tokenVersion, &r.webhookSealed, &r.bot.ConfiguredAt, &r.bot.FirstUpdateAt, &r.bot.UnsupportedTopicCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if r.bot.Kind == BotKindTaskAssistant {
		r.bot.Space = &FeishuBotRef{ID: r.spaceID, Name: spaceName}
	}
	return r, nil
}
func telegramCanManage(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, r telegramControl) (bool, error) {
	if r.bot.Kind == BotKindAgent {
		return isAdminRole(actor.Role) || r.owner == actor.UserID, nil
	}
	return agents.CanManageSpace(ctx, tx, org, r.spaceID, agentActor(actor))
}
func managedTelegram(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, lock bool) (telegramControl, error) {
	if err := telegramActor(ctx, tx, org, actor, false); err != nil {
		return telegramControl{}, err
	}
	r, err := loadTelegramControl(ctx, tx, id, lock)
	if err != nil {
		return r, err
	}
	can, err := telegramCanManage(ctx, tx, org, actor, r)
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
			r.bot.Runtime = &IMBotRuntime{ComputerID: runtime.ComputerID, ComputerName: runtime.ComputerName, GrantID: runtime.GrantID, Runtime: runtime.Runtime, Model: runtime.Model}
		}
	}
	return r, nil
}
func (s Service) telegramCoordinates(org string, r telegramControl) (telegramWebhookCoordinates, error) {
	var h telegramWebhookCoordinates
	p, err := s.open(r.webhookSealed, telegramAAD(org, "hook", r.bot.ID))
	if err != nil {
		return h, err
	}
	if json.Unmarshal(p, &h) != nil || h.URL == "" || h.Route == "" || h.Secret == "" {
		return h, ErrSecretsUnavailable
	}
	return h, nil
}
func (s Service) newTelegramCoordinates() (telegramWebhookCoordinates, error) {
	var h telegramWebhookCoordinates
	u, err := url.Parse(s.PublicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return h, ErrNotConfigured
	}
	h.Route, err = randomBase64URL(32)
	if err != nil {
		return h, err
	}
	h.Secret, err = randomBase64URL(32)
	if err != nil {
		return h, err
	}
	h.URL = strings.TrimRight(u.String(), "/") + "/api/v1/hooks/telegram/" + h.Route
	return h, nil
}
func (s Service) GetTelegramBot(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (TelegramBot, error) {
	r, err := managedTelegram(ctx, tx, org, actor, id, false)
	return r.bot, err
}
func (s Service) TelegramBots(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) (TelegramBotsView, error) {
	out := TelegramBotsView{Bots: []TelegramBot{}}
	if err := telegramActor(ctx, tx, org, actor, false); err != nil {
		return out, err
	}
	query := strings.Replace(telegramControlSelect, "AND t.bot_id=$1::uuid", "", 1) + " ORDER BY t.created_at,t.bot_id LIMIT 201"
	rows, err := tx.Query(ctx, query)
	if err != nil {
		return out, err
	}
	records := []telegramControl{}
	for rows.Next() {
		r, e := scanTelegramControl(rows)
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
			can, err = telegramCanManage(ctx, tx, org, actor, r)
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
func telegramControlManager(ctx context.Context, tx pgx.Tx, org string, r telegramControl, lock bool) (collaboration.Actor, error) {
	var id string
	var expected uint64
	err := tx.QueryRow(ctx, `SELECT managed_by::text,managed_version FROM openfox.telegram_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, r.bot.ID).Scan(&id, &expected)
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
	can, err := telegramCanManage(ctx, tx, org, actor, r)
	if err != nil {
		return actor, err
	}
	if !can {
		return actor, ErrForbidden
	}
	return actor, telegramActor(ctx, tx, org, actor, lock)
}
