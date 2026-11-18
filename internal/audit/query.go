package audit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 阶段 12e：组织级审计查询（只读）。调用方负责鉴权（仅组织 Owner/Admin）并在组织租户事务内调用；
// 这里再次限定 organization_id，并只返回审计表里的非敏感字段。

const (
	// DefaultPageSize / MaxPageSize 是审计列表每页条数的默认值与上限。
	DefaultPageSize = 50
	MaxPageSize     = 100
	// MaxQuerySpan 是同时给出起止时间时允许的最大跨度。
	MaxQuerySpan = 366 * 24 * time.Hour
)

// ErrInvalidQuery 表示筛选条件、时间范围或游标不合法。
var ErrInvalidQuery = errors.New("invalid audit query")

// Query 是审计列表的筛选条件。时间范围左闭右开：From <= created_at < To。
type Query struct {
	OrganizationID string
	// ActorID 为空表示不限操作人。
	ActorID string
	// Action 精确匹配；ActionPrefix 前缀匹配。二者至多给一个。
	Action       string
	ActionPrefix string
	ResourceKind string
	From         time.Time
	To           time.Time
	Limit        int
	Cursor       string
}

// ListedEvent 是返回给管理界面的一条审计记录。不含 request_id、授权版本等内部字段。
type ListedEvent struct {
	ID           string            `json:"id"`
	ActorID      string            `json:"actor_id"`
	ActorName    string            `json:"actor_name"`
	ActorEmail   string            `json:"actor_email"`
	Action       string            `json:"action"`
	Decision     string            `json:"decision"`
	Reason       string            `json:"reason"`
	ResourceKind string            `json:"resource_kind"`
	ResourceID   string            `json:"resource_id"`
	Metadata     map[string]string `json:"metadata"`
	CreatedAt    time.Time         `json:"created_at"`
}

// Page 是一页审计记录；NextCursor 为空表示没有更多。
type Page struct {
	Events     []ListedEvent `json:"events"`
	NextCursor string        `json:"next_cursor"`
}

// Validate 校验并规范化查询条件（Limit 为 0 时取默认值）。
func (query *Query) Validate() error {
	if !validUUID(query.OrganizationID) {
		return invalidQuery("organization_id")
	}
	if query.ActorID != "" && !validUUID(query.ActorID) {
		return invalidQuery("actor_id")
	}
	if query.Action != "" && query.ActionPrefix != "" {
		return invalidQuery("action")
	}
	if query.Action != "" && !stableToken(query.Action) {
		return invalidQuery("action")
	}
	if query.ActionPrefix != "" && !stableToken(query.ActionPrefix) {
		return invalidQuery("action")
	}
	if query.ResourceKind != "" && (!stableToken(query.ResourceKind) || len(query.ResourceKind) > 64) {
		return invalidQuery("resource_kind")
	}
	if !query.From.IsZero() && !query.To.IsZero() {
		if !query.From.Before(query.To) {
			return invalidQuery("time range")
		}
		if query.To.Sub(query.From) > MaxQuerySpan {
			return invalidQuery("time range span")
		}
	}
	if query.Limit == 0 {
		query.Limit = DefaultPageSize
	}
	if query.Limit < 1 || query.Limit > MaxPageSize {
		return invalidQuery("limit")
	}
	if query.Cursor != "" {
		if _, _, err := decodeCursor(query.Cursor); err != nil {
			return err
		}
	}
	return nil
}

// List 按时间倒序返回一页审计记录（游标分页，不使用 OFFSET）。
// 必须在组织租户事务内调用；行级安全与显式的 organization_id 条件共同保证租户隔离。
func List(ctx context.Context, tx pgx.Tx, query Query) (Page, error) {
	if err := query.Validate(); err != nil {
		return Page{}, err
	}
	conditions := []string{
		"e.organization_id = openfox_private.current_organization_id()",
		"e.organization_id = $1::uuid",
	}
	args := []any{query.OrganizationID}
	add := func(condition string, value any) {
		args = append(args, value)
		conditions = append(conditions, strings.ReplaceAll(condition, "$$", "$"+strconv.Itoa(len(args))))
	}
	if query.ActorID != "" {
		add("e.actor_id = $$::uuid", query.ActorID)
	}
	if query.Action != "" {
		add("e.action = $$", query.Action)
	}
	if query.ActionPrefix != "" {
		// 动作只含 [A-Za-z0-9_.-]；下划线是 LIKE 通配符，需转义。
		add(`e.action LIKE $$ ESCAPE '\'`, strings.ReplaceAll(query.ActionPrefix, "_", `\_`)+"%")
	}
	if query.ResourceKind != "" {
		add("e.resource_kind = $$", query.ResourceKind)
	}
	if !query.From.IsZero() {
		add("e.created_at >= $$", query.From.UTC())
	}
	if !query.To.IsZero() {
		add("e.created_at < $$", query.To.UTC())
	}
	if query.Cursor != "" {
		createdAt, id, err := decodeCursor(query.Cursor)
		if err != nil {
			return Page{}, err
		}
		args = append(args, createdAt, id)
		conditions = append(conditions, fmt.Sprintf("(e.created_at, e.id) < ($%d, $%d::uuid)", len(args)-1, len(args)))
	}
	args = append(args, query.Limit+1)
	rows, err := tx.Query(ctx, `
		SELECT e.id::text, e.actor_id::text, COALESCE(u.full_name, ''), COALESCE(u.email, ''),
		e.action, e.decision, COALESCE(e.reason, ''), e.resource_kind, e.resource_id,
		       e.safe_metadata, e.created_at
		  FROM openfox.audit_events e
		  LEFT JOIN openfox.users u ON u.id = e.actor_id
		 WHERE `+strings.Join(conditions, " AND ")+`
		 ORDER BY e.created_at DESC, e.id DESC
		 LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	page := Page{Events: make([]ListedEvent, 0, query.Limit)}
	for rows.Next() {
		var record ListedEvent
		var fullName string
		var metadata []byte
		if err := rows.Scan(&record.ID, &record.ActorID, &fullName, &record.ActorEmail,
			&record.Action, &record.Decision, &record.Reason, &record.ResourceKind, &record.ResourceID,
			&metadata, &record.CreatedAt); err != nil {
			return Page{}, err
		}
		record.ActorName = actorDisplayName(fullName, record.ActorEmail, record.ActorID)
		record.Metadata = SanitizeMetadata(metadata)
		page.Events = append(page.Events, record)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	if len(page.Events) > query.Limit {
		page.Events = page.Events[:query.Limit]
		last := page.Events[len(page.Events)-1]
		page.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

// SanitizeMetadata 在读取时再做一次白名单过滤：只保留白名单键的字符串值（控制字符与超长值丢弃）。
// 写入端已校验，但数据库函数也会直接写入审计行，读取端不信任存量数据。
func SanitizeMetadata(raw []byte) map[string]string {
	result := map[string]string{}
	var decoded map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &decoded) != nil {
		return result
	}
	for key, value := range decoded {
		text, ok := value.(string)
		if !ok || !allowlistedMetadataKey(key) || text == "" || len(text) > 256 || strings.IndexFunc(text, isControl) >= 0 {
			continue
		}
		result[key] = text
	}
	return result
}

// actorDisplayName：姓名优先，其次邮箱，都没有时回退为 ID 前缀。
func actorDisplayName(fullName, email, id string) string {
	if name := strings.TrimSpace(fullName); name != "" {
		return name
	}
	if email != "" {
		return email
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// 游标是不透明字符串：base64url("<创建时间的微秒时间戳>.<事件 ID>")。
func encodeCursor(createdAt time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(createdAt.UnixMicro(), 10) + "." + id))
}

func decodeCursor(cursor string) (time.Time, string, error) {
	if len(cursor) > 128 {
		return time.Time{}, "", invalidQuery("cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", invalidQuery("cursor")
	}
	micros, id, found := strings.Cut(string(raw), ".")
	value, err := strconv.ParseInt(micros, 10, 64)
	if !found || err != nil || value <= 0 || !validUUID(id) {
		return time.Time{}, "", invalidQuery("cursor")
	}
	return time.UnixMicro(value).UTC(), id, nil
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func invalidQuery(field string) error {
	return fmt.Errorf("%w: %s", ErrInvalidQuery, field)
}
