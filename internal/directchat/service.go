// Package directchat handles private human conversations, separate from Agent execution.
package directchat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/attachments"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/membership"
)

type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Chat struct {
	LastOfferKind string    `json:"last_offer_kind,omitempty"`
	ID            string    `json:"id"`
	Peer          Person    `json:"peer"`
	LastSeq       int64     `json:"last_seq"`
	ReadSeq       int64     `json:"read_seq"`
	UnreadCount   int64     `json:"unread_count"`
	LastBody      string    `json:"last_body"`
	UpdatedAt     time.Time `json:"updated_at"`
}
type Message struct {
	Offer       json.RawMessage `json:"offer,omitempty"`
	ID          string          `json:"id"`
	Seq         int64           `json:"seq"`
	AuthorID    string          `json:"author_id"`
	Body        string          `json:"body"`
	CreatedAt   time.Time       `json:"created_at"`
	Attachments []File          `json:"attachments"`
}
type File struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Previewable bool   `json:"previewable"`
	Key         string `json:"-"`
}
type SendInput struct {
	offerID         string   // only set by DeliverOffer; never decoded from a client
	Body            string   `json:"body"`
	ClientMessageID string   `json:"client_message_id"`
	AttachmentIDs   []string `json:"attachment_ids"`
}

var ErrSelf = collaboration.NewRuleError(http.StatusBadRequest, "DIRECT_CHAT_SELF", "cannot start a conversation with yourself")
var ErrReplay = collaboration.NewRuleError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", "client_message_id was already used with different content")

// Authorize also locks active membership rows for writes: revocation cannot race a send.
func Authorize(ctx context.Context, tx pgx.Tx, id string, lock bool) error {
	if !membership.ValidateUUID(id) {
		return conversations.ErrInvalidRequest
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF c"
	}
	var low, high string
	err := tx.QueryRow(ctx, `SELECT c.user_low::text,c.user_high::text FROM openfox.direct_chats c WHERE c.organization_id=openfox_private.current_organization_id() AND c.id=$1::uuid`+suffix, id).Scan(&low, &high)
	if errors.Is(err, pgx.ErrNoRows) {
		return conversations.ErrNotFound
	}
	if err != nil {
		return err
	}
	return activePair(ctx, tx, low, high, lock)
}
func activePair(ctx context.Context, tx pgx.Tx, low, high string, lock bool) error {
	suffix := ""
	if lock {
		suffix = " FOR SHARE"
	}
	rows, err := tx.Query(ctx, `SELECT user_id FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id IN ($1::uuid,$2::uuid) AND status='ACTIVE' ORDER BY user_id`+suffix, low, high)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if n != 2 {
		return conversations.ErrNotFound
	}
	return nil
}
func Ensure(ctx context.Context, tx pgx.Tx, actor, peer string) (Chat, error) {
	if !membership.ValidateUUID(peer) {
		return Chat{}, conversations.ErrInvalidRequest
	}
	peer = strings.ToLower(peer)
	if actor == peer {
		return Chat{}, ErrSelf
	}
	low, high := actor, peer
	if low > high {
		low, high = high, low
	}
	if err := activePair(ctx, tx, low, high, true); err != nil {
		return Chat{}, err
	}
	// Atomic pair uniqueness handles reverse-direction and concurrent creation without duplicate chats.
	_, err := tx.Exec(ctx, `INSERT INTO openfox.direct_chats(organization_id,user_low,user_high) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid) ON CONFLICT(organization_id,user_low,user_high) DO NOTHING`, low, high)
	if err != nil {
		return Chat{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.direct_chats WHERE organization_id=openfox_private.current_organization_id() AND user_low=$1::uuid AND user_high=$2::uuid`, low, high).Scan(&id)
	if err != nil {
		return Chat{}, err
	}
	return Get(ctx, tx, id)
}

const chatSelect = `SELECT c.id::text,u.id::text,u.email,c.last_seq,
 CASE WHEN c.user_low=openfox_private.current_actor_id() THEN c.read_low ELSE c.read_high END,
 (SELECT count(*) FROM openfox.direct_messages m WHERE m.organization_id=c.organization_id AND m.chat_id=c.id AND m.author_id<>openfox_private.current_actor_id() AND m.seq>CASE WHEN c.user_low=openfox_private.current_actor_id() THEN c.read_low ELSE c.read_high END),
 COALESCE(left(last.body,200),''),coalesce(o.kind,''),c.updated_at
 FROM openfox.direct_chats c LEFT JOIN openfox.direct_messages last ON last.organization_id=c.organization_id AND last.chat_id=c.id AND last.seq=c.last_seq
 LEFT JOIN openfox.conversation_offers o ON o.organization_id=c.organization_id AND o.id=last.id AND o.sender_user_id=last.author_id
 JOIN openfox.users u ON u.id=CASE WHEN c.user_low=openfox_private.current_actor_id() THEN c.user_high ELSE c.user_low END
 JOIN openfox.organization_members om ON om.organization_id=c.organization_id AND om.user_id=u.id AND om.status='ACTIVE'
 WHERE c.organization_id=openfox_private.current_organization_id()`

func scanChat(row pgx.Row) (c Chat, err error) {
	err = row.Scan(&c.ID, &c.Peer.ID, &c.Peer.Name, &c.LastSeq, &c.ReadSeq, &c.UnreadCount, &c.LastBody, &c.LastOfferKind, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = conversations.ErrNotFound
	}
	return
}
func Get(ctx context.Context, tx pgx.Tx, id string) (Chat, error) {
	if err := Authorize(ctx, tx, id, false); err != nil {
		return Chat{}, err
	}
	return scanChat(tx.QueryRow(ctx, chatSelect+` AND c.id=$1::uuid`, id))
}
func List(ctx context.Context, tx pgx.Tx, before string, limit int) ([]Chat, string, error) {
	if limit < 1 || limit > 100 {
		return nil, "", conversations.ErrInvalidRequest
	}
	var ts time.Time
	var id string
	if before != "" {
		parts := strings.Split(before, "|")
		if len(parts) != 2 || !membership.ValidateUUID(parts[1]) {
			return nil, "", conversations.ErrInvalidRequest
		}
		var err error
		ts, err = time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			return nil, "", conversations.ErrInvalidRequest
		}
		id = parts[1]
	}
	if id == "" {
		id = "00000000-0000-0000-0000-000000000000"
	}
	rows, err := tx.Query(ctx, chatSelect+` AND c.last_seq>0 AND ($1 OR (c.updated_at,c.id)<($2,$3::uuid)) ORDER BY c.updated_at DESC,c.id DESC LIMIT $4`, before == "", ts, id, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := []Chat{}
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, "", err
		}
		items = append(items, c)
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		c := items[len(items)-1]
		next = c.UpdatedAt.Format(time.RFC3339Nano) + "|" + c.ID
	}
	return items, next, rows.Err()
}
func Messages(ctx context.Context, tx pgx.Tx, id string, before int64, limit int) ([]Message, int64, error) {
	return MessageWindow(ctx, tx, id, before, 0, "", limit)
}

// MessageWindow reads a page after checking membership in this exact chat.
func MessageWindow(ctx context.Context, tx pgx.Tx, id string, before, after int64, around string, limit int) ([]Message, int64, error) {
	if before < 0 || after < 0 || limit < 1 || limit > 100 || (after != 0 && before != 0) || (around != "" && (before != 0 || after != 0 || !membership.ValidateUUID(around))) {
		return nil, 0, conversations.ErrInvalidRequest
	}
	if err := Authorize(ctx, tx, id, false); err != nil {
		return nil, 0, err
	}
	if around != "" {
		err := tx.QueryRow(ctx, `SELECT seq + $3::bigint FROM openfox.direct_messages WHERE organization_id=openfox_private.current_organization_id()
			AND chat_id=$1::uuid AND id=$2::uuid`, id, around, (limit+1)/2).Scan(&before)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, conversations.ErrNotFound
		}
		if err != nil {
			return nil, 0, err
		}
	}
	condition, order, bound := "($2::bigint=0 OR m.seq<$2)", "m.seq DESC", before
	if after != 0 {
		condition, order, bound = "m.seq>$2::bigint", "m.seq ASC", after
	}
	rows, err := tx.Query(ctx, `SELECT m.id::text,m.seq,m.author_id::text,m.body,m.created_at,`+conversations.DirectMessageOfferSQL+`,
 COALESCE((SELECT jsonb_agg(jsonb_build_object('id',a.id,'name',a.name,'content_type',a.content_type,'size_bytes',a.size_bytes) ORDER BY a.created_at,a.id) FROM openfox.direct_attachments a WHERE a.organization_id=m.organization_id AND a.message_id=m.id),'[]')
 FROM openfox.direct_messages m WHERE m.organization_id=openfox_private.current_organization_id() AND m.chat_id=$1::uuid AND `+condition+` ORDER BY `+order+` LIMIT $3`, id, bound, limit+1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []Message{}
	for rows.Next() {
		var m Message
		var data []byte
		if err = rows.Scan(&m.ID, &m.Seq, &m.AuthorID, &m.Body, &m.CreatedAt, &m.Offer, &data); err != nil {
			return nil, 0, err
		}
		if err = json.Unmarshal(data, &m.Attachments); err != nil {
			return nil, 0, err
		}
		for i := range m.Attachments {
			m.Attachments[i].Previewable = attachments.Previewable(m.Attachments[i].ContentType)
		}
		items = append(items, m)
	}
	next := int64(0)
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].Seq
	}
	if after == 0 {
		slices.Reverse(items)
	}
	return items, next, rows.Err()
}
func Send(ctx context.Context, tx pgx.Tx, id, actor string, in SendInput) (Message, error) {
	in.Body = strings.TrimSpace(in.Body)
	if !membership.ValidateUUID(in.ClientMessageID) || utf8.RuneCountInString(in.Body) > 65536 || len(in.AttachmentIDs) > attachments.MaxPerMessage || (in.Body == "" && len(in.AttachmentIDs) == 0) {
		return Message{}, conversations.ErrInvalidRequest
	}
	for i, v := range in.AttachmentIDs {
		if !membership.ValidateUUID(v) {
			return Message{}, conversations.ErrInvalidRequest
		}
		in.AttachmentIDs[i] = strings.ToLower(v)
	}
	slices.Sort(in.AttachmentIDs)
	for i := 1; i < len(in.AttachmentIDs); i++ {
		if in.AttachmentIDs[i] == in.AttachmentIDs[i-1] {
			return Message{}, conversations.ErrInvalidRequest
		}
	}
	if len(in.AttachmentIDs) == 0 {
		in.AttachmentIDs = []string{}
	}
	data, _ := json.Marshal(struct {
		Body string
		IDs  []string
	}{in.Body, in.AttachmentIDs})
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	if err := Authorize(ctx, tx, id, true); err != nil {
		return Message{}, err
	}
	var oldHash string
	var seq int64
	err := tx.QueryRow(ctx, `SELECT seq,request_hash FROM openfox.direct_messages WHERE organization_id=openfox_private.current_organization_id() AND chat_id=$1::uuid AND author_id=$2::uuid AND client_message_id=$3::uuid`, id, actor, in.ClientMessageID).Scan(&seq, &oldHash)
	if err == nil {
		if oldHash != hash {
			return Message{}, ErrReplay
		}
		ms, _, err := Messages(ctx, tx, id, seq+1, 1)
		if err != nil {
			return Message{}, err
		}
		return ms[0], nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Message{}, err
	}
	// Attachments must still be pending and owned by this sender in this chat.
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.direct_attachments WHERE organization_id=openfox_private.current_organization_id() AND chat_id=$1::uuid AND uploaded_by=$2::uuid AND message_id IS NULL AND id=ANY($3::uuid[])`, id, actor, in.AttachmentIDs).Scan(&count)
	if err != nil {
		return Message{}, err
	}
	if count != len(in.AttachmentIDs) {
		return Message{}, attachments.ErrBadMessage
	}
	err = tx.QueryRow(ctx, `UPDATE openfox.direct_chats SET last_seq=last_seq+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid RETURNING last_seq`, id).Scan(&seq)
	if err != nil {
		return Message{}, err
	}
	var mid string
	err = tx.QueryRow(ctx, `INSERT INTO openfox.direct_messages(organization_id,chat_id,seq,author_id,client_message_id,body,request_hash,id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3::uuid,$4::uuid,$5,$6,coalesce(nullif($7,'')::uuid,gen_random_uuid())) RETURNING id::text`, id, seq, actor, in.ClientMessageID, in.Body, hash, in.offerID).Scan(&mid)
	if err != nil {
		return Message{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.direct_attachments SET message_id=$1::uuid WHERE organization_id=openfox_private.current_organization_id() AND id=ANY($2::uuid[])`, mid, in.AttachmentIDs)
	if err != nil {
		return Message{}, err
	}
	if err = emit(ctx, tx, id, ""); err != nil {
		return Message{}, err
	}
	ms, _, err := Messages(ctx, tx, id, seq+1, 1)
	if err != nil {
		return Message{}, err
	}
	return ms[0], nil
}
func MarkRead(ctx context.Context, tx pgx.Tx, id, actor string, seq int64) error {
	if seq < 0 {
		return conversations.ErrInvalidRequest
	}
	if err := Authorize(ctx, tx, id, true); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.direct_chats SET read_low=CASE WHEN user_low=$2::uuid THEN GREATEST(read_low,LEAST(last_seq,$3)) ELSE read_low END,read_high=CASE WHEN user_high=$2::uuid THEN GREATEST(read_high,LEAST(last_seq,$3)) ELSE read_high END WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND CASE WHEN user_low=$2::uuid THEN read_low ELSE read_high END < LEAST(last_seq,$3)`, id, actor, seq)
	if err == nil && tag.RowsAffected() > 0 {
		err = emit(ctx, tx, id, actor)
	}
	return err
}
func emit(ctx context.Context, tx pgx.Tx, id, recipient string) error {
	_, err := tx.Exec(ctx, `INSERT INTO openfox.organization_events(organization_id,type,entity,entity_id,recipient_user_id,payload)
 SELECT organization_id,'direct_chat.changed','direct_chat',id,recipient,jsonb_build_object('chat_id',id) FROM openfox.direct_chats CROSS JOIN LATERAL unnest(ARRAY[user_low,user_high]) recipient WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND ($2='' OR recipient::text=$2)`, id, recipient)
	return err
}
func Upload(ctx context.Context, tx pgx.Tx, id, actor, fid, name string, info attachments.FileInfo) (File, error) {
	if err := Authorize(ctx, tx, id, true); err != nil {
		return File{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.direct_attachments WHERE organization_id=openfox_private.current_organization_id() AND chat_id=$1::uuid AND uploaded_by=$2::uuid AND message_id IS NULL`, id, actor).Scan(&count); err != nil {
		return File{}, err
	}
	if count >= attachments.MaxPendingPerUser {
		return File{}, attachments.ErrPendingLimit
	}
	_, err := tx.Exec(ctx, `INSERT INTO openfox.direct_attachments(organization_id,id,chat_id,uploaded_by,name,file_key,content_type,size_bytes,sha256) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8)`, fid, id, actor, name, info.Key, info.ContentType, info.SizeBytes, info.SHA256)
	return File{ID: fid, Name: name, ContentType: info.ContentType, SizeBytes: info.SizeBytes, Previewable: attachments.Previewable(info.ContentType)}, err
}
func LoadFile(ctx context.Context, tx pgx.Tx, id, fid, actor string, remove bool) (File, error) {
	if !membership.ValidateUUID(fid) {
		return File{}, conversations.ErrInvalidRequest
	}
	if err := Authorize(ctx, tx, id, remove); err != nil {
		return File{}, err
	}
	var f File
	var pending bool
	var owner string
	err := tx.QueryRow(ctx, `SELECT id::text,name,file_key,content_type,size_bytes,message_id IS NULL,uploaded_by::text FROM openfox.direct_attachments WHERE organization_id=openfox_private.current_organization_id() AND chat_id=$1::uuid AND id=$2::uuid`, id, fid).Scan(&f.ID, &f.Name, &f.Key, &f.ContentType, &f.SizeBytes, &pending, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, conversations.ErrNotFound
	}
	if err != nil {
		return f, err
	}
	if remove {
		if !pending || owner != actor {
			return f, conversations.ErrForbidden
		}
		_, err = tx.Exec(ctx, `DELETE FROM openfox.direct_attachments WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, fid)
	}
	f.Previewable = attachments.Previewable(f.ContentType)
	return f, err
}

// DeliverOffer binds a real private message to the immutable, server-issued offer id.
// The chat, card, event, and offer all commit together. Retried offers reuse the card.
func DeliverOffer(ctx context.Context, tx pgx.Tx, sender, recipient, kind, id string) error {
	chat, err := Ensure(ctx, tx, sender, recipient)
	if err != nil {
		return err
	}
	body := "[对话邀请]"
	if kind == "TRANSFER" {
		body = "[转让对话]"
	}
	_, err = Send(ctx, tx, chat.ID, sender, SendInput{offerID: id, ClientMessageID: id, Body: body})
	return err
}
func OfferChanged(ctx context.Context, tx pgx.Tx, offerID string) error {
	var chat string
	err := tx.QueryRow(ctx, `SELECT chat_id::text FROM openfox.direct_messages WHERE organization_id=openfox_private.current_organization_id() AND id=$1`, offerID).Scan(&chat)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return emit(ctx, tx, chat, "")
}
