// Package sharesnapshots 实现阶段 12g 的分享快照：对话或单条消息在创建时刻的不可变文本副本。
// 快照不引用来源对象；读取只通过令牌哈希，所有失败条件对外统一表现为“不存在”。
package sharesnapshots

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
	"openfox/internal/membership"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/secrets"
)

const (
	KindConversation = "CONVERSATION"
	KindComment      = "COMMENT"
	ScopePublic      = "PUBLIC"
	ScopeOrg         = "ORGANIZATION"

	// 过期选项：7 天、30 天、永不；默认 30 天。
	Expiry7d      = "7d"
	Expiry30d     = "30d"
	ExpiryNever   = "never"
	DefaultExpiry = Expiry30d

	// MaxContentBytes 是单个快照序列化后的内容上限，低于表约束（262144），给 jsonb 文本格式差异留余量。
	MaxContentBytes = 200 * 1024
	// MaxTitleRunes 与表约束一致。
	MaxTitleRunes = 200
	// MaxActivePerUser / MaxActivePerOrg 限制有效（未撤销且未过期）快照数量，防止无限堆积公开内容。
	MaxActivePerUser = 100
	MaxActivePerOrg  = 1000

	listLimit = 200
	// 对话里没有显示名时的创建人显示名，绝不回退到邮箱。
	anonymousName = "Member"
)

var (
	ErrNotFound       = errors.New("sharesnapshots: not found")
	ErrInvalidRequest = errors.New("sharesnapshots: invalid request")
	ErrPublicDisabled = errors.New("sharesnapshots: public sharing is disabled")
	ErrTooLarge       = errors.New("sharesnapshots: content is too large")
	ErrQuotaExceeded  = errors.New("sharesnapshots: too many active snapshots")
	ErrSourceEmpty    = errors.New("sharesnapshots: source has nothing to share")
)

// RedactionError 表示脱敏检查命中：阻止创建并返回位置，不返回命中原文。
type RedactionError struct{ Findings []Finding }

func (e *RedactionError) Error() string { return "sharesnapshots: sensitive content detected" }

type Service struct {
	Box           *secrets.Box
	Conversations conversations.Service
	// Now 便于测试注入；为空使用 time.Now。
	Now func() time.Time
	// Pool 只用于匿名读取（Resolve）。
	Pool *postgres.Pool
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Item 是快照里的一条消息。ReplyTo 是线程内的 1 基序号，0 表示不是回复。
type Item struct {
	Author     string    `json:"author"`
	AuthorKind string    `json:"author_kind"`
	Body       string    `json:"body"`
	At         time.Time `json:"at"`
	ReplyTo    int       `json:"reply_to,omitempty"`
}

type content struct {
	Items []Item `json:"items"`
}

type CreateInput struct {
	Kind  string
	Scope string
	// 会话快照
	ConversationID string
	// MessageID 空时复制整个对话；否则只取选中的一条可读文本消息。
	MessageID string
	SpaceID   string
	TaskID    string
	CommentID string
	// Title 为空时取来源标题或首条正文摘要。
	Title string
	// Expiry 取 7d / 30d / never，空表示默认。
	Expiry string
}

// Meta 是列表与创建响应里的元数据，不含内容与令牌哈希。
type Meta struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Scope       string     `json:"scope"`
	Title       string     `json:"title"`
	ItemCount   int        `json:"item_count"`
	CreatedBy   string     `json:"created_by"`
	CreatorName string     `json:"creator_name"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
	// Status：ACTIVE / EXPIRED / REVOKED。
	Status string `json:"status"`
}

// Created 的令牌仅在创建或创建者恢复响应返回，不写入日志或明文持久化。
type Created struct {
	Meta
	Token string `json:"token"`
}

// View 是访问者看到的快照。
type View struct {
	ID               string     `json:"id"`
	Kind             string     `json:"kind"`
	Scope            string     `json:"scope"`
	Title            string     `json:"title"`
	Items            []Item     `json:"items"`
	CreatorName      string     `json:"creator_name"`
	OrganizationName string     `json:"organization_name"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        *time.Time `json:"expires_at"`
}

// NewToken 生成 32 字节随机令牌，返回明文与存库的 SHA-256 哈希。
func NewToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate share token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func expiryFor(option string, now time.Time) (*time.Time, error) {
	switch option {
	case "", Expiry30d:
		t := now.Add(30 * 24 * time.Hour)
		return &t, nil
	case Expiry7d:
		t := now.Add(7 * 24 * time.Hour)
		return &t, nil
	case ExpiryNever:
		return nil, nil
	}
	return nil, ErrInvalidRequest
}

func canModerate(role policy.OrganizationRole) bool {
	return role == policy.OrganizationRoleOwner || role == policy.OrganizationRoleAdmin
}

// Create 在租户事务里创建快照。读权限、脱敏、大小与数量上限都在写入前判定。
func (s Service) Create(ctx context.Context, tx pgx.Tx, organizationID string, actor conversations.Actor, input CreateInput) (Created, error) {
	if err := s.Conversations.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Created{}, err
	}
	if input.Scope != ScopePublic && input.Scope != ScopeOrg {
		return Created{}, ErrInvalidRequest
	}
	if s.Box == nil {
		return Created{}, secrets.ErrUnavailable
	}
	now := s.now()
	expiresAt, err := expiryFor(input.Expiry, now)
	if err != nil {
		return Created{}, err
	}
	// 先读来源：没有读权限的人拿到的是与“不存在”相同的错误，不会通过公开开关状态探测来源。
	var source conversations.ShareSource
	switch input.Kind {
	case KindConversation:
		source, err = s.Conversations.ConversationShareSource(ctx, tx, organizationID, input.ConversationID, actor, input.MessageID)
	case KindComment:
		source, err = s.Conversations.CommentThreadShareSource(ctx, tx, organizationID, input.SpaceID, input.TaskID, input.CommentID, actor)
	default:
		return Created{}, ErrInvalidRequest
	}
	if errors.Is(err, conversations.ErrShareSourceEmpty) {
		return Created{}, ErrSourceEmpty
	}
	if errors.Is(err, conversations.ErrShareSourceTooLarge) {
		return Created{}, ErrTooLarge
	}
	if err != nil {
		return Created{}, err
	}
	if input.Scope == ScopePublic {
		var allowed bool
		if err := tx.QueryRow(ctx, `
			SELECT allow_public_sharing FROM openfox.organizations
			 WHERE id = openfox_private.current_organization_id() AND deleted_at IS NULL FOR SHARE`).Scan(&allowed); err != nil {
			return Created{}, err
		}
		if !allowed {
			return Created{}, ErrPublicDisabled
		}
	}

	items, title := buildContent(source, input.Title)
	var findings []Finding
	findings = append(findings, Scan(0, title)...)
	for index, item := range items {
		findings = append(findings, Scan(index+1, item.Body)...)
		findings = append(findings, Scan(index+1, item.Author)...)
	}
	if len(findings) > 0 {
		if len(findings) > maxFindings {
			findings = findings[:maxFindings]
		}
		return Created{}, &RedactionError{Findings: findings}
	}
	encoded, err := json.Marshal(content{Items: items})
	if err != nil {
		return Created{}, err
	}
	if len(encoded) > MaxContentBytes {
		return Created{}, ErrTooLarge
	}

	// 数量上限：同组织串行化计数，避免并发创建绕过上限。
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('share_snapshots:' || openfox_private.current_organization_id()::text, 0))`); err != nil {
		return Created{}, err
	}
	var perUser, perOrg int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE created_by = $1::uuid), count(*)
		  FROM openfox.share_snapshots
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > $2)`, actor.UserID, now).Scan(&perUser, &perOrg); err != nil {
		return Created{}, err
	}
	if perUser >= MaxActivePerUser || perOrg >= MaxActivePerOrg {
		return Created{}, ErrQuotaExceeded
	}

	var creatorName string
	if err := tx.QueryRow(ctx, `SELECT btrim(full_name) FROM openfox.users WHERE id = $1::uuid`, actor.UserID).Scan(&creatorName); err != nil {
		return Created{}, err
	}
	if creatorName == "" {
		creatorName = anonymousName
	}
	token, hash, err := NewToken()
	if err != nil {
		return Created{}, err
	}
	id, err := membership.NewID()
	if err != nil {
		return Created{}, err
	}
	sealed, err := s.Box.Seal([]byte(token), tokenAAD(organizationID, id, actor.UserID))
	if err != nil {
		return Created{}, err
	}
	var meta Meta
	err = tx.QueryRow(ctx, `
		INSERT INTO openfox.share_snapshots (organization_id, id, kind, scope, title, content, item_count, creator_name,
		         token_hash, created_by, created_at, expires_at, token_ciphertext, token_nonce, token_key_id)
		VALUES (openfox_private.current_organization_id(), $11::uuid, $1, $2, $3, $4::jsonb, $5, $6, $7, $8::uuid, $9, $10, $12, $13, $14)
		RETURNING id::text, kind, scope, title, item_count, created_by::text, creator_name, created_at, expires_at, revoked_at`,
		input.Kind, input.Scope, title, string(encoded), len(items), creatorName, hash, actor.UserID, now, expiresAt, id, sealed.Ciphertext, sealed.Nonce, sealed.KeyID).Scan(
		&meta.ID, &meta.Kind, &meta.Scope, &meta.Title, &meta.ItemCount, &meta.CreatedBy, &meta.CreatorName,
		&meta.CreatedAt, &meta.ExpiresAt, &meta.RevokedAt)
	if err != nil {
		return Created{}, err
	}
	meta.Status = statusOf(meta, now)
	return Created{Meta: meta, Token: token}, nil
}

// buildContent 把来源转成快照条目并确定标题；回复关系换算成线程内 1 基序号。
func buildContent(source conversations.ShareSource, requested string) ([]Item, string) {
	index := make(map[string]int, len(source.Items))
	items := make([]Item, 0, len(source.Items))
	for position, item := range source.Items {
		index[item.ID] = position + 1
		entry := Item{Author: item.AuthorName, AuthorKind: item.AuthorKind, Body: item.Body, At: item.CreatedAt.UTC()}
		if item.ReplyToID != "" {
			entry.ReplyTo = index[item.ReplyToID]
		}
		items = append(items, entry)
	}
	title := strings.TrimSpace(requested)
	if title == "" {
		title = strings.TrimSpace(source.Title)
	}
	if title == "" && len(items) > 0 {
		title = strings.Join(strings.Fields(items[0].Body), " ")
	}
	if title == "" {
		title = "Shared snapshot"
	}
	if utf8.RuneCountInString(title) > MaxTitleRunes {
		title = string([]rune(title)[:MaxTitleRunes])
	}
	return items, title
}

func statusOf(meta Meta, now time.Time) string {
	switch {
	case meta.RevokedAt != nil:
		return "REVOKED"
	case meta.ExpiresAt != nil && !meta.ExpiresAt.After(now):
		return "EXPIRED"
	default:
		return "ACTIVE"
	}
}

// List 返回快照元数据：普通成员只看自己创建的，Owner/Admin 看全组织。不含内容与令牌。
func (s Service) List(ctx context.Context, tx pgx.Tx, organizationID string, actor conversations.Actor) ([]Meta, error) {
	if err := s.Conversations.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, kind, scope, title, item_count, created_by::text, creator_name, created_at, expires_at, revoked_at
		  FROM openfox.share_snapshots
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND ($2 OR created_by = $1::uuid)
		 ORDER BY created_at DESC, id DESC
		 LIMIT $3`, actor.UserID, canModerate(actor.Role), listLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.now()
	items := []Meta{}
	for rows.Next() {
		var meta Meta
		if err := rows.Scan(&meta.ID, &meta.Kind, &meta.Scope, &meta.Title, &meta.ItemCount, &meta.CreatedBy,
			&meta.CreatorName, &meta.CreatedAt, &meta.ExpiresAt, &meta.RevokedAt); err != nil {
			return nil, err
		}
		meta.Status = statusOf(meta, now)
		items = append(items, meta)
	}
	return items, rows.Err()
}

// Revoke 撤销快照，立即生效且不可恢复。创建人与 Owner/Admin 可撤销；其他人（含看不到该行的人）一律 ErrNotFound。
// 幂等：已撤销再撤销返回成功且 changed=false；并发撤销由行锁串行化，只有第一个返回 changed=true（用于只写一条审计）。
func (s Service) Revoke(ctx context.Context, tx pgx.Tx, organizationID string, actor conversations.Actor, id string) (Meta, bool, error) {
	if err := s.Conversations.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Meta{}, false, err
	}
	if !membership.ValidateUUID(id) {
		return Meta{}, false, ErrNotFound
	}
	var meta Meta
	err := tx.QueryRow(ctx, `
		SELECT id::text, kind, scope, title, item_count, created_by::text, creator_name, created_at, expires_at, revoked_at
		  FROM openfox.share_snapshots
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		 FOR UPDATE`, id).Scan(&meta.ID, &meta.Kind, &meta.Scope, &meta.Title, &meta.ItemCount, &meta.CreatedBy,
		&meta.CreatorName, &meta.CreatedAt, &meta.ExpiresAt, &meta.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Meta{}, false, ErrNotFound
	}
	if err != nil {
		return Meta{}, false, err
	}
	if meta.CreatedBy != actor.UserID && !canModerate(actor.Role) {
		return Meta{}, false, ErrNotFound
	}
	now := s.now()
	if meta.RevokedAt != nil {
		meta.Status = "REVOKED"
		return meta, false, nil
	}
	if err := tx.QueryRow(ctx, `
		UPDATE openfox.share_snapshots SET revoked_at = clock_timestamp(), revoked_by = $2::uuid
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND revoked_at IS NULL
		RETURNING revoked_at`, id, actor.UserID).Scan(&meta.RevokedAt); err != nil {
		return Meta{}, false, err
	}
	meta.Status = statusOf(meta, now)
	return meta, true, nil
}

// Resolve 凭明文令牌读取快照。userID 为空表示匿名访问者。
// 令牌不存在、已撤销、已过期、范围不匹配、公开分享被关闭一律 ErrNotFound，不区分原因。
func (s Service) Resolve(ctx context.Context, userID, token string) (View, error) {
	if s.Pool == nil {
		return View{}, postgres.ErrNilPool
	}
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 128 {
		return View{}, ErrNotFound
	}
	var user any
	if userID != "" {
		user = userID
	}
	var view View
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var raw []byte
		var itemCount int
		err := tx.QueryRow(ctx, `
			SELECT id::text, kind, scope, title, content, item_count, creator_name, organization_name, created_at, expires_at
			  FROM openfox_private.resolve_share_snapshot($1::bytea, $2::uuid)`, HashToken(token), user).Scan(
			&view.ID, &view.Kind, &view.Scope, &view.Title, &raw, &itemCount, &view.CreatorName,
			&view.OrganizationName, &view.CreatedAt, &view.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var decoded content
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return err
		}
		view.Items = decoded.Items
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return view, nil
}

// tokenAAD binds ciphertext to its purpose, tenant, immutable snapshot and creator.
func tokenAAD(org, id, creator string) string {
	return "share-snapshot-token-v1\x00" + strings.ToLower(org) + "\x00" + strings.ToLower(id) + "\x00" + strings.ToLower(creator)
}

// Recover never creates or extends a link. Only its active creator can recover
// an active snapshot; ownership of the organization does not grant token access.
// Source permissions are intentionally not rechecked: the approved snapshot is independent.
func (s Service) Recover(ctx context.Context, tx pgx.Tx, organizationID string, actor conversations.Actor, id string) (Created, error) {
	if err := s.Conversations.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Created{}, err
	}
	if !membership.ValidateUUID(id) {
		return Created{}, ErrNotFound
	}
	var result Created
	var sealed secrets.Sealed
	err := tx.QueryRow(ctx, `
 SELECT s.id::text,s.kind,s.scope,s.title,s.item_count,s.created_by::text,s.creator_name,s.created_at,s.expires_at,s.revoked_at,
        s.token_ciphertext,s.token_nonce,s.token_key_id
 FROM openfox.share_snapshots s JOIN openfox.organizations o ON o.id=s.organization_id
 WHERE s.organization_id=openfox_private.current_organization_id() AND s.id=$1::uuid AND s.created_by=$2::uuid
   AND s.revoked_at IS NULL AND (s.expires_at IS NULL OR s.expires_at>clock_timestamp())
   AND o.deleted_at IS NULL AND (s.scope='ORGANIZATION' OR o.allow_public_sharing)
 FOR SHARE OF s,o`, id, actor.UserID).Scan(&result.ID, &result.Kind, &result.Scope, &result.Title, &result.ItemCount, &result.CreatedBy, &result.CreatorName, &result.CreatedAt, &result.ExpiresAt, &result.RevokedAt, &sealed.Ciphertext, &sealed.Nonce, &sealed.KeyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Created{}, ErrNotFound
	}
	if err != nil {
		return Created{}, err
	}
	plain, err := s.Box.Open(sealed, tokenAAD(organizationID, id, actor.UserID))
	if err != nil {
		return Created{}, err
	}
	result.Token = string(plain)
	result.Status = "ACTIVE"
	return result, nil
}
