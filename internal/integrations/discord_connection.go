package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/integrations/discord"
	"openfox/internal/membership"
	"strings"
	"time"
	"unicode/utf8"
)

func (s Service) PrepareDiscordConnection(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in DiscordConnectInput) (DiscordConnectTarget, error) {
	p := DiscordConnectTarget{org: org, actor: actor.UserID, actorVersion: actor.AuthorizationVersion, input: in}
	if e := discordActor(ctx, tx, org, actor, false); e != nil {
		return p, e
	}
	if s.Box == nil {
		return p, ErrSecretsUnavailable
	}
	if !membership.ValidateUUID(in.OperationKey) || len(in.Token) < 1 || len(in.Token) > 4096 || strings.IndexFunc(in.Token, func(r rune) bool { return r <= 32 || r == 127 }) >= 0 || !utf8.ValidString(in.Token) || len([]rune(in.ExtraPrompt)) > 4000 {
		return p, ErrInvalidRequest
	}
	if in.Kind != BotKindTaskAssistant && in.Kind != BotKindAgent {
		return p, ErrInvalidRequest
	}
	if in.BotID != "" {
		r, e := managedDiscord(ctx, tx, org, actor, in.BotID, false)
		if e != nil {
			return p, e
		}
		if in.Version == 0 || in.Version != r.bot.Version {
			return p, ErrVersionConflict
		}
		if in.Kind != r.bot.Kind || (in.Kind == BotKindTaskAssistant && (in.SpaceID != r.spaceID || in.AgentID != "")) || (in.Kind == BotKindAgent && (in.AgentID != r.bot.Agent.ID || in.SpaceID != "")) {
			return p, ErrInvalidRequest
		}
		if in.Kind == BotKindTaskAssistant {
			cur := r.bot.Runtime
			if cur == nil || in.Runtime == nil || cur.ComputerID != in.Runtime.ComputerID || cur.GrantID != in.Runtime.GrantID || cur.Runtime != in.Runtime.Runtime || cur.Model != in.Runtime.Model || r.bot.ExtraPrompt != in.ExtraPrompt {
				return p, ErrInvalidRequest
			}
		}
		p.remoteID, p.applicationID = r.bot.RemoteBotID, r.bot.ApplicationID
	} else if in.Version != 0 {
		return p, ErrInvalidRequest
	}
	if e := discordCheckConnectTarget(ctx, tx, org, actor, &p, false); e != nil {
		return p, e
	}
	raw, e := json.Marshal(in)
	if e != nil {
		return p, ErrInvalidRequest
	}
	p.requestHash = TokenHash(string(raw))
	return p, nil
}
func discordCheckConnectTarget(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p *DiscordConnectTarget, lock bool) error {
	in := p.input
	lockSQL := ""
	if lock {
		lockSQL = " FOR SHARE"
	}
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
		if err = tx.QueryRow(ctx, `SELECT version FROM openfox.spaces WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE' AND deleted_at IS NULL`+lockSQL, in.SpaceID).Scan(&version); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if p.targetVersion != 0 && p.targetVersion != version {
			return ErrVersionConflict
		}
		p.targetVersion = version
		read, err := (conversations.Service{}).CanReadSpace(ctx, tx, org, conversations.Actor(actor), in.SpaceID)
		if err != nil {
			return err
		}
		if !read {
			return ErrForbidden
		}
		config, err := agents.TaskAssistantConfig(agents.AssistantInput{ComputerID: in.Runtime.ComputerID, GrantID: in.Runtime.GrantID, Runtime: in.Runtime.Runtime, Model: in.Runtime.Model}, in.ExtraPrompt)
		if err != nil {
			return ErrInvalidRequest
		}
		if err = agents.CheckRuntimeTarget(ctx, tx, org, config); err != nil {
			return err
		}
		config.Instructions = strings.Replace(config.Instructions, "负责这个空间在飞书里的任务协作。", "负责这个空间在 Discord 已授权文字频道中的任务协作。", 1)
		config.Instructions = strings.Replace(config.Instructions, "只在被 @ 或私聊时响应", "只在已授权频道被 @ 时响应", 1)
		p.config = config
	} else {
		if !membership.ValidateUUID(in.AgentID) || in.SpaceID != "" || in.Runtime != nil || in.ExtraPrompt != "" {
			return ErrInvalidRequest
		}
		if err := agentBindCheck(ctx, tx, actor, in.AgentID); err != nil {
			return err
		}
		var version uint64
		var space, visibility, owner string
		if err := tx.QueryRow(ctx, `SELECT version,space_id::text,visibility,COALESCE(owner_user_id::text,'') FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE' AND archived_at IS NULL`+lockSQL, in.AgentID).Scan(&version, &space, &visibility, &owner); err != nil {
			return err
		}
		if p.targetVersion != 0 && p.targetVersion != version {
			return ErrVersionConflict
		}
		p.targetVersion = version
		if visibility == "PRIVATE" && owner != actor.UserID {
			return ErrForbidden
		}
		read, err := (conversations.Service{}).CanReadSpace(ctx, tx, org, conversations.Actor(actor), space)
		if err != nil {
			return err
		}
		if !read {
			return ErrForbidden
		}
	}
	return nil
}

// VerifyDiscordConnection is read-only and outside every SQL transaction.
func (s Service) VerifyDiscordConnection(ctx context.Context, p DiscordConnectTarget) (DiscordConnectProof, error) {
	var proof DiscordConnectProof
	if len(p.requestHash) != 32 || p.org == "" || p.actor == "" {
		return proof, ErrInvalidRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	bot, e := s.discordClient().GetMe(ctx, p.input.Token)
	if e != nil {
		return proof, e
	}
	app, e := s.discordClient().GetApplication(ctx, p.input.Token)
	if e != nil {
		return proof, e
	}
	if app.Bot != nil && app.Bot.ID != bot.ID {
		return proof, ErrPersonalIdentity
	}
	if p.remoteID != "" && (p.remoteID != bot.ID || p.applicationID != app.ID) {
		return proof, ErrPersonalIdentity
	}
	if *app.RequiresCodeGrant || p.input.Kind == BotKindTaskAssistant && !app.MessageContentEnabled() {
		return proof, ErrDiscordUnsupported
	}
	gateway, e := s.discordClient().GetGatewayBot(ctx, p.input.Token)
	if e != nil {
		return proof, e
	}
	if gateway.Shards != 1 {
		return proof, ErrDiscordUnsupported
	}
	if len([]rune(bot.Username)) > 128 {
		return proof, ErrDiscordUnsupported
	}
	return DiscordConnectProof{requestHash: append([]byte(nil), p.requestHash...), bot: bot, app: app, gateway: gateway, verifiedAt: time.Now()}, nil
}
func (s Service) SaveDiscordPreparation(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, p DiscordConnectTarget, proof DiscordConnectProof) (DiscordPreparation, error) {
	var out DiscordPreparation
	if p.org != org || p.actor != actor.UserID || p.actorVersion != actor.AuthorizationVersion || !bytes.Equal(p.requestHash, proof.requestHash) || proof.verifiedAt.IsZero() || time.Since(proof.verifiedAt) < 0 || time.Since(proof.verifiedAt) > time.Minute {
		return out, ErrDiscordSessionState
	}
	if e := discordActor(ctx, tx, org, actor, false); e != nil {
		return out, e
	}
	if p.input.BotID != "" {
		r, e := managedDiscord(ctx, tx, org, actor, p.input.BotID, true)
		if e != nil {
			return out, e
		}
		if r.bot.Version != p.input.Version {
			return out, ErrVersionConflict
		}
	}
	if e := discordCheckConnectTarget(ctx, tx, org, actor, &p, true); e != nil {
		return out, e
	}
	targetControl := discordControl{spaceID: p.input.SpaceID, bot: DiscordBot{Kind: p.input.Kind}, owner: actor.UserID}
	if p.input.Kind == BotKindAgent {
		targetControl, e := loadDiscordTargetAccess(ctx, tx, p.input.AgentID)
		if e != nil {
			return out, e
		}
		if e = discordFinalManager(ctx, tx, org, actor, targetControl); e != nil {
			return out, e
		}
	} else if e := discordFinalManager(ctx, tx, org, actor, targetControl); e != nil {
		return out, e
	}
	if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, org+":discord:prepare:"+actor.UserID+":"+p.input.OperationKey); e != nil {
		return out, e
	}
	var existing string
	var hash []byte
	e := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM openfox.discord_connection_preparations WHERE organization_id=openfox_private.current_organization_id() AND actor_id=$1::uuid AND operation_key=$2::uuid`, actor.UserID, p.input.OperationKey).Scan(&existing, &hash)
	if e == nil {
		if !bytes.Equal(hash, p.requestHash) {
			return out, ErrVersionConflict
		}
		return s.GetDiscordPreparation(ctx, tx, org, actor, existing)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	id, e := membership.NewID()
	if e != nil {
		return out, e
	}
	sealed, _, e := s.seal([]byte(p.input.Token), discordAAD(org, "prepare", id))
	if e != nil {
		return out, e
	}
	cfg, _ := json.Marshal(p.input.Runtime)
	if string(cfg) == "null" {
		cfg = []byte(`{}`)
	}
	name := proof.app.Name
	if len([]rune(name)) > 200 {
		name = string([]rune(name)[:200])
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.discord_connection_preparations(organization_id,id,actor_id,actor_version,operation_key,request_hash,kind,space_id,agent_id,target_version,bot_id,bot_version,remote_bot_id,application_id,username,bot_name,message_content_enabled,bot_public,config,extra_prompt,token_sealed,expires_at)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4::uuid,$5,$6,NULLIF($7,'')::uuid,NULLIF($8,'')::uuid,$9,NULLIF($10,'')::uuid,$11,$12,$13,$14,$15,$16,$17,$18::jsonb,$19,$20,clock_timestamp()+interval '10 minutes')`, id, actor.UserID, actor.AuthorizationVersion, p.input.OperationKey, p.requestHash, p.input.Kind, p.input.SpaceID, p.input.AgentID, p.targetVersion, p.input.BotID, p.input.Version, string(proof.bot.ID), string(proof.app.ID), proof.bot.Username, name, proof.app.MessageContentEnabled(), *proof.app.BotPublic, string(cfg), p.input.ExtraPrompt, sealed)
	if isUniqueViolation(e) {
		return out, ErrVersionConflict
	}
	if e != nil {
		return out, e
	}
	return s.GetDiscordPreparation(ctx, tx, org, actor, id)
}

const discordPreparationColumns = `id::text,kind,COALESCE(space_id::text,''),COALESCE(agent_id::text,''),COALESCE(bot_id::text,''),remote_bot_id::text,application_id::text,username,bot_name,message_content_enabled,status,version,expires_at`

func scanDiscordPreparation(row pgx.Row) (DiscordPreparation, error) {
	var p DiscordPreparation
	e := row.Scan(&p.ID, &p.Kind, &p.SpaceID, &p.AgentID, &p.BotID, &p.RemoteBotID, &p.ApplicationID, &p.Username, &p.BotName, &p.MessageContentEnabled, &p.Status, &p.Version, &p.ExpiresAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, e
}
func (s Service) GetDiscordPreparation(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (DiscordPreparation, error) {
	if e := discordActor(ctx, tx, org, actor, false); e != nil {
		return DiscordPreparation{}, e
	}
	if !membership.ValidateUUID(id) {
		return DiscordPreparation{}, ErrNotFound
	}
	return scanDiscordPreparation(tx.QueryRow(ctx, `SELECT `+discordPreparationColumns+` FROM openfox.discord_connection_preparations WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND actor_id=$2::uuid`, id, actor.UserID))
}

func (s Service) ConfirmDiscordPreparation(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in DiscordConfirmInput) (DiscordBot, error) {
	var out DiscordBot
	peek, e := s.GetDiscordPreparation(ctx, tx, org, actor, id)
	if e != nil {
		return out, e
	}
	var current discordControl
	if peek.BotID != "" {
		current, e = managedDiscord(ctx, tx, org, actor, peek.BotID, true)
		if e != nil {
			return out, e
		}
	}
	var p DiscordConnectTarget
	var sealed, cfg []byte
	var status, username, name string
	var version uint64
	var expires time.Time
	var remote, app discord.ID
	var content, public bool
	p.org, p.actor = org, actor.UserID
	e = tx.QueryRow(ctx, `SELECT actor_version,kind,COALESCE(space_id::text,''),COALESCE(agent_id::text,''),target_version,COALESCE(bot_id::text,''),bot_version,config,extra_prompt,token_sealed,status,version,expires_at,remote_bot_id::text,application_id::text,username,bot_name,message_content_enabled,bot_public
 FROM openfox.discord_connection_preparations WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND actor_id=$2::uuid FOR UPDATE`, id, actor.UserID).Scan(&p.actorVersion, &p.input.Kind, &p.input.SpaceID, &p.input.AgentID, &p.targetVersion, &p.input.BotID, &p.input.Version, &cfg, &p.input.ExtraPrompt, &sealed, &status, &version, &expires, &remote, &app, &username, &name, &content, &public)
	if e != nil {
		return out, e
	}
	if status == "COMPLETED" {
		return s.GetDiscordBot(ctx, tx, org, actor, p.input.BotID)
	}
	if status != "PENDING_CONFIRM" || !time.Now().Before(expires) || p.actorVersion != actor.AuthorizationVersion {
		return out, ErrDiscordSessionState
	}
	if in.Version == 0 || version != in.Version || in.RemoteBotID != string(remote) || in.ApplicationID != string(app) {
		return out, ErrVersionConflict
	}
	if current.bot.ID != "" && (current.bot.Version != p.input.Version || current.bot.RemoteBotID != remote || current.bot.ApplicationID != app) {
		return out, ErrVersionConflict
	}
	if p.input.Kind == BotKindTaskAssistant {
		p.input.Runtime = &FeishuAssistantRuntime{}
		if json.Unmarshal(cfg, p.input.Runtime) != nil {
			return out, ErrInvalidRequest
		}
	}
	if e = discordCheckConnectTarget(ctx, tx, org, actor, &p, true); e != nil {
		return out, e
	}
	plain, e := s.open(sealed, discordAAD(org, "prepare", id))
	if e != nil {
		return out, e
	}
	botID, connectionID := p.input.BotID, current.bot.ConnectionID
	if botID == "" {
		botID, e = membership.NewID()
		if e != nil {
			return out, e
		}
		connectionID, e = membership.NewID()
		if e != nil {
			return out, e
		}
	}
	credentials, key, e := s.sealCredentials(org, connectionID, connectionCredentials{AccessToken: string(plain)})
	if e != nil {
		return out, e
	}
	agentID := p.input.AgentID
	if current.bot.ID == "" {
		_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,credentials_sealed,credentials_key_id,created_by)
 VALUES(openfox_private.current_organization_id(),$1::uuid,'discord','ORGANIZATION',$2,$3,$4,$5,$6,$7::uuid)`, connectionID, string(remote), name, strings.ToLower(p.input.Kind), credentials, key, actor.UserID)
		if e != nil {
			if isUniqueViolation(e) {
				return out, ErrDiscordBotOccupied
			}
			return out, e
		}
		if p.input.Kind == BotKindTaskAssistant {
			agentID, e = agents.CreateTaskAssistant(ctx, tx, selectionFor(org, actor), agentActor(actor), p.input.SpaceID, p.config)
			if e != nil {
				return out, e
			}
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.im_bots(organization_id,id,connection_id,kind,agent_id,space_id,extra_prompt,created_by)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4::uuid,NULLIF($5,'')::uuid,$6,$7::uuid)`, botID, connectionID, p.input.Kind, agentID, p.input.SpaceID, p.input.ExtraPrompt, actor.UserID)
		if e != nil {
			if isUniqueViolation(e) {
				return out, ErrDiscordBotOccupied
			}
			return out, e
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.discord_bot_controls(organization_id,bot_id,connection_id,remote_bot_id,application_id,username,name,kind,target_space_id,target_agent_id,message_content_enabled,bot_public,managed_by,managed_version)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6,$7,NULLIF($8,'')::uuid,NULLIF($9,'')::uuid,$10,$11,$12::uuid,$13)`, botID, connectionID, string(remote), string(app), username, name, p.input.Kind, p.input.SpaceID, p.input.AgentID, content, public, actor.UserID, actor.AuthorizationVersion)
		if e != nil {
			if isUniqueViolation(e) {
				return out, ErrDiscordBotOccupied
			}
			return out, e
		}
	} else {
		// A disconnected task assistant remains archived. A deliberate reconnect
		// creates a fresh assistant; no old Run/conversation authority is revived.
		agentID = current.bot.Agent.ID
		if current.bot.State == "DISCONNECTED" && p.input.Kind == BotKindTaskAssistant {
			agentID, e = agents.CreateTaskAssistant(ctx, tx, selectionFor(org, actor), agentActor(actor), p.input.SpaceID, p.config)
			if e != nil {
				return out, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE openfox.im_bots SET removed_at=NULL,agent_id=$2::uuid,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, botID, agentID); e != nil {
			if isUniqueViolation(e) {
				return out, ErrDiscordBotOccupied
			}
			return out, e
		}
		if e = stopDiscordAuthorization(ctx, tx, current, true); e != nil {
			return out, e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',disconnected_at=NULL,credentials_sealed=$2,credentials_key_id=$3,display_name=$4,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, connectionID, credentials, key, name)
		if e != nil {
			return out, e
		}
		tag, e := tx.Exec(ctx, `UPDATE openfox.discord_bot_controls SET state='ACTIVE',username=$2,name=$3,message_content_enabled=$4,bot_public=$5,authorization_generation=authorization_generation+1,token_version=token_version+1,managed_by=$6::uuid,managed_version=$7,last_error='',send_claim='',send_lease_until=NULL,global_not_before=NULL,version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND version=$8`, botID, username, name, content, public, actor.UserID, actor.AuthorizationVersion, current.bot.Version)
		if e != nil {
			return out, e
		}
		if tag.RowsAffected() != 1 {
			return out, ErrVersionConflict
		}
	}
	// No existing Agent/Run locks are acquired after this member proof.
	finalTarget, e := loadDiscordTargetAccess(ctx, tx, agentID)
	if e != nil {
		return out, e
	}
	finalTarget.bot.Kind = p.input.Kind
	if e = discordFinalManager(ctx, tx, org, actor, finalTarget); e != nil {
		return out, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.im_connection_states(organization_id,connection_id,state) VALUES(openfox_private.current_organization_id(),$1::uuid,'PENDING')
 ON CONFLICT(organization_id,connection_id) DO UPDATE SET state='PENDING',lease_owner='',lease_expires_at=NULL,failure_count=0,last_error='',updated_at=clock_timestamp()`, connectionID)
	if e != nil {
		return out, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.discord_gateway_sessions(organization_id,connection_id,bot_id,authorization_generation,token_version)
 SELECT organization_id,connection_id,bot_id,authorization_generation,token_version FROM openfox.discord_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid
 ON CONFLICT(organization_id,connection_id) DO UPDATE SET authorization_generation=EXCLUDED.authorization_generation,token_version=EXCLUDED.token_version,lease_fence=discord_gateway_sessions.lease_fence+1,attempt_id=NULL,session_generation=discord_gateway_sessions.session_generation+1,resume_sealed=NULL,durable_seq=-1,gateway_url='',identify_remaining=-1,identify_reset_at=NULL,identify_not_before=NULL`, botID)
	if e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `UPDATE openfox.discord_connection_preparations SET status='CANCELLED',token_sealed=NULL,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND id<>$2::uuid AND status='PENDING_CONFIRM'`, botID, id); e != nil {
		return out, e
	}

	tag, e := tx.Exec(ctx, `UPDATE openfox.discord_connection_preparations SET status='COMPLETED',token_sealed=NULL,bot_id=$2::uuid,version=version+1,completed_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='PENDING_CONFIRM' AND version=$3`, id, botID, version)
	if e != nil {
		return out, e
	}
	if tag.RowsAffected() != 1 {
		return out, ErrVersionConflict
	}
	if e = discordAudit(ctx, tx, org, actor, "connection_confirmed", botID); e != nil {
		return out, e
	}
	return s.GetDiscordBot(ctx, tx, org, actor, botID)
}
