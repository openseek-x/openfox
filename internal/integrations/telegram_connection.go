package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/telegram"
	"openfox/internal/membership"
)

func (s Service) PrepareTelegramConnection(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in TelegramConnectInput) (TelegramConnectTarget, error) {
	p := TelegramConnectTarget{org: org, actor: actor.UserID, actorVersion: actor.AuthorizationVersion, input: in}
	if err := telegramActor(ctx, tx, org, actor, false); err != nil {
		return p, err
	}
	if s.Box == nil {
		return p, ErrSecretsUnavailable
	}
	if _, err := s.newTelegramCoordinates(); err != nil {
		return p, err
	}
	if !membership.ValidateUUID(in.OperationKey) || len(in.Token) < 3 || len(in.Token) > 512 || strings.TrimSpace(in.Token) != in.Token || len([]rune(in.ExtraPrompt)) > 4000 {
		return p, ErrInvalidRequest
	}
	if in.Kind != BotKindTaskAssistant && in.Kind != BotKindAgent {
		return p, ErrInvalidRequest
	}
	if in.BotID != "" {
		r, err := managedTelegram(ctx, tx, org, actor, in.BotID, false)
		if err != nil {
			return p, err
		}
		if in.Version == 0 || in.Version != r.bot.Version {
			return p, ErrVersionConflict
		}
		if r.bot.Kind != in.Kind || (in.Kind == BotKindTaskAssistant && (in.SpaceID != r.spaceID || in.AgentID != "")) || (in.Kind == BotKindAgent && (in.AgentID != r.bot.Agent.ID || in.SpaceID != "")) {
			return p, ErrInvalidRequest
		}
		if r.bot.Kind == BotKindTaskAssistant {
			cur := r.bot.Runtime
			if cur == nil || in.Runtime == nil || cur.ComputerID != in.Runtime.ComputerID || cur.GrantID != in.Runtime.GrantID || cur.Runtime != in.Runtime.Runtime || cur.Model != in.Runtime.Model || r.bot.ExtraPrompt != in.ExtraPrompt {
				return p, ErrInvalidRequest
			}
		}
		if err := telegramCanReplaceWebhook(ctx, tx, r, in.RecoverWebhook); err != nil {
			return p, err
		}
		h, err := s.telegramCoordinates(org, r)
		if err != nil {
			return p, err
		}
		p.expectedHook = h.URL
		p.expectedPreviousHook = h.PreviousURL
		p.remoteID = r.bot.RemoteBotID
	} else if in.Version != 0 || in.RecoverWebhook {
		return p, ErrInvalidRequest
	}
	if err := telegramCheckConnectTarget(ctx, tx, org, actor, &p); err != nil {
		return p, err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return p, ErrInvalidRequest
	}
	p.requestHash = TokenHash(string(raw))
	return p, nil
}
func telegramCheckConnectTarget(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p *TelegramConnectTarget) error {
	in := p.input
	if in.Kind == BotKindTaskAssistant {
		if !membership.ValidateUUID(in.SpaceID) || in.AgentID != "" || in.Runtime == nil {
			return ErrInvalidRequest
		}
		can, err := agents.CanManageSpace(ctx, tx, org, in.SpaceID, agentActor(actor))
		if err != nil {
			return err
		}
		if !can {
			return ErrForbidden
		}
		var version uint64
		if err = tx.QueryRow(ctx, `SELECT version FROM openfox.spaces WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE'`, in.SpaceID).Scan(&version); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if p.targetVersion != 0 && p.targetVersion != version {
			return ErrVersionConflict
		}
		p.targetVersion = version
		config, err := agents.TaskAssistantConfig(agents.AssistantInput{ComputerID: in.Runtime.ComputerID, GrantID: in.Runtime.GrantID, Runtime: in.Runtime.Runtime, Model: in.Runtime.Model}, in.ExtraPrompt)
		if err != nil {
			return ErrInvalidRequest
		}
		if err = agents.CheckRuntimeTarget(ctx, tx, org, config); err != nil {
			return err
		}
		p.config = config
	} else {
		if !membership.ValidateUUID(in.AgentID) || in.SpaceID != "" || in.Runtime != nil || in.ExtraPrompt != "" {
			return ErrInvalidRequest
		}
		if err := agentBindCheck(ctx, tx, actor, in.AgentID); err != nil {
			return err
		}
		var version uint64
		if err := tx.QueryRow(ctx, `SELECT version FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, in.AgentID).Scan(&version); err != nil {
			return err
		}
		if p.targetVersion != 0 && p.targetVersion != version {
			return ErrVersionConflict
		}
		p.targetVersion = version
	}
	return nil
}

// Verify is read-only. A proof binds the exact private plan; no webhook write
// happens before the final local global-bot uniqueness transaction commits.
func (s Service) VerifyTelegramConnection(ctx context.Context, p TelegramConnectTarget) (TelegramConnectProof, error) {
	var proof TelegramConnectProof
	if len(p.requestHash) != 32 || p.org == "" || p.actor == "" {
		return proof, ErrInvalidRequest
	}
	b, err := s.telegramClient().GetMe(ctx, p.input.Token)
	if err != nil {
		return proof, err
	}
	if p.remoteID != 0 && b.ID != p.remoteID {
		return proof, ErrPersonalIdentity
	}
	if b.HasTopicsEnabled != nil && *b.HasTopicsEnabled {
		return proof, ErrTelegramUnsupported
	}
	w, err := s.telegramClient().GetWebhookInfo(ctx, p.input.Token)
	if err != nil {
		return proof, err
	}
	if w.URL != "" && w.URL != p.expectedHook && (p.expectedPreviousHook == "" || w.URL != p.expectedPreviousHook) {
		return proof, ErrTelegramWebhookConflict
	}
	proof.requestHash = append([]byte(nil), p.requestHash...)
	proof.bot = b
	proof.verifiedAt = time.Now()
	return proof, nil
}
func (s Service) SaveTelegramPreparation(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p TelegramConnectTarget, proof TelegramConnectProof) (TelegramPreparation, error) {
	var out TelegramPreparation
	if p.org != org || p.actor != actor.UserID || p.actorVersion != actor.AuthorizationVersion || !bytes.Equal(p.requestHash, proof.requestHash) || proof.verifiedAt.IsZero() || time.Since(proof.verifiedAt) < 0 || time.Since(proof.verifiedAt) > time.Minute {
		return out, ErrTelegramSessionState
	}
	if err := telegramActor(ctx, tx, org, actor, false); err != nil {
		return out, err
	}
	// Existing Bot first; preparation locks never precede the common bot order.
	if p.input.BotID != "" {
		r, err := managedTelegram(ctx, tx, org, actor, p.input.BotID, true)
		if err != nil {
			return out, err
		}
		if r.bot.Version != p.input.Version {
			return out, ErrVersionConflict
		}
	}
	if err := telegramCheckConnectTarget(ctx, tx, org, actor, &p); err != nil {
		return out, err
	}
	if err := telegramActor(ctx, tx, org, actor, true); err != nil {
		return out, err
	}
	// Operation key retries serialize without refreshing the original TTL or token.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":telegram:prepare:"+actor.UserID+":"+p.input.OperationKey); err != nil {
		return out, err
	}
	var existing string
	var hash []byte
	err := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM openfox.telegram_connection_preparations WHERE organization_id=openfox_private.current_organization_id() AND actor_id=$1::uuid AND operation_key=$2::uuid`, actor.UserID, p.input.OperationKey).Scan(&existing, &hash)
	if err == nil {
		if !bytes.Equal(hash, p.requestHash) {
			return out, ErrVersionConflict
		}
		return s.GetTelegramPreparation(ctx, tx, org, actor, existing)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	id, err := membership.NewID()
	if err != nil {
		return out, err
	}
	sealed, _, err := s.seal([]byte(p.input.Token), telegramAAD(org, "prepare", id))
	if err != nil {
		return out, err
	}
	cfg, _ := json.Marshal(p.input.Runtime)
	name := proof.bot.FirstName
	if len([]rune(name)) > 200 {
		name = string([]rune(name)[:200])
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.telegram_connection_preparations(organization_id,id,actor_id,actor_version,operation_key,request_hash,kind,space_id,agent_id,target_version,bot_id,bot_version,remote_bot_id,username,bot_name,config,extra_prompt,token_sealed,expires_at,recover_webhook)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4::uuid,$5,$6,NULLIF($7,'')::uuid,NULLIF($8,'')::uuid,$9,NULLIF($10,'')::uuid,$11,$12,$13,$14,$15::jsonb,$16,$17,clock_timestamp()+interval '10 minutes',$18)`,
		id, actor.UserID, actor.AuthorizationVersion, p.input.OperationKey, p.requestHash, p.input.Kind, p.input.SpaceID, p.input.AgentID, p.targetVersion, p.input.BotID, p.input.Version, int64(proof.bot.ID), proof.bot.Username, name, stringObject(cfg), p.input.ExtraPrompt, sealed, p.input.RecoverWebhook)
	if isUniqueViolation(err) {
		return out, ErrVersionConflict
	}
	if err != nil {
		return out, err
	}
	return s.GetTelegramPreparation(ctx, tx, org, actor, id)
}
func stringObject(raw []byte) string {
	if string(raw) == "null" || len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

const telegramPreparationColumns = `id::text,kind,COALESCE(space_id::text,''),COALESCE(agent_id::text,''),COALESCE(bot_id::text,''),remote_bot_id,username,bot_name,status,version,expires_at`

func scanTelegramPreparation(row pgx.Row) (TelegramPreparation, error) {
	var p TelegramPreparation
	err := row.Scan(&p.ID, &p.Kind, &p.SpaceID, &p.AgentID, &p.BotID, &p.RemoteBotID, &p.Username, &p.BotName, &p.Status, &p.Version, &p.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}
func (s Service) GetTelegramPreparation(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (TelegramPreparation, error) {
	if err := telegramActor(ctx, tx, org, actor, false); err != nil {
		return TelegramPreparation{}, err
	}
	if !membership.ValidateUUID(id) {
		return TelegramPreparation{}, ErrNotFound
	}
	return scanTelegramPreparation(tx.QueryRow(ctx, `SELECT `+telegramPreparationColumns+` FROM openfox.telegram_connection_preparations WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND actor_id=$2::uuid`, id, actor.UserID))
}
func (s Service) ConfirmTelegramPreparation(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in TelegramConfirmInput) (TelegramBot, error) {
	var out TelegramBot
	peek, err := s.GetTelegramPreparation(ctx, tx, org, actor, id)
	if err != nil {
		return out, err
	}
	var current telegramControl
	if peek.BotID != "" {
		current, err = managedTelegram(ctx, tx, org, actor, peek.BotID, true)
		if err != nil {
			return out, err
		}
	}
	// Serialized by session, then the unique remote ID / existing target indexes.
	var p TelegramConnectTarget
	var token []byte
	var config []byte
	var status string
	var version uint64
	var expires time.Time
	var remote telegram.ID
	var username, name string
	p.org, p.actor = org, actor.UserID
	err = tx.QueryRow(ctx, `SELECT actor_version,kind,COALESCE(space_id::text,''),COALESCE(agent_id::text,''),target_version,COALESCE(bot_id::text,''),bot_version,config,extra_prompt,token_sealed,status,version,expires_at,remote_bot_id,username,bot_name,recover_webhook
 FROM openfox.telegram_connection_preparations WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND actor_id=$2::uuid FOR UPDATE`, id, actor.UserID).
		Scan(&p.actorVersion, &p.input.Kind, &p.input.SpaceID, &p.input.AgentID, &p.targetVersion, &p.input.BotID, &p.input.Version, &config, &p.input.ExtraPrompt, &token, &status, &version, &expires, &remote, &username, &name, &p.input.RecoverWebhook)
	if err != nil {
		return out, err
	}
	if status == "COMPLETED" {
		return s.GetTelegramBot(ctx, tx, org, actor, p.input.BotID)
	} // Never resurrect a disconnected bot on replay.
	if status != "PENDING_CONFIRM" || !time.Now().Before(expires) || p.actorVersion != actor.AuthorizationVersion {
		return out, ErrTelegramSessionState
	}
	if in.Version != version || in.Version == 0 || in.RemoteBotID != remote.String() {
		return out, ErrVersionConflict
	}
	if p.input.BotID != "" && current.bot.Version != p.input.Version {
		return out, ErrVersionConflict
	}
	if p.input.Kind == BotKindTaskAssistant {
		p.input.Runtime = &FeishuAssistantRuntime{}
		if json.Unmarshal(config, p.input.Runtime) != nil {
			return out, ErrInvalidRequest
		}
	}
	if err = telegramCheckConnectTarget(ctx, tx, org, actor, &p); err != nil {
		return out, err
	}
	if err = telegramActor(ctx, tx, org, actor, true); err != nil {
		return out, err
	}
	plain, err := s.open(token, telegramAAD(org, "prepare", id))
	if err != nil {
		return out, err
	}
	botID, connID := p.input.BotID, current.bot.ConnectionID
	if botID == "" {
		botID, err = membership.NewID()
		if err != nil {
			return out, err
		}
		connID, err = membership.NewID()
		if err != nil {
			return out, err
		}
	}
	h, err := s.newTelegramCoordinates()
	if err != nil {
		return out, err
	}
	if current.bot.ID != "" {
		old, err := s.telegramCoordinates(org, current)
		if err != nil {
			return out, err
		}
		h.PreviousURL = old.URL
	}
	hookRaw, _ := json.Marshal(h)
	hookSeal, _, err := s.seal(hookRaw, telegramAAD(org, "hook", botID))
	if err != nil {
		return out, err
	}
	cred, key, err := s.sealCredentials(org, connID, connectionCredentials{AccessToken: string(plain)})
	if err != nil {
		return out, err
	}
	if p.input.BotID == "" {
		_, err = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,credentials_sealed,credentials_key_id,created_by)
   VALUES(openfox_private.current_organization_id(),$1::uuid,'telegram','ORGANIZATION',$2,$3,$4,$5,$6,$7::uuid)`, connID, formatID(int64(remote)), name, strings.ToLower(p.input.Kind), cred, key, actor.UserID)
		if err != nil {
			if isUniqueViolation(err) {
				return out, ErrTelegramBotOccupied
			}
			return out, err
		}
		agentID := p.input.AgentID
		if p.input.Kind == BotKindTaskAssistant {
			agentID, err = agents.CreateTaskAssistant(ctx, tx, selectionFor(org, actor), agentActor(actor), p.input.SpaceID, p.config)
			if err != nil {
				return out, err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.im_bots(organization_id,id,connection_id,kind,agent_id,space_id,extra_prompt,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4::uuid,NULLIF($5,'')::uuid,$6,$7::uuid)`, botID, connID, p.input.Kind, agentID, p.input.SpaceID, p.input.ExtraPrompt, actor.UserID)
		if err != nil {
			if isUniqueViolation(err) {
				return out, ErrTelegramBotOccupied
			}
			return out, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.telegram_bot_controls(organization_id,bot_id,connection_id,remote_bot_id,username,kind,target_space_id,target_agent_id,route_hash,webhook_sealed,managed_by,managed_version)
   VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,NULLIF($6,'')::uuid,NULLIF($7,'')::uuid,$8,$9,$10::uuid,$11)`, botID, connID, int64(remote), username, p.input.Kind, p.input.SpaceID, p.input.AgentID, TokenHash(h.Route), hookSeal, actor.UserID, actor.AuthorizationVersion)
		if isUniqueViolation(err) {
			return out, ErrTelegramBotOccupied
		}
		if err != nil {
			return out, err
		}
	} else {
		// A code issued before credential rotation cannot become a new binding.
		if _, err = tx.Exec(ctx, `UPDATE openfox.telegram_binding_sessions SET status='CANCELLED',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('WAITING','CANDIDATE')`, botID); err != nil {
			return out, err
		}
		// Existing identities never transfer to a different Bot. Reconnect after a
		// disconnect has already invalidated the authorization generation.
		if current.bot.RemoteBotID != remote {
			return out, ErrPersonalIdentity
		}
		if err = telegramCanReplaceWebhook(ctx, tx, current, p.input.RecoverWebhook); err != nil {
			return out, err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',disconnected_at=NULL,credentials_sealed=$2,credentials_key_id=$3,display_name=$4,updated_at=clock_timestamp(),version=version+1
   WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, connID, cred, key, name)
		if err != nil {
			return out, err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET username=$2,state='SETTING',version=version+1,webhook_generation=webhook_generation+1,token_version=token_version+1,route_hash=$3,webhook_sealed=$4,
   control_action='SET',control_status='PENDING',control_attempts=0,control_claim='',control_lease_until=NULL,next_attempt_at=clock_timestamp(),last_error='',configured_at=NULL,first_update_at=NULL,managed_by=$5::uuid,managed_version=$6,updated_at=clock_timestamp()
   WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, botID, username, TokenHash(h.Route), hookSeal, actor.UserID, actor.AuthorizationVersion)
		if err != nil {
			return out, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_connection_preparations SET status='COMPLETED',token_sealed=NULL,bot_id=$2::uuid,version=version+1,completed_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, botID)
	if err != nil {
		return out, err
	}
	if err = telegramAudit(ctx, tx, org, actor, "connection_confirmed", botID); err != nil {
		return out, err
	}
	return s.GetTelegramBot(ctx, tx, org, actor, botID)
}

// Explicit recovery starts a fresh credential-verified control intent. It never
// retries a message send and never takes over an unexpired control lease.
func telegramCanReplaceWebhook(ctx context.Context, tx pgx.Tx, r telegramControl, recover bool) error {
	if r.bot.ControlStatus == "UNKNOWN" && !recover {
		return ErrTelegramSessionState
	}
	if r.bot.ControlStatus != "SENDING" {
		return nil
	}
	if !recover {
		return ErrTelegramSessionState
	}
	var expired bool
	if err := tx.QueryRow(ctx, `SELECT control_lease_until IS NOT NULL AND control_lease_until<=clock_timestamp() FROM openfox.telegram_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, r.bot.ID).Scan(&expired); err != nil {
		return err
	}
	if !expired {
		return ErrTelegramSessionState
	}
	return nil
}
