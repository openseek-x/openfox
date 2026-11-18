package conversations

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

type OfferInput struct {
	Kind             string `json:"kind"`
	Recipient        string `json:"recipient_user_id"`
	Note             string `json:"note"`
	TransferAssignee bool   `json:"transfer_assignee"`
}
type PendingOffer struct {
	Assistant        bool   `json:"assistant,omitempty"`
	SpaceID          string `json:"space_id"`
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	ConversationID   string `json:"conversation_id"`
	Title            string `json:"title"`
	Sender           string `json:"sender"`
	Note             string `json:"note"`
	TransferAssignee bool   `json:"transfer_assignee"`
}
type OfferPeer struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

type offerContext struct {
	id, space, task, owner, visibility string
	assistant                          bool
}

func loadOfferContext(ctx context.Context, tx pgx.Tx, id string, lock bool) (offerContext, error) {
	var c offerContext
	if !membership.ValidateUUID(id) {
		return c, ErrNotFound
	}
	sql := `SELECT c.id::text,c.space_id::text,COALESCE(c.task_id::text,''),c.owner_user_id::text,c.visibility,COALESCE(a.builtin_kind='ASSISTANT',false) FROM openfox.conversations c
 JOIN openfox.agents a ON a.organization_id=c.organization_id AND a.id=c.agent_id
 LEFT JOIN openfox.tasks t ON t.organization_id=c.organization_id AND t.space_id=c.space_id AND t.id=c.task_id AND t.deleted_at IS NULL
 JOIN openfox.spaces s ON s.organization_id=c.organization_id AND s.id=c.space_id AND s.status='ACTIVE'
 WHERE c.organization_id=openfox_private.current_organization_id() AND c.id=$1 AND c.lifecycle='OPEN' AND c.archived_at IS NULL
 AND (t.id IS NOT NULL OR (c.task_id IS NULL AND a.builtin_kind='ASSISTANT'))
 AND (a.builtin_kind IS DISTINCT FROM 'ASSISTANT' OR (a.owner_user_id=c.owner_user_id AND c.crew_id IS NULL))`
	if lock {
		sql += " FOR UPDATE OF c"
	}
	err := tx.QueryRow(ctx, sql, id).Scan(&c.id, &c.space, &c.task, &c.owner, &c.visibility, &c.assistant)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}
func offerMember(ctx context.Context, tx pgx.Tx, org, id string) (Actor, error) {
	m, err := (membership.Service{}).LoadActiveActor(ctx, tx, org, id)
	if errors.Is(err, membership.ErrAuthorizationStale) {
		return Actor{}, ErrForbidden
	}
	return Actor{UserID: m.UserID, Role: m.Role, AuthorizationVersion: m.AuthorizationVersion}, err
}
func (s Service) allowOffer(ctx context.Context, tx pgx.Tx, org string, c offerContext, a Actor, kind string) error {
	if kind != "INVITE" && kind != "TRANSFER" {
		return ErrInvalidRequest
	}
	if c.task == "" && kind != "TRANSFER" {
		return ErrInvalidRequest
	}
	if c.owner != a.UserID {
		var level string
		err := tx.QueryRow(ctx, `SELECT access_level FROM openfox.conversation_members WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1 AND user_id=$2`, c.id, a.UserID).Scan(&level)
		if errors.Is(err, pgx.ErrNoRows) || c.visibility != VisibilityShared {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if kind == "TRANSFER" || level != AccessCommenter {
			return ErrForbidden
		}
	}
	return s.authorizeSpace(ctx, tx, org, a, c.space, c.id, policy.ActionUpdateObject)
}

// Apply the same space visibility predicate used for task comment recipients.
const offerPeerFrom = ` FROM openfox.organization_members om JOIN openfox.users u ON u.id=om.user_id
 JOIN openfox.spaces s ON s.organization_id=om.organization_id AND s.id=$1
 WHERE om.organization_id=openfox_private.current_organization_id() AND om.status='ACTIVE' AND om.user_id<>$2::uuid AND om.user_id<>$3::uuid AND ` + commentRecipientReadableSQL + `
 AND ($4<>'TRANSFER' OR NOT EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=om.organization_id AND sm.space_id=s.id AND sm.user_id=om.user_id AND sm.role='VIEWER'))
 AND ($4='TRANSFER' OR NOT EXISTS(SELECT 1 FROM openfox.conversation_members cm WHERE cm.organization_id=om.organization_id AND cm.conversation_id=$5 AND cm.user_id=om.user_id))`

func (s Service) OfferPeers(ctx context.Context, tx pgx.Tx, org, id string, a Actor, kind, q string) ([]OfferPeer, error) {
	if len(q) > 256 {
		return nil, ErrInvalidRequest
	}
	c, err := loadOfferContext(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	if err = s.allowOffer(ctx, tx, org, c, a, kind); err != nil {
		return nil, err
	}
	readiness := `''`
	if c.assistant && kind == "TRANSFER" {
		target := strings.ReplaceAll(assistantTransferTargetFrom, "$1", "om.user_id")
		target = strings.ReplaceAll(target, "$2", "s.id")
		readiness = `CASE WHEN EXISTS(SELECT a.id` + target + `) THEN '' ELSE 'ASSISTANT_TRANSFER_NOT_READY' END`
	}
	rows, err := tx.Query(ctx, `SELECT om.user_id::text,u.email,`+readiness+offerPeerFrom+` AND strpos(lower(u.email),lower($6))>0 ORDER BY u.email,om.user_id LIMIT 50`, c.space, a.UserID, c.owner, kind, c.id, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OfferPeer{}
	for rows.Next() {
		var p OfferPeer
		if err := rows.Scan(&p.ID, &p.Name, &p.UnavailableReason); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s Service) SendOffer(ctx context.Context, tx pgx.Tx, org, id string, a Actor, in OfferInput) (string, error) {
	if !membership.ValidateUUID(in.Recipient) || utf8.RuneCountInString(in.Note) > 1000 || in.TransferAssignee && in.Kind != "TRANSFER" {
		return "", ErrInvalidRequest
	}
	c, err := loadOfferContext(ctx, tx, id, true)
	if err != nil {
		return "", err
	}
	if err = s.allowOffer(ctx, tx, org, c, a, in.Kind); err != nil {
		return "", err
	}
	var eligible bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1"+offerPeerFrom+" AND om.user_id=$6)", c.space, a.UserID, c.owner, in.Kind, c.id, in.Recipient).Scan(&eligible); err != nil {
		return "", err
	}
	if !eligible {
		return "", ErrForbidden
	}
	if c.assistant && in.Kind == "TRANSFER" {
		if _, err := assistantTransferTarget(ctx, tx, in.Recipient, c.space); err != nil {
			return "", err
		}
	}
	if in.TransferAssignee {
		if c.task == "" {
			return "", ErrInvalidRequest
		}
		var assigned bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.task_assignees WHERE organization_id=openfox_private.current_organization_id() AND task_id=$1 AND user_id=$2)`, c.task, c.owner).Scan(&assigned)
		if err != nil {
			return "", err
		}
		if !assigned {
			return "", ErrVersionConflict
		}
	}
	var result, note string
	var flag bool
	err = tx.QueryRow(ctx, `SELECT id::text,note,transfer_assignee FROM openfox.conversation_offers WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1 AND sender_user_id=$2 AND recipient_user_id=$3 AND kind=$4 AND status='PENDING'`, id, a.UserID, in.Recipient, in.Kind).Scan(&result, &note, &flag)
	if err == nil {
		if note != strings.TrimSpace(in.Note) || flag != in.TransferAssignee {
			return "", ErrConflict
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	err = tx.QueryRow(ctx, `INSERT INTO openfox.conversation_offers(organization_id,conversation_id,sender_user_id,recipient_user_id,kind,note,transfer_assignee)
 VALUES(openfox_private.current_organization_id(),$1,$2,$3,$4,$5,$6) RETURNING id::text`, id, a.UserID, in.Recipient, in.Kind, strings.TrimSpace(in.Note), in.TransferAssignee).Scan(&result)
	return result, err
}

const pendingOfferFrom = ` FROM openfox.conversation_offers o
 JOIN openfox.users u ON u.id=o.sender_user_id
 JOIN openfox.conversations c ON c.organization_id=o.organization_id AND c.id=o.conversation_id AND c.archived_at IS NULL AND c.lifecycle='OPEN'
 JOIN openfox.agents a ON a.organization_id=c.organization_id AND a.id=c.agent_id
 LEFT JOIN openfox.tasks t ON t.organization_id=c.organization_id AND t.id=c.task_id AND t.space_id=c.space_id AND t.deleted_at IS NULL
 JOIN openfox.spaces s ON s.organization_id=c.organization_id AND s.id=c.space_id AND s.status='ACTIVE'
 JOIN openfox.organization_members om ON om.organization_id=o.organization_id AND om.user_id=o.recipient_user_id AND om.status='ACTIVE'
 WHERE o.organization_id=openfox_private.current_organization_id() AND o.recipient_user_id=openfox_private.current_actor_id() AND o.status='PENDING'
 AND (t.id IS NOT NULL OR (c.task_id IS NULL AND a.builtin_kind='ASSISTANT' AND o.kind='TRANSFER'))
 AND (o.kind<>'TRANSFER' OR c.owner_user_id=o.sender_user_id) AND ` + commentRecipientReadableSQL

// Direct chat card lookup. m is a real message, whose server-issued id matches
// the offer; the caller cannot choose a message id in the public send API.
// Re-evaluate the reader's current space permission before exposing the title or note.
const DirectMessageOfferSQL = `(SELECT jsonb_build_object('id',o.id,'kind',o.kind,'status',CASE WHEN o.status='PENDING' AND
   (c.lifecycle<>'OPEN' OR c.archived_at IS NOT NULL OR s.status<>'ACTIVE' OR (o.kind='TRANSFER' AND o.sender_user_id<>c.owner_user_id)) THEN 'UNAVAILABLE' ELSE o.status END,
   'title',coalesce(nullif(c.title,''),t.title,a.name),'note',o.note,'transfer_assignee',o.transfer_assignee,'conversation_id',c.id,
   'task_display_id',COALESCE(s.prefix||'-'||t.sequence,''),'assistant',COALESCE(a.builtin_kind='ASSISTANT',false),
   'can_open',(c.owner_user_id=openfox_private.current_actor_id() OR
   (c.visibility='SHARED' AND EXISTS(SELECT 1 FROM openfox.conversation_members cm WHERE cm.organization_id=c.organization_id AND cm.conversation_id=c.id AND cm.user_id=openfox_private.current_actor_id()))))
 FROM openfox.conversation_offers o JOIN openfox.conversations c ON c.organization_id=o.organization_id AND c.id=o.conversation_id
 JOIN openfox.agents a ON a.organization_id=c.organization_id AND a.id=c.agent_id
 LEFT JOIN openfox.tasks t ON t.organization_id=c.organization_id AND t.id=c.task_id AND t.deleted_at IS NULL
 JOIN openfox.spaces s ON s.organization_id=c.organization_id AND s.id=c.space_id
 JOIN openfox.organization_members om ON om.organization_id=o.organization_id AND om.user_id=openfox_private.current_actor_id() AND om.status='ACTIVE'
 WHERE o.organization_id=m.organization_id AND o.id=m.id AND o.sender_user_id=m.author_id
 AND (t.id IS NOT NULL OR (c.task_id IS NULL AND a.builtin_kind='ASSISTANT' AND o.kind='TRANSFER')) AND ` + commentRecipientReadableSQL + `)`

func (s Service) PendingOffers(ctx context.Context, tx pgx.Tx, org string, a Actor) ([]PendingOffer, error) {
	if err := s.CheckActor(ctx, tx, org, a); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT c.space_id::text,o.id::text,o.kind,c.id::text,coalesce(nullif(c.title,''),t.title,a.name),u.email,o.note,o.transfer_assignee,COALESCE(a.builtin_kind='ASSISTANT',false)`+pendingOfferFrom+` ORDER BY o.created_at DESC,o.id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PendingOffer{}
	for rows.Next() {
		var p PendingOffer
		if err := rows.Scan(&p.SpaceID, &p.ID, &p.Kind, &p.ConversationID, &p.Title, &p.Sender, &p.Note, &p.TransferAssignee, &p.Assistant); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s Service) DecideOffer(ctx context.Context, tx pgx.Tx, org, id string, a Actor, accept bool, requestID string) (bool, error) {
	if err := s.CheckActor(ctx, tx, org, a); err != nil {
		return false, err
	}
	if !membership.ValidateUUID(id) {
		return false, ErrNotFound
	}
	var conv, sender, kind, status string
	var withTask bool
	err := tx.QueryRow(ctx, `SELECT conversation_id::text,sender_user_id::text,kind,status,transfer_assignee FROM openfox.conversation_offers
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1 AND recipient_user_id=$2 FOR UPDATE`, id, a.UserID).Scan(&conv, &sender, &kind, &status, &withTask)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	next := "DECLINED"
	if accept {
		next = "ACCEPTED"
	}
	if status == next {
		return false, nil
	}
	if status != "PENDING" {
		return false, ErrConflict
	}
	if accept {
		c, err := loadOfferContext(ctx, tx, conv, true)
		if err != nil {
			return false, err
		}
		from, err := offerMember(ctx, tx, org, sender)
		if err != nil {
			return false, err
		}
		if err = s.allowOffer(ctx, tx, org, c, from, kind); err != nil {
			return false, err
		}
		if err = s.authorizeSpace(ctx, tx, org, a, c.space, c.id, policy.ActionReadObject); err != nil {
			return false, err
		}
		level := AccessCommenter
		if err = s.authorizeSpace(ctx, tx, org, a, c.space, c.id, policy.ActionUpdateObject); err != nil {
			if kind == "TRANSFER" || !errors.Is(err, ErrForbidden) {
				return false, err
			}
			level = AccessViewer
		}
		if c.owner == a.UserID {
			return false, ErrConflict
		}
		targetAgent := ""
		if c.assistant && kind == "TRANSFER" {
			if err := assistantTransferIdle(ctx, tx, c.id); err != nil {
				return false, err
			}
			if targetAgent, err = assistantTransferTarget(ctx, tx, a.UserID, c.space); err != nil {
				return false, err
			}
		}
		member := a.UserID
		owner := c.owner
		if kind == "TRANSFER" {
			member = c.owner
			owner = a.UserID
		}
		if withTask {
			service := collaboration.Service{}
			ca := collaboration.Actor(a)
			task, err := service.GetTask(ctx, tx, org, c.space, c.task, ca)
			if err != nil {
				return false, err
			}
			refs := []collaboration.TaskAssigneeRef{}
			found := false
			hasRecipient := false
			for _, ref := range task.Assignees {
				if ref.Kind == "HUMAN" && ref.ID == a.UserID {
					hasRecipient = true
				}
			}
			for _, ref := range task.Assignees {
				if ref.Kind == "HUMAN" && ref.ID == sender {
					found = true
					if hasRecipient {
						continue
					}
					ref.ID = a.UserID
				}
				refs = append(refs, collaboration.TaskAssigneeRef{Kind: ref.Kind, ID: ref.ID})
			}
			if !found {
				return false, ErrVersionConflict
			}
			if _, err = service.UpdateTask(ctx, tx, org, c.space, c.task, ca, collaboration.TaskUpdateInput{Version: task.Version, Assignees: &refs, RequestID: requestID}); err != nil {
				return false, err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.conversation_members(organization_id,conversation_id,user_id,access_level) VALUES(openfox_private.current_organization_id(),$1,$2,$3)
      ON CONFLICT(organization_id,conversation_id,user_id) DO UPDATE SET access_level=excluded.access_level,version=openfox.conversation_members.version+1,updated_at=clock_timestamp()`, conv, member, level)
		if err != nil {
			return false, err
		}
		update := `UPDATE openfox.conversations SET owner_user_id=$3,visibility='SHARED',version=version+1,updated_at=clock_timestamp()`
		args := []any{conv, c.owner, owner}
		if targetAgent != "" {
			update += `,agent_id=$4,workspace_selection=NULL,binding_id=NULL,runtime_override=NULL,model_override=NULL,
 approval_mode_override=NULL,reasoning_effort_override=NULL,overrides_version=overrides_version+1`
			args = append(args, targetAgent)
		}
		tag, err := tx.Exec(ctx, update+` WHERE organization_id=openfox_private.current_organization_id() AND id=$1 AND owner_user_id=$2`, args...)
		if err != nil {
			return false, err
		}
		if tag.RowsAffected() != 1 {
			return false, ErrVersionConflict
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.conversation_offers SET status=$2,decided_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1 AND status='PENDING'`, id, next)
	if err == nil && tag.RowsAffected() != 1 {
		return false, ErrVersionConflict
	}
	return err == nil, err
}
