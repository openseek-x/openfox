package integrations

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/telegram"
	"openfox/internal/membership"
)

type TelegramWebhookLookup struct {
	OrganizationID string
	BotID          string
}
type TelegramReceiveResult struct {
	Duplicate bool   `json:"duplicate"`
	Result    string `json:"result"`
}

// Lookup carries only trusted internal IDs. The callback token and header are
// both rechecked in the tenant transaction; a copied lookup is not authority.
func (s Service) ReceiveTelegram(ctx context.Context, tx pgx.Tx, lookup TelegramWebhookLookup, routeToken string, header http.Header, raw []byte) (TelegramReceiveResult, error) {
	out := TelegramReceiveResult{}
	if len(raw) == 0 || len(raw) > telegram.MaxBodyBytes || len(routeToken) != 43 {
		return out, ErrInvalidRequest
	}
	r, err := loadTelegramControl(ctx, tx, lookup.BotID, true)
	if err != nil {
		return out, err
	}
	if r.bot.State == "DISCONNECTED" || r.bot.State == "DELETING" {
		return out, ErrNotFound
	}
	var routeHash []byte
	if err = tx.QueryRow(ctx, `SELECT route_hash FROM openfox.telegram_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, r.bot.ID).Scan(&routeHash); err != nil {
		return out, err
	}
	if !bytes.Equal(routeHash, TokenHash(routeToken)) {
		return out, ErrNotFound
	}
	h, err := s.telegramCoordinates(lookup.OrganizationID, r)
	if err != nil {
		return out, err
	}
	if err = telegram.VerifyWebhookSecret(h.Secret, header); err != nil {
		return out, err
	}
	update, decodeErr := telegram.DecodeUpdate(raw)
	if decodeErr != nil && !errors.Is(decodeErr, telegram.ErrUnsupported) {
		return out, decodeErr
	}
	eventID := formatID(int64(update.ID))
	hash := TokenHash(string(raw))
	tag, err := tx.Exec(ctx, `INSERT INTO openfox.im_inbound_events(organization_id,connection_id,event_id,event_type)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3) ON CONFLICT DO NOTHING`, r.bot.ConnectionID, eventID, update.Kind)
	if err != nil {
		return out, err
	}
	if tag.RowsAffected() == 0 {
		var prior []byte
		err = tx.QueryRow(ctx, `SELECT body_hash,result FROM openfox.telegram_update_receipts WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND event_id=$2`, r.bot.ConnectionID, eventID).Scan(&prior, &out.Result)
		if err != nil {
			return out, err
		}
		if !bytes.Equal(hash, prior) {
			return out, ErrVersionConflict
		}
		out.Duplicate = true
		return out, nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.telegram_update_receipts(organization_id,connection_id,event_id,body_hash,kind) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4)`, r.bot.ConnectionID, eventID, hash, update.Kind)
	if err != nil {
		return out, err
	}
	// A valid current-secret update proves a previously uncertain setWebhook,
	// even if it is an unsupported message. It grants no person/target authority.
	if r.bot.ControlStatus == "UNKNOWN" && r.bot.State == "ATTENTION" {
		var action string
		if err = tx.QueryRow(ctx, `SELECT control_action FROM openfox.telegram_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, r.bot.ID).Scan(&action); err != nil {
			return out, err
		}
		if action == "SET" {
			_, managerErr := telegramControlManager(ctx, tx, lookup.OrganizationID, r, true)
			if managerErr != nil && !errors.Is(managerErr, ErrForbidden) {
				return out, managerErr
			}
			if managerErr == nil {
				_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET state='ACTIVE',control_status='DONE',control_action='',last_error='',next_attempt_at=NULL,control_claim='',control_lease_until=NULL,configured_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, r.bot.ID)
				if err != nil {
					return out, err
				}
				r.bot.State = "ACTIVE"
			}
		}
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET first_update_at=COALESCE(first_update_at,clock_timestamp()),last_update_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, r.bot.ID)
	if err != nil {
		return out, err
	}
	switch {
	case errors.Is(decodeErr, telegram.ErrUnsupported):
		out.Result = "unsupported"
		var apiErr *telegram.Error
		if (errors.As(decodeErr, &apiErr) && apiErr.Code == "unsupported_topic") || (update.Message != nil && update.Message.Chat.IsForum) {
			out.Result = "unsupported_topic"
			_, err = tx.Exec(ctx, `UPDATE openfox.telegram_bot_controls SET unsupported_topic_count=unsupported_topic_count+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, r.bot.ID)
		}
	case update.MyChatMember != nil:
		if update.MyChatMember.NewChatMember.User.ID != r.bot.RemoteBotID {
			out.Result = "wrong_bot"
			break
		}
		if !telegramBotCanParticipate(update.MyChatMember.NewChatMember) {
			err = revokeTelegramChat(ctx, tx, r, update.MyChatMember.Chat.ID)
			out.Result = "chat_revoked"
		} else {
			out.Result = "membership_observed"
		}
	case update.Message != nil && update.Kind == "migration":
		err = revokeTelegramChat(ctx, tx, r, update.Message.Chat.ID)
		out.Result = "migration_closed"
	case update.Message != nil:
		if r.bot.State != "ACTIVE" || r.parentStatus != StatusActive {
			out.Result = "not_active"
			break
		}
		out.Result, err = s.receiveTelegramMessage(ctx, tx, lookup.OrganizationID, r, update.ID, *update.Message)
	default:
		out.Result = "unsupported"
	}
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_update_receipts SET result=$3 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND event_id=$2`, r.bot.ConnectionID, eventID, out.Result)
	return out, err
}
func revokeTelegramChat(ctx context.Context, tx pgx.Tx, r telegramControl, chat telegram.ID) error {
	if chat < 0 {
		var id string
		err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.telegram_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 AND enabled FOR UPDATE`, r.bot.ID, int64(chat)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return disableTelegramEpoch(ctx, tx, id)
	}
	var owner, identity string
	err := tx.QueryRow(ctx, `SELECT owner_user_id::text,id::text FROM openfox.telegram_identities WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND private_chat_id=$2 AND active`, r.bot.ID, int64(chat)).Scan(&owner, &identity)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = closeTelegramIdentityEpochs(ctx, tx, r.bot.ID, owner, identity); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.telegram_identities SET active=false,version=version+1,disabled_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND active`, identity)
	return err
}

type telegramInboundIdentity struct {
	id, owner string
	version   uint64
	confirmed time.Time
	chat      telegram.ID
}

func loadTelegramInboundIdentity(ctx context.Context, tx pgx.Tx, r telegramControl, user telegram.ID) (telegramInboundIdentity, error) {
	var v telegramInboundIdentity
	err := tx.QueryRow(ctx, `SELECT id::text,owner_user_id::text,version,confirmed_at,private_chat_id FROM openfox.telegram_identities WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND remote_user_id=$2 AND active AND authorization_generation=$3 FOR SHARE`, r.bot.ID, int64(user), r.authGeneration).
		Scan(&v.id, &v.owner, &v.version, &v.confirmed, &v.chat)
	return v, err
}
func (s Service) receiveTelegramMessage(ctx context.Context, tx pgx.Tx, org string, r telegramControl, updateID telegram.ID, m telegram.Message) (string, error) {
	if m.From == nil {
		return "unsupported", nil
	}
	facts, err := telegram.ParseText(m, telegram.Bot{User: telegram.User{ID: r.bot.RemoteBotID, IsBot: true, Username: r.bot.Username}})
	if err != nil {
		return "unsupported", nil
	}
	if facts.Command == "start" {
		if err = consumeTelegramBinding(ctx, tx, r, m, facts.CommandArgument); errors.Is(err, ErrTelegramSessionState) {
			return "binding_rejected", nil
		}
		return "binding_candidate", err
	}
	// Lock group epoch before identity, matching strong tool gates. A discovery
	// command can work without an enabled epoch but still proves current identity.
	var epoch string
	var epochVersion uint64
	var background bool
	var enabledAt time.Time
	if m.Chat.ID < 0 {
		err = tx.QueryRow(ctx, `SELECT id::text,version,background_enabled,created_at FROM openfox.telegram_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 AND enabled AND authorization_generation=$3 FOR SHARE`, r.bot.ID, int64(m.Chat.ID), r.authGeneration).
			Scan(&epoch, &epochVersion, &background, &enabledAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	if facts.Command == "openfox_connect" {
		if m.Chat.ID >= 0 {
			return "discovery_rejected", nil
		}
		return discoverTelegramGroup(ctx, tx, org, r, m)
	}
	if m.Chat.ID < 0 && epoch == "" {
		return "group_disabled", nil
	}
	now := time.Now()
	sent := time.Unix(m.Date, 0)
	if sent.After(now.Add(time.Minute)) || now.Sub(sent) > 24*time.Hour {
		return "message_time_rejected", nil
	}
	if m.Chat.ID < 0 && sent.Before(enabledAt.Truncate(time.Second)) {
		return "before_epoch", nil
	}
	triggered := m.Chat.Type == "private" || facts.Mentioned || facts.Command == "ask"
	if !triggered && facts.ReplyToBot {
		var known bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.im_reply_outbox o JOIN openfox.telegram_replies p ON p.organization_id=o.organization_id AND p.outbox_id=o.id
   JOIN openfox.telegram_inputs i ON i.organization_id=p.organization_id AND i.id=p.input_id
   WHERE o.organization_id=openfox_private.current_organization_id() AND o.bot_id=$1::uuid AND o.chat_id=$2 AND o.remote_message_id=$3 AND o.status='DONE' AND p.authorization_generation=$4 AND i.epoch_id=NULLIF($5,'')::uuid)`, r.bot.ID, formatID(int64(m.Chat.ID)), formatID(int64(facts.ReplyToMessageID)), r.authGeneration, epoch).Scan(&known)
		if err != nil {
			return "", err
		}
		triggered = known
	}
	if !triggered {
		if background && r.bot.Kind == BotKindTaskAssistant {
			return "background_cached", cacheTelegramBackground(ctx, tx, epoch, m)
		}
		return "not_mentioned", nil
	}
	identity, err := loadTelegramInboundIdentity(ctx, tx, r, m.From.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "identity_required", queueTelegramBindingNotice(ctx, tx, r, m, epoch)
	}
	if err != nil {
		return "", err
	}
	if m.Chat.Type == "private" && identity.chat != m.Chat.ID {
		return "identity_required", nil
	}
	if sent.Before(identity.confirmed.Truncate(time.Second)) {
		return "before_identity", nil
	}
	current, err := (membership.Service{}).LoadActiveActor(ctx, tx, org, identity.owner)
	if errors.Is(err, membership.ErrAuthorizationStale) {
		return "permission_denied", nil
	}
	if err != nil {
		return "", err
	}
	actor := collaboration.Actor{UserID: current.UserID, Role: current.Role, AuthorizationVersion: current.AuthorizationVersion}
	if err = telegramActor(ctx, tx, org, actor, true); err != nil {
		if errors.Is(err, ErrForbidden) || errors.Is(err, membership.ErrAuthorizationStale) {
			return "permission_denied", nil
		}
		return "", err
	}
	// Current target and space permissions are checked again during materialize,
	// dispatch and every tool call. Receipt admission is never the execution grant.
	var route string
	err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.telegram_routes WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 AND closed_at IS NULL AND authorization_generation=$3
 AND ((kind='p2p' AND identity_id=$4::uuid AND identity_version=$5) OR (kind='group' AND epoch_id=NULLIF($6,'')::uuid))`, r.bot.ID, int64(m.Chat.ID), r.authGeneration, identity.id, identity.version, epoch).Scan(&route)
	if errors.Is(err, pgx.ErrNoRows) {
		return "route_closed", nil
	}
	if err != nil {
		return "", err
	}
	if len([]rune(facts.Text)) > 8000 || facts.Text == "" {
		return "text_limit", nil
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','PROCESSING')`, r.bot.ID).Scan(&count); err != nil {
		return "", err
	}
	if count >= 1000 {
		return "", ErrTelegramQueueFull
	}
	id, err := membership.NewID()
	if err != nil {
		return "", err
	}
	kind := "p2p"
	if epoch != "" {
		kind = "group"
	}
	tag, err := tx.Exec(ctx, `INSERT INTO openfox.im_inbound_messages(organization_id,id,bot_id,remote_message_id,chat_id,chat_type,sender_open_id,message_type,text,parent_remote_id)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6,'text',$7,$8) ON CONFLICT(organization_id,bot_id,remote_message_id) DO NOTHING`,
		id, r.bot.ID, formatID(int64(m.Chat.ID))+":"+formatID(int64(m.ID)), formatID(int64(m.Chat.ID)), kind, formatID(int64(m.From.ID)), facts.Text, formatID(int64(m.ID)))
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "message_duplicate", nil
	}
	tag, err = tx.Exec(ctx, `UPDATE openfox.telegram_update_receipts SET inbound_id=$2::uuid,route_id=$3::uuid,chat_id=$4,remote_message_id=$5,update_id=$6,authorization_generation=$7,actor_id=$8::uuid,actor_version=$9,identity_id=$10::uuid,identity_version=$11,epoch_id=NULLIF($12,'')::uuid,epoch_version=CASE WHEN $12='' THEN NULL ELSE $13::bigint END
 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$14::uuid AND event_id=$15 AND inbound_id IS NULL AND EXISTS(SELECT 1 FROM openfox.telegram_routes rr WHERE rr.organization_id=openfox_private.current_organization_id() AND rr.id=$3::uuid AND rr.bot_id=$1::uuid)`,
		r.bot.ID, id, route, int64(m.Chat.ID), int64(m.ID), int64(updateID), r.authGeneration, identity.owner, actor.AuthorizationVersion, identity.id, identity.version, epoch, epochVersion, r.bot.ConnectionID, formatID(int64(updateID)))
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", ErrVersionConflict
	}
	return "queued", nil
}
func discoverTelegramGroup(ctx context.Context, tx pgx.Tx, org string, r telegramControl, m telegram.Message) (string, error) {
	identity, err := loadTelegramInboundIdentity(ctx, tx, r, m.From.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "identity_required", nil
	}
	if err != nil {
		return "", err
	}
	current, err := (membership.Service{}).LoadActiveActor(ctx, tx, org, identity.owner)
	if errors.Is(err, membership.ErrAuthorizationStale) {
		return "permission_denied", nil
	}
	if err != nil {
		return "", err
	}
	actor := collaboration.Actor{UserID: current.UserID, Role: current.Role, AuthorizationVersion: current.AuthorizationVersion}
	can, err := telegramCanManage(ctx, tx, org, actor, r)
	if err != nil {
		return "", err
	}
	if !can {
		return "permission_denied", nil
	}
	if err = telegramActor(ctx, tx, org, actor, true); err != nil {
		return "", err
	}
	title := m.Chat.Title
	if len([]rune(title)) > 255 {
		title = string([]rune(title)[:255])
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.telegram_group_discoveries(organization_id,bot_id,chat_id,title,identity_id,identity_version,owner_user_id,authorization_generation,expires_at)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4::uuid,$5,$6::uuid,$7,clock_timestamp()+interval '10 minutes')
 ON CONFLICT(organization_id,bot_id,chat_id,owner_user_id) DO UPDATE SET title=EXCLUDED.title,identity_id=EXCLUDED.identity_id,identity_version=EXCLUDED.identity_version,authorization_generation=EXCLUDED.authorization_generation,expires_at=EXCLUDED.expires_at,version=telegram_group_discoveries.version+1,created_at=clock_timestamp()`,
		r.bot.ID, int64(m.Chat.ID), title, identity.id, identity.version, identity.owner, r.authGeneration)
	return "group_discovered", err
}
func cacheTelegramBackground(ctx context.Context, tx pgx.Tx, epoch string, m telegram.Message) error {
	if epoch == "" || m.From == nil || m.Text == "" || len([]rune(m.Text)) > 8000 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO openfox.telegram_background_messages(organization_id,epoch_id,chat_message_id,sender_id,message_date,body)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, epoch, int64(m.ID), int64(m.From.ID), time.Unix(m.Date, 0), m.Text)
	if err != nil {
		return err
	}
	// The control lock serializes receives for this bot. Retain complete messages,
	// newest first, and remove any item outside all three explicit local budgets.
	_, err = tx.Exec(ctx, `WITH ranked AS (SELECT chat_message_id,row_number() OVER(ORDER BY message_date DESC,chat_message_id DESC) n,
 sum(char_length(body)) OVER(ORDER BY message_date DESC,chat_message_id DESC) chars,message_date FROM openfox.telegram_background_messages WHERE organization_id=openfox_private.current_organization_id() AND epoch_id=$1::uuid)
 DELETE FROM openfox.telegram_background_messages b USING ranked r WHERE b.organization_id=openfox_private.current_organization_id() AND b.epoch_id=$1::uuid AND b.chat_message_id=r.chat_message_id AND (r.n>20 OR r.chars>8000 OR r.message_date<clock_timestamp()-interval '10 minutes')`, epoch)
	return err
}
