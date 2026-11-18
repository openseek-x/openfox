package integrations

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
)

const qqBindingColumns = `id::text,bot_id::text,scope,chat_id,subject_id,owner_user_id::text,proof_kind,COALESCE(group_epoch_id::text,''),generation,version,active`

func scanQQBinding(row pgx.Row) (QQBinding, error) {
	var v QQBinding
	e := row.Scan(&v.ID, &v.BotID, &v.Scope, &v.ChatID, &v.SubjectID, &v.OwnerUserID, &v.ProofKind, &v.GroupEpochID, &v.Generation, &v.Version, &v.Active)
	return v, e
}

// Ordinary members see only visible Agent bots and their own identities and
// invitations. Manager-only candidates and delivery diagnostics are separate.
func (s Service) QQConfiguration(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) (QQConfiguration, error) {
	out := QQConfiguration{Configured: s.QQConfigured(), QRConfigured: s.qqConnector().Configured(), Bots: []QQBot{}, Bindings: []QQBinding{}, BindingSessions: []QQBindingSession{}}
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT `+qqBotColumns+` FROM openfox.qq_bot_controls q JOIN openfox.im_bots b ON (b.organization_id,b.id)=(q.organization_id,q.bot_id) JOIN openfox.integration_connections c ON (c.organization_id,c.id)=(q.organization_id,q.connection_id) JOIN openfox.agents ag ON (ag.organization_id,ag.id)=(b.organization_id,b.agent_id) JOIN openfox.spaces s ON (s.organization_id,s.id)=(b.organization_id,b.space_id) WHERE q.organization_id=openfox_private.current_organization_id() AND b.removed_at IS NULL AND (ag.owner_user_id=$2::uuid OR $1 IN ('OWNER','ADMIN') OR (ag.visibility<>'PRIVATE' AND `+collaboration.SpaceReadableSQL+`)) ORDER BY q.created_at,q.bot_id LIMIT $3`, strings.ToUpper(string(a.Role)), a.UserID, maxDetailRows)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		v, e := scanQQBot(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Bots = append(out.Bots, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	// One set query gives management ownership; no per-bot permission query.
	rows, e = tx.Query(ctx, `SELECT b.id::text FROM openfox.im_bots b JOIN openfox.agents a ON (a.organization_id,a.id)=(b.organization_id,b.agent_id) WHERE b.organization_id=openfox_private.current_organization_id() AND b.id=ANY($1::uuid[]) AND (a.owner_user_id=$2::uuid OR $3)`, qqBotIDs(out.Bots), a.UserID, CanManage(a.Role))
	if e != nil {
		return out, e
	}
	managers := map[string]bool{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		managers[id] = true
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	for i := range out.Bots {
		out.Bots[i].CanManage = managers[out.Bots[i].ID]
	}
	rows, e = tx.Query(ctx, `SELECT `+qqBindingColumns+` FROM openfox.qq_bindings WHERE organization_id=openfox_private.current_organization_id() AND owner_user_id=$1::uuid AND active ORDER BY created_at,id LIMIT $2`, a.UserID, maxDetailRows)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		v, e := scanQQBinding(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Bindings = append(out.Bindings, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	// loadQQBindingSession's projection is shared here without returning Code.
	rows, e = tx.Query(ctx, `SELECT s.id::text,s.bot_id::text,s.purpose,s.created_by::text,s.owner_user_id::text,s.status,s.version,s.expires_at,COALESCE(s.receipt_id::text,''),COALESCE(s.group_epoch_id::text,''),COALESCE(s.binding_id::text,''),COALESCE(p.chat_id,''),COALESCE(p.subject_id,''),COALESCE(p.member_role,'') FROM openfox.qq_binding_sessions s LEFT JOIN openfox.qq_message_receipts p ON (p.organization_id,p.id)=(s.organization_id,s.receipt_id) WHERE s.organization_id=openfox_private.current_organization_id() AND (s.created_by=$1::uuid OR s.owner_user_id=$1::uuid) AND s.status IN ('PENDING','OBSERVED') AND s.expires_at>clock_timestamp() ORDER BY s.created_at DESC,s.id LIMIT $2`, a.UserID, maxDetailRows)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var v QQBindingSession
		if e = rows.Scan(&v.ID, &v.BotID, &v.Purpose, &v.CreatedBy, &v.OwnerUserID, &v.Status, &v.Version, &v.ExpiresAt, &v.ReceiptID, &v.GroupEpochID, &v.BindingID, &v.ChatID, &v.SubjectID, &v.MemberRole); e != nil {
			return out, e
		}
		out.BindingSessions = append(out.BindingSessions, v)
	}
	return out, rows.Err()
}
func qqBotIDs(bots []QQBot) []string {
	out := make([]string, 0, len(bots))
	for _, b := range bots {
		out = append(out, b.ID)
	}
	return out
}

func (s Service) GetQQBotDetail(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, bot string) (QQBotDetail, error) {
	out := QQBotDetail{Groups: []QQGroup{}, Bindings: []QQBinding{}, Candidates: []QQCandidate{}, QRSessions: []QQQRSession{}, Replies: []QQReplyDiagnostic{}}
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return out, e
	}
	b, e := loadQQBot(ctx, tx, bot)
	if e != nil {
		return out, e
	}
	if e = qqBotStopManager(ctx, tx, a, b); e != nil {
		return out, e
	}
	b.CanManage = true
	out.Bot = b
	rows, e := tx.Query(ctx, `SELECT id::text,bot_id::text,chat_id,authorized_by::text,version,generation,disabled_at IS NULL,created_at FROM openfox.qq_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid ORDER BY created_at DESC,id LIMIT 100`, bot)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v QQGroup
		if e = rows.Scan(&v.ID, &v.BotID, &v.ChatID, &v.AuthorizedBy, &v.Version, &v.Generation, &v.Enabled, &v.CreatedAt); e != nil {
			rows.Close()
			return out, e
		}
		out.Groups = append(out.Groups, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = tx.Query(ctx, `SELECT `+qqBindingColumns+` FROM openfox.qq_bindings WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND active ORDER BY created_at,id LIMIT $2`, bot, maxDetailRows)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		v, e := scanQQBinding(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Bindings = append(out.Bindings, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = tx.Query(ctx, `SELECT id::text,chat_id,subject_id,member_role,received_at FROM openfox.qq_message_receipts WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND generation=$2 AND event_type='GROUP_AT_MESSAGE_CREATE' AND received_at>clock_timestamp()-interval '10 minutes' AND disposition='IGNORED' AND status_reason='identity_unbound' ORDER BY received_at DESC,id LIMIT 100`, bot, b.Generation)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var v QQCandidate
		if e = rows.Scan(&v.ReceiptID, &v.ChatID, &v.SubjectID, &v.MemberRole, &v.ReceivedAt); e != nil {
			rows.Close()
			return out, e
		}
		out.Candidates = append(out.Candidates, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = tx.Query(ctx, `SELECT `+qqQRColumns+` FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid ORDER BY created_at DESC,id LIMIT 10`, bot)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		v, e := scanQQQR(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.QRSessions = append(out.QRSessions, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = tx.Query(ctx, `SELECT id::text,state,status_reason,version,remote_message_id,attempts,deadline,created_at FROM openfox.qq_replies WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid ORDER BY created_at DESC,id LIMIT 100`, bot)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var v QQReplyDiagnostic
		if e = rows.Scan(&v.ID, &v.State, &v.StatusReason, &v.Version, &v.RemoteMessageID, &v.Attempts, &v.Deadline, &v.CreatedAt); e != nil {
			return out, e
		}
		out.Replies = append(out.Replies, v)
	}
	return out, rows.Err()
}

func (s Service) CancelQQBindingSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) (QQBindingSession, error) {
	v, e := s.GetQQBindingSession(ctx, tx, org, actor, id)
	if e != nil {
		return v, e
	}
	if _, e = lockQQBot(ctx, tx, v.BotID); e != nil {
		return v, e
	}
	if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.qq_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&id); e != nil {
		return v, e
	}
	v, e = loadQQBindingSession(ctx, tx, id)
	if e != nil {
		return v, e
	}
	if version == 0 || v.Version != version {
		return v, ErrVersionConflict
	}
	if v.Status == "CANCELLED" {
		return v, nil
	}
	if v.Status != "PENDING" && v.Status != "OBSERVED" {
		return v, ErrVersionConflict
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_binding_sessions SET status='CANCELLED',version=version+1,code_sealed=NULL,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	if e != nil {
		return v, e
	}
	return loadQQBindingSession(ctx, tx, id)
}

// Release only the local Agent slot. It cannot uninstall a remote App and never
// releases the verified AppID tombstone for another organization.
func (s Service) ReleaseQQBot(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in QQQRActionInput) (QQBot, error) {
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return QQBot{}, e
	}
	b, e := lockQQBot(ctx, tx, id)
	if e != nil {
		return b, e
	}
	if e = qqBotStopManager(ctx, tx, a, b); e != nil {
		return b, e
	}
	if b.Version != in.Version {
		return b, ErrVersionConflict
	}
	if b.removed {
		return b, nil
	}
	if b.State != "DISCONNECTED" && b.State != "PREPARED" && b.State != "NEEDS_REAUTH" {
		return b, ErrVersionConflict
	}
	var pending, external bool
	e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND (status IN ('PENDING','POLLING','WAITING_SCAN') OR status='UNKNOWN' AND status_reason<>'local_helper_stopped' AND expires_at+interval '8 seconds'>clock_timestamp())),EXISTS(SELECT 1 FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('DONE','UNKNOWN'))`, id).Scan(&pending, &external)
	if e != nil {
		return b, e
	}
	if pending || ((external || b.AppID != "") && !in.RemoteChecked) {
		return b, ErrVersionConflict
	}
	if e = invalidateQQAuthorization(ctx, tx, id, "released"); e != nil {
		return b, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.im_bots SET removed_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	if e != nil {
		return b, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET state='CANCELLED',version=version+1,generation=generation+1,credential_version=credential_version+1,webhook_hash=NULL,webhook_sealed=NULL,token_sealed=NULL,token_expires_at=NULL,refresh_after=NULL,lease_owner='',lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, id)
	if e != nil {
		return b, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',credentials_sealed=NULL,credentials_key_id='',version=version+1,disconnected_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, b.ConnectionID)
	if e != nil {
		return b, e
	}
	return loadQQBot(ctx, tx, id)
}
