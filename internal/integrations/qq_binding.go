package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

type QQBindingSession struct {
	ID                       string    `json:"id"`
	BotID                    string    `json:"bot_id"`
	Purpose                  string    `json:"purpose"`
	CreatedBy                string    `json:"created_by"`
	OwnerUserID              string    `json:"owner_user_id"`
	Status                   string    `json:"status"`
	Version                  uint64    `json:"version"`
	ExpiresAt                time.Time `json:"expires_at"`
	ReceiptID                string    `json:"receipt_id"`
	GroupEpochID             string    `json:"group_epoch_id"`
	BindingID                string    `json:"binding_id"`
	ChatID                   string    `json:"chat_id"`
	SubjectID                string    `json:"subject_id"`
	MemberRole               string    `json:"member_role"`
	Code                     string    `json:"code,omitempty"`
	generation, actorVersion uint64
}
type QQStartBindingInput struct {
	Version        uint64 `json:"version"`
	Purpose        string `json:"purpose"`
	OwnerUserID    string `json:"owner_user_id,omitempty"`
	ReceiptID      string `json:"receipt_id,omitempty"`
	GroupEpochID   string `json:"group_epoch_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}
type QQConfirmBindingInput struct {
	Version            uint64 `json:"version"`
	PublicReplyConsent bool   `json:"public_reply_consent"`
}
type QQGroup struct {
	ID           string    `json:"id"`
	BotID        string    `json:"bot_id"`
	ChatID       string    `json:"chat_id"`
	AuthorizedBy string    `json:"authorized_by"`
	Version      uint64    `json:"version"`
	Generation   uint64    `json:"generation"`
	Enabled      bool      `json:"enabled"`
	CreatedAt    time.Time `json:"created_at"`
}

func qqCanUse(ctx context.Context, tx pgx.Tx, org string, a collaboration.Actor, b QQBot) error {
	if e := (conversations.Service{}).AuthorizeSpaceAction(ctx, tx, org, conversations.Actor{UserID: a.UserID, Role: a.Role, AuthorizationVersion: a.AuthorizationVersion}, b.SpaceID, b.AgentID, policy.ActionCreateObject); e != nil {
		return e
	}
	var allowed bool
	e := tx.QueryRow(ctx, `SELECT archived_at IS NULL AND status='ACTIVE' AND COALESCE(builtin_kind,'')='' AND (visibility<>'PRIVATE' OR owner_user_id=$2::uuid) AND allow_direct_chat AND NOT EXISTS(SELECT 1 FROM openfox.agent_crew_members m WHERE m.organization_id=agents.organization_id AND m.agent_id=agents.id AND m.exclusive) FROM openfox.agents WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, b.AgentID, a.UserID).Scan(&allowed)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func loadQQBindingSession(ctx context.Context, tx pgx.Tx, id string) (QQBindingSession, error) {
	var v QQBindingSession
	if !membership.ValidateUUID(id) {
		return v, ErrNotFound
	}
	e := tx.QueryRow(ctx, `SELECT s.id::text,s.bot_id::text,s.purpose,s.created_by::text,s.owner_user_id::text,s.status,s.version,s.expires_at,COALESCE(s.receipt_id::text,''),COALESCE(s.group_epoch_id::text,''),COALESCE(s.binding_id::text,''),COALESCE(p.chat_id,''),COALESCE(p.subject_id,''),COALESCE(p.member_role,''),s.generation,s.actor_version FROM openfox.qq_binding_sessions s LEFT JOIN openfox.qq_message_receipts p ON p.organization_id=s.organization_id AND p.id=s.receipt_id WHERE s.organization_id=openfox_private.current_organization_id() AND s.id=$1::uuid`, id).Scan(&v.ID, &v.BotID, &v.Purpose, &v.CreatedBy, &v.OwnerUserID, &v.Status, &v.Version, &v.ExpiresAt, &v.ReceiptID, &v.GroupEpochID, &v.BindingID, &v.ChatID, &v.SubjectID, &v.MemberRole, &v.generation, &v.actorVersion)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return v, e
}
func (s Service) GetQQBindingSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (QQBindingSession, error) {
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return QQBindingSession{}, e
	}
	v, e := loadQQBindingSession(ctx, tx, id)
	if e != nil {
		return v, e
	}
	if v.CreatedBy != a.UserID && v.OwnerUserID != a.UserID {
		return v, ErrNotFound
	}
	return v, nil
}

func (s Service) StartQQBinding(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, bot string, in QQStartBindingInput) (QQBindingSession, error) {
	var out QQBindingSession
	if in.Version == 0 || len(in.IdempotencyKey) < 16 || len(in.IdempotencyKey) > 128 {
		return out, ErrInvalidRequest
	}
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return out, e
	}
	b, e := lockQQBot(ctx, tx, bot)
	if e != nil {
		return out, e
	}
	if b.State != "ACTIVE" || b.removed {
		return out, ErrNeedsReauth
	}
	if e = qqCanUse(ctx, tx, org, a, b); e != nil {
		return out, e
	}
	if in.Purpose != "PRIVATE_CODE" && in.Purpose != "GROUP_DISCOVERY" && in.Purpose != "GROUP_ASSIGN" {
		return out, ErrInvalidRequest
	}
	owner := a.UserID
	if in.Purpose != "PRIVATE_CODE" {
		if e = qqBotManager(ctx, tx, a, b); e != nil {
			return out, e
		}
	}
	if in.Purpose == "GROUP_ASSIGN" {
		if !membership.ValidateUUID(in.OwnerUserID) || !membership.ValidateUUID(in.ReceiptID) || !membership.ValidateUUID(in.GroupEpochID) {
			return out, ErrInvalidRequest
		}
		owner = in.OwnerUserID
	} else if in.OwnerUserID != "" || in.ReceiptID != "" || in.GroupEpochID != "" {
		return out, ErrInvalidRequest
	}
	raw, _ := json.Marshal([]string{bot, in.Purpose, owner, in.ReceiptID, in.GroupEpochID})
	hash := sha256.Sum256(raw)
	var id string
	var prior []byte
	e = tx.QueryRow(ctx, `SELECT id::text,request_hash FROM openfox.qq_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND created_by=$1::uuid AND operation_key=$2`, a.UserID, in.IdempotencyKey).Scan(&id, &prior)
	if e == nil {
		if !bytes.Equal(hash[:], prior) {
			return out, ErrVersionConflict
		}
		out, e = loadQQBindingSession(ctx, tx, id)
		if e != nil {
			return out, e
		}
		return s.qqSessionCode(ctx, tx, org, out)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	if b.Version != in.Version {
		return out, ErrVersionConflict
	}
	if in.Purpose == "GROUP_ASSIGN" {
		var valid bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.qq_message_receipts p JOIN openfox.qq_group_epochs g ON g.organization_id=p.organization_id AND g.id=$2::uuid WHERE p.organization_id=openfox_private.current_organization_id() AND p.id=$1::uuid AND p.bot_id=$3::uuid AND p.scope='GROUP' AND p.event_type='GROUP_AT_MESSAGE_CREATE' AND p.generation=$4 AND p.received_at>clock_timestamp()-interval '10 minutes' AND g.bot_id=p.bot_id AND g.chat_id=p.chat_id AND g.generation=p.generation AND g.disabled_at IS NULL)`, in.ReceiptID, in.GroupEpochID, bot, b.Generation).Scan(&valid)
		if e != nil {
			return out, e
		}
		if !valid {
			return out, ErrInvalidRequest
		}
		member, e := qqActor(ctx, tx, org, collaboration.Actor{UserID: owner})
		if e != nil {
			return out, e
		}
		if e = qqCanUse(ctx, tx, org, member, b); e != nil {
			return out, e
		}
	}
	id, e = membership.NewID()
	if e != nil {
		return out, e
	}
	var code string
	var codeHash, codeSealed []byte
	var key string
	if in.Purpose != "GROUP_ASSIGN" {
		code, e = randomBase64URL(24)
		if e != nil {
			return out, e
		}
		codeHash = TokenHash(code)
		codeSealed, key, e = s.seal([]byte(code), qqAAD(org, id, "binding-code"))
		if e != nil {
			return out, e
		}
	}
	status := "PENDING"
	if in.Purpose == "GROUP_ASSIGN" {
		status = "OBSERVED"
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.qq_binding_sessions(organization_id,id,bot_id,generation,purpose,created_by,actor_version,owner_user_id,operation_key,request_hash,code_hash,code_sealed,code_key_id,receipt_id,group_epoch_id,status,expires_at) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5::uuid,$6,$7::uuid,$8,$9,$10,$11,$12,NULLIF($13,'')::uuid,NULLIF($14,'')::uuid,$15,clock_timestamp()+interval '10 minutes')`, id, bot, b.Generation, in.Purpose, a.UserID, a.AuthorizationVersion, owner, in.IdempotencyKey, hash[:], codeHash, codeSealed, key, in.ReceiptID, in.GroupEpochID, status)
	if isUniqueViolation(e) {
		return out, ErrVersionConflict
	}
	if e != nil {
		return out, e
	}
	out, e = loadQQBindingSession(ctx, tx, id)
	out.Code = code
	return out, e
}
func (s Service) qqSessionCode(ctx context.Context, tx pgx.Tx, org string, v QQBindingSession) (QQBindingSession, error) {
	if v.Status != "PENDING" || v.Purpose == "GROUP_ASSIGN" || !v.ExpiresAt.After(time.Now()) {
		return v, nil
	}
	var sealed []byte
	e := tx.QueryRow(ctx, `SELECT code_sealed FROM openfox.qq_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, v.ID).Scan(&sealed)
	if e != nil {
		return v, e
	}
	raw, e := s.open(sealed, qqAAD(org, v.ID, "binding-code"))
	if e == nil {
		v.Code = string(raw)
	}
	return v, e
}

// A signed private/group discovery command only nominates a candidate. It
// cannot authorize execution until its original OpenFox actor confirms.
func observeQQBinding(ctx context.Context, tx pgx.Tx, b QQBot, receipt, text string, sent time.Time, scope, role string) (bool, error) {
	purpose, prefix := "PRIVATE_CODE", "/openfox-bind "
	if scope == "GROUP" {
		purpose, prefix = "GROUP_DISCOVERY", "/openfox-group "
	}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, prefix) {
		return false, nil
	}
	code := strings.TrimPrefix(trimmed, prefix)
	validCode := len(code) == 32 && !strings.ContainsAny(code, " \t\r\n")
	if validCode && (scope != "GROUP" || role == "owner" || role == "admin") {
		_, e := tx.Exec(ctx, `UPDATE openfox.qq_binding_sessions SET receipt_id=$2::uuid,status='OBSERVED',version=version+1,code_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND code_hash=$3 AND purpose=$4 AND status='PENDING' AND generation=$5 AND expires_at>clock_timestamp() AND created_at<($6::timestamptz+interval '1 second')`, b.ID, receipt, TokenHash(code), purpose, b.Generation, sent)
		if e != nil {
			return true, e
		}
	}
	_, e := tx.Exec(ctx, `UPDATE openfox.qq_message_receipts SET disposition='BINDING',status_reason='confirmation_required',text='' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, receipt)
	return true, e
}

func (s Service) ConfirmQQBinding(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in QQConfirmBindingInput) (QQBindingSession, error) {
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return QQBindingSession{}, e
	}
	v, e := loadQQBindingSession(ctx, tx, id)
	if e != nil {
		return v, e
	}
	if v.OwnerUserID != a.UserID {
		return v, ErrNotFound
	}
	b, e := lockQQBot(ctx, tx, v.BotID)
	if e != nil {
		return v, e
	}
	if b.State != "ACTIVE" || b.removed || b.Generation != v.generation {
		return v, ErrNeedsReauth
	}
	if e = qqCanUse(ctx, tx, org, a, b); e != nil {
		return v, e
	}
	if v.Purpose != "PRIVATE_CODE" {
		creator, e := qqActor(ctx, tx, org, collaboration.Actor{UserID: v.CreatedBy, AuthorizationVersion: v.actorVersion})
		if e != nil {
			return v, e
		}
		if e = qqBotManager(ctx, tx, creator, b); e != nil {
			return v, e
		}
	}
	var locked string
	if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.qq_binding_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&locked); e != nil {
		return v, e
	}
	v, e = loadQQBindingSession(ctx, tx, id)
	if e != nil {
		return v, e
	}
	if v.Status == "CONFIRMED" {
		return v, nil
	}
	if v.Version != in.Version || v.Status != "OBSERVED" || !v.ExpiresAt.After(time.Now()) {
		return v, ErrVersionConflict
	}
	if v.Purpose == "PRIVATE_CODE" && a.AuthorizationVersion != v.actorVersion {
		return v, ErrForbidden
	}
	if v.Purpose == "GROUP_DISCOVERY" {
		if !in.PublicReplyConsent || (v.MemberRole != "owner" && v.MemberRole != "admin") {
			return v, ErrInvalidRequest
		}
		gid, e := membership.NewID()
		if e != nil {
			return v, e
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.qq_group_epochs(organization_id,id,bot_id,generation,chat_id,authorized_by,proof_receipt_id,public_reply_consent) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5::uuid,$6::uuid,true)`, gid, b.ID, b.Generation, v.ChatID, a.UserID, v.ReceiptID)
		if isUniqueViolation(e) {
			return v, ErrVersionConflict
		}
		if e != nil {
			return v, e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.qq_binding_sessions SET status='CONFIRMED',group_epoch_id=$2::uuid,version=version+1,finished_at=clock_timestamp(),code_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, gid)
		if e != nil {
			return v, e
		}
		return loadQQBindingSession(ctx, tx, id)
	}
	if in.PublicReplyConsent {
		return v, ErrInvalidRequest
	}
	scope := "C2C"
	if v.Purpose == "GROUP_ASSIGN" {
		scope = "GROUP"
		var current bool
		e = tx.QueryRow(ctx, `SELECT generation=$2 AND disabled_at IS NULL AND chat_id=$3 FROM openfox.qq_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, v.GroupEpochID, b.Generation, v.ChatID).Scan(&current)
		if e != nil {
			return v, e
		}
		if !current {
			return v, ErrVersionConflict
		}
	}
	binding, e := membership.NewID()
	if e != nil {
		return v, e
	}
	proof := "PRIVATE_CODE"
	if scope == "GROUP" {
		proof = "ADMIN_ASSIGNED"
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.qq_bindings(organization_id,id,bot_id,generation,group_epoch_id,scope,chat_id,subject_id,owner_user_id,proof_kind,created_by,accepted_at) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8::uuid,$9,$10::uuid,clock_timestamp())`, binding, b.ID, b.Generation, v.GroupEpochID, scope, v.ChatID, v.SubjectID, a.UserID, proof, v.CreatedBy)
	if isUniqueViolation(e) {
		return v, ErrVersionConflict
	}
	if e != nil {
		return v, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_binding_sessions SET status='CONFIRMED',binding_id=$2::uuid,version=version+1,finished_at=clock_timestamp(),code_sealed=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, binding)
	if e != nil {
		return v, e
	}
	return loadQQBindingSession(ctx, tx, id)
}
