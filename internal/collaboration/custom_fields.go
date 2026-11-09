package collaboration

// 全局配置：自定义字段（字段库、选项）、字段与任务类型的绑定、组织级任务类型列表。
//
// 取值存储约定（default_value 与 task_field_values.value 一致）：
//   TEXT / RICH_TEXT / URL / DATE → JSON 字符串；NUMBER → JSON 数字；BOOLEAN → JSON 布尔；
//   SELECT → 选项 id 字符串；MULTI_SELECT → 选项 id 字符串数组；MEMBER → {"kind":"HUMAN|AGENT","id":uuid}。
// 创建字段时选项还没有 id，SELECT / MULTI_SELECT 的默认值用 {"option_index": n} /
// {"option_indexes": [..]} 引用本次请求的 options，落库时换成选项 id。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/idempotency"
	"openfox/internal/membership"
)

var customFieldKinds = map[string]bool{
	"TEXT": true, "NUMBER": true, "SELECT": true, "MULTI_SELECT": true, "BOOLEAN": true,
	"DATE": true, "MEMBER": true, "URL": true, "RICH_TEXT": true,
}

const defaultOptionColor = "#6B7280"

type FieldOption struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Color      string     `json:"color"`
	Position   int        `json:"position"`
	ArchivedAt *time.Time `json:"archived_at"`
	Version    uint64     `json:"version"`
}

type FieldCreator struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

type CustomField struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Kind            string          `json:"kind"`
	Description     string          `json:"description"`
	DefaultValue    json.RawMessage `json:"default_value"`
	MemberScope     *string         `json:"member_scope"`
	Options         []FieldOption   `json:"options"`
	UsedByTypeCount int64           `json:"used_by_type_count"`
	CreatedBy       *FieldCreator   `json:"created_by"`
	ArchivedAt      *time.Time      `json:"archived_at"`
	Version         uint64          `json:"version"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type FieldOptionInput struct {
	Label string `json:"label"`
	Color string `json:"color"`
}

type CustomFieldInput struct {
	Name           string
	Kind           string
	Description    string
	DefaultValue   json.RawMessage
	MemberScope    string
	Options        []FieldOptionInput
	IdempotencyKey string
}

type CustomFieldUpdateInput struct {
	Name         *string
	Description  *string
	DefaultValue OptionalRaw
	MemberScope  *string
	Version      uint64
}

type FieldOptionCreateInput struct {
	Label          string
	Color          string
	IdempotencyKey string
}

type FieldOptionUpdateInput struct {
	Label    *string
	Color    *string
	Position *int
	Version  uint64
}

// FieldMemberRef 是 MEMBER 字段取值引用的人员或 Agent。
type FieldMemberRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type OrgTaskType struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Icon     *string  `json:"icon"`
	FieldIDs []string `json:"field_ids"`
}

type OrgTaskTypeSpace struct {
	SpaceID     string        `json:"space_id"`
	SpaceName   string        `json:"space_name"`
	SpacePrefix string        `json:"space_prefix"`
	TaskTypes   []OrgTaskType `json:"task_types"`
}

func decodeStrictValue(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data")
	}
	return nil
}

func isJSONNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}

// NormalizeFieldValue 按字段类型校验并规范化一个取值；JSON null（或空数组）表示清空，返回 nil。
// optionIDs 是该字段当前未归档的选项 id 集合（SELECT / MULTI_SELECT 使用）。
// MEMBER 取值只做格式与 member_scope 校验，返回引用供调用方在数据库里确认存在。
func NormalizeFieldValue(kind, memberScope string, raw json.RawMessage, optionIDs map[string]bool) (json.RawMessage, *FieldMemberRef, error) {
	if isJSONNull(raw) {
		return nil, nil, nil
	}
	bad := func(message string) (json.RawMessage, *FieldMemberRef, error) {
		return nil, nil, invalidInput(message)
	}
	marshal := func(value any) (json.RawMessage, *FieldMemberRef, error) {
		encoded, err := json.Marshal(value)
		return encoded, nil, err
	}
	switch kind {
	case "TEXT", "RICH_TEXT":
		limit := 256
		if kind == "RICH_TEXT" {
			limit = 65536
		}
		var value string
		if err := decodeStrictValue(raw, &value); err != nil || len([]rune(value)) > limit {
			return bad("value must be a string within the length limit")
		}
		return marshal(value)
	case "NUMBER":
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || (trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9')) {
			return bad("value must be a number")
		}
		number, err := strconv.ParseFloat(string(trimmed), 64)
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return bad("value must be a finite number")
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, trimmed); err != nil {
			return bad("value must be a number")
		}
		return json.RawMessage(compact.Bytes()), nil, nil
	case "BOOLEAN":
		var value bool
		if err := decodeStrictValue(raw, &value); err != nil {
			return bad("value must be a boolean")
		}
		return marshal(value)
	case "DATE":
		var value string
		if err := decodeStrictValue(raw, &value); err != nil {
			return bad("value must be a YYYY-MM-DD date")
		}
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil || parsed.Format("2006-01-02") != value {
			return bad("value must be a YYYY-MM-DD date")
		}
		return marshal(value)
	case "URL":
		var value string
		if err := decodeStrictValue(raw, &value); err != nil || len(value) > 2048 {
			return bad("value must be an http(s) url within 2048 characters")
		}
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return bad("value must be an http(s) url")
		}
		return marshal(value)
	case "SELECT":
		var value string
		if err := decodeStrictValue(raw, &value); err != nil {
			return bad("value must be an option id")
		}
		value = strings.ToLower(value)
		if !optionIDs[value] {
			return bad("value is not an available option")
		}
		return marshal(value)
	case "MULTI_SELECT":
		var values []string
		if err := decodeStrictValue(raw, &values); err != nil || len(values) > 50 {
			return bad("value must be an array of at most 50 option ids")
		}
		if len(values) == 0 {
			return nil, nil, nil
		}
		seen := make(map[string]bool, len(values))
		normalized := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.ToLower(value)
			if !optionIDs[value] || seen[value] {
				return bad("value contains an unavailable or duplicate option")
			}
			seen[value] = true
			normalized = append(normalized, value)
		}
		return marshal(normalized)
	case "MEMBER":
		var value FieldMemberRef
		if err := decodeStrictValue(raw, &value); err != nil {
			return bad("value must be {kind, id}")
		}
		value.Kind = strings.ToUpper(value.Kind)
		value.ID = strings.ToLower(value.ID)
		if (value.Kind != "HUMAN" && value.Kind != "AGENT") || !membership.ValidateUUID(value.ID) {
			return bad("value must be {kind: HUMAN|AGENT, id: uuid}")
		}
		if (memberScope == "HUMANS" && value.Kind != "HUMAN") || (memberScope == "AGENTS" && value.Kind != "AGENT") {
			return bad("value kind is outside member_scope")
		}
		encoded, err := json.Marshal(value)
		return encoded, &value, err
	default:
		return bad("unknown field kind")
	}
}

// checkMemberRef 确认 MEMBER 取值引用的成员或 Agent 属于当前组织。
func checkMemberRef(ctx context.Context, tx pgx.Tx, ref *FieldMemberRef) error {
	if ref == nil {
		return nil
	}
	var exists bool
	var err error
	if ref.Kind == "HUMAN" {
		err = tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.organization_members
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND user_id = $1::uuid AND status = 'ACTIVE')`, ref.ID).Scan(&exists)
	} else {
		err = tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.agents
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND id = $1::uuid AND status = 'ACTIVE')`, ref.ID).Scan(&exists)
	}
	if err != nil {
		return err
	}
	if !exists {
		return invalidInput("value references an unavailable member")
	}
	return nil
}

func normalizeOptionInput(label, color string) (string, string, error) {
	label = strings.TrimSpace(label)
	if label == "" || len([]rune(label)) > 100 {
		return "", "", invalidInput("option label length is out of range")
	}
	for _, character := range label {
		if character < 0x20 || character == 0x7f {
			return "", "", invalidInput("option label contains control characters")
		}
	}
	if color == "" {
		color = defaultOptionColor
	}
	if err := validateColor(color); err != nil {
		return "", "", err
	}
	return label, color, nil
}

// resolveCreateDefault 解析创建请求里的默认值（SELECT / MULTI_SELECT 用选项下标引用）。
func resolveCreateDefault(kind, memberScope string, raw json.RawMessage, optionIDs []string) (json.RawMessage, *FieldMemberRef, error) {
	if isJSONNull(raw) {
		return nil, nil, nil
	}
	known := make(map[string]bool, len(optionIDs))
	for _, id := range optionIDs {
		known[id] = true
	}
	switch kind {
	case "SELECT":
		var reference struct {
			OptionIndex *int `json:"option_index"`
		}
		if err := decodeStrictValue(raw, &reference); err != nil || reference.OptionIndex == nil ||
			*reference.OptionIndex < 0 || *reference.OptionIndex >= len(optionIDs) {
			return nil, nil, invalidInput(`default_value must be {"option_index": n} within options`)
		}
		raw = json.RawMessage(strconv.Quote(optionIDs[*reference.OptionIndex]))
	case "MULTI_SELECT":
		var reference struct {
			OptionIndexes []int `json:"option_indexes"`
		}
		if err := decodeStrictValue(raw, &reference); err != nil {
			return nil, nil, invalidInput(`default_value must be {"option_indexes": [..]} within options`)
		}
		ids := make([]string, 0, len(reference.OptionIndexes))
		for _, index := range reference.OptionIndexes {
			if index < 0 || index >= len(optionIDs) {
				return nil, nil, invalidInput(`default_value must be {"option_indexes": [..]} within options`)
			}
			ids = append(ids, optionIDs[index])
		}
		encoded, err := json.Marshal(ids)
		if err != nil {
			return nil, nil, err
		}
		raw = encoded
	}
	return NormalizeFieldValue(kind, memberScope, raw, known)
}

// customFieldsQuery 读取字段、未归档选项和使用数（共三条查询，不随字段数增长）。
func (s Service) customFieldsQuery(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]CustomField, error) {
	rows, err := tx.Query(ctx, `
		SELECT cf.id::text, cf.name, cf.kind, cf.description, cf.default_value, cf.member_scope,
		       cf.created_by::text, u.email, cf.archived_at, cf.version, cf.created_at, cf.updated_at
		  FROM openfox.custom_fields cf
		  LEFT JOIN openfox.users u ON u.id = cf.created_by
		 WHERE cf.organization_id = openfox_private.current_organization_id() `+where+`
		 ORDER BY cf.created_at, cf.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]CustomField, 0)
	ids := make([]string, 0)
	for rows.Next() {
		var item CustomField
		var version int64
		var createdBy, email *string
		var defaultValue []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind, &item.Description, &defaultValue, &item.MemberScope,
			&createdBy, &email, &item.ArchivedAt, &version, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if item.Version, err = positiveVersion(version); err != nil {
			return nil, err
		}
		if defaultValue != nil {
			item.DefaultValue = json.RawMessage(defaultValue)
		}
		if createdBy != nil && email != nil {
			item.CreatedBy = &FieldCreator{UserID: *createdBy, Email: *email}
		}
		item.Options = []FieldOption{}
		items = append(items, item)
		ids = append(ids, item.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(items) == 0 {
		return items, nil
	}
	index := make(map[string]int, len(items))
	for position, item := range items {
		index[item.ID] = position
	}

	optionRows, err := tx.Query(ctx, `
		SELECT field_id::text, id::text, label, color, position, archived_at, version
		  FROM openfox.custom_field_options
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND field_id = ANY($1::uuid[]) AND archived_at IS NULL
		 ORDER BY field_id, position, id`, ids)
	if err != nil {
		return nil, err
	}
	defer optionRows.Close()
	for optionRows.Next() {
		var fieldID string
		var option FieldOption
		var version int64
		if err := optionRows.Scan(&fieldID, &option.ID, &option.Label, &option.Color, &option.Position,
			&option.ArchivedAt, &version); err != nil {
			return nil, err
		}
		if option.Version, err = positiveVersion(version); err != nil {
			return nil, err
		}
		items[index[fieldID]].Options = append(items[index[fieldID]].Options, option)
	}
	if err := optionRows.Err(); err != nil {
		return nil, err
	}
	optionRows.Close()

	usageRows, err := tx.Query(ctx, `
		SELECT field_id::text, count(*) FROM openfox.task_type_fields
		 WHERE organization_id = openfox_private.current_organization_id() AND field_id = ANY($1::uuid[])
		 GROUP BY field_id`, ids)
	if err != nil {
		return nil, err
	}
	defer usageRows.Close()
	for usageRows.Next() {
		var fieldID string
		var count int64
		if err := usageRows.Scan(&fieldID, &count); err != nil {
			return nil, err
		}
		items[index[fieldID]].UsedByTypeCount = count
	}
	return items, usageRows.Err()
}

func (s Service) ListCustomFields(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, includeArchived bool, spaceID string) ([]CustomField, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	where := `AND cf.archived_at IS NULL`
	if includeArchived {
		where = ``
	}
	if spaceID != "" {
		if !membership.ValidateUUID(spaceID) {
			return nil, ErrSpaceNotFound
		}
		ok, err := spaceReadable(ctx, tx, actor, spaceID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrSpaceNotFound
		}
		return s.customFieldsQuery(ctx, tx, where+` AND EXISTS (SELECT 1 FROM openfox.task_type_fields b WHERE b.organization_id=cf.organization_id AND b.field_id=cf.id AND b.space_id=$1::uuid)`, spaceID)
	}
	return s.customFieldsQuery(ctx, tx, where)
}

func (s Service) GetCustomField(ctx context.Context, tx pgx.Tx, organizationID, fieldID string, actor Actor) (CustomField, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return CustomField{}, err
	}
	return s.getCustomField(ctx, tx, fieldID)
}

func (s Service) getCustomField(ctx context.Context, tx pgx.Tx, fieldID string) (CustomField, error) {
	if !membership.ValidateUUID(fieldID) {
		return CustomField{}, configNotFound("custom field not found")
	}
	items, err := s.customFieldsQuery(ctx, tx, `AND cf.id = $1::uuid`, fieldID)
	if err != nil {
		return CustomField{}, err
	}
	if len(items) == 0 {
		return CustomField{}, configNotFound("custom field not found")
	}
	return items[0], nil
}

// CreateCustomField 创建字段（含选项与默认值）；返回的 bool 为 true 表示幂等重放。
func (s Service) CreateCustomField(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, input CustomFieldInput) (CustomField, bool, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return CustomField{}, false, err
	}
	name, err := normalizeConfigName(input.Name, 64)
	if err != nil {
		return CustomField{}, false, err
	}
	kind := strings.ToUpper(strings.TrimSpace(input.Kind))
	if !customFieldKinds[kind] {
		return CustomField{}, false, invalidInput("unknown field kind")
	}
	description, err := validateDescription(input.Description)
	if err != nil {
		return CustomField{}, false, err
	}
	memberScope := strings.ToUpper(strings.TrimSpace(input.MemberScope))
	if kind == "MEMBER" {
		if memberScope == "" {
			memberScope = "ALL"
		}
		if memberScope != "ALL" && memberScope != "HUMANS" && memberScope != "AGENTS" {
			return CustomField{}, false, invalidInput("member_scope must be ALL, HUMANS or AGENTS")
		}
	} else if memberScope != "" {
		return CustomField{}, false, invalidInput("member_scope is only valid for MEMBER fields")
	}
	hasOptions := kind == "SELECT" || kind == "MULTI_SELECT"
	if !hasOptions && len(input.Options) > 0 {
		return CustomField{}, false, invalidInput("options are only valid for SELECT and MULTI_SELECT fields")
	}
	if len(input.Options) > MaxFieldOptions {
		return CustomField{}, false, invalidInput("too many options")
	}
	type optionRow struct{ id, label, color string }
	options := make([]optionRow, 0, len(input.Options))
	optionIDs := make([]string, 0, len(input.Options))
	seenLabels := make(map[string]bool)
	for _, option := range input.Options {
		label, color, err := normalizeOptionInput(option.Label, option.Color)
		if err != nil {
			return CustomField{}, false, err
		}
		if seenLabels[strings.ToLower(label)] {
			return CustomField{}, false, invalidInput("option labels must be unique")
		}
		seenLabels[strings.ToLower(label)] = true
		id, err := membership.NewID()
		if err != nil {
			return CustomField{}, false, err
		}
		options = append(options, optionRow{id, label, color})
		optionIDs = append(optionIDs, id)
	}
	defaultValue, memberRef, err := resolveCreateDefault(kind, memberScope, input.DefaultValue, optionIDs)
	if err != nil {
		return CustomField{}, false, err
	}

	var requestDefault string
	if !isJSONNull(input.DefaultValue) {
		var compact bytes.Buffer
		if err := json.Compact(&compact, input.DefaultValue); err != nil {
			return CustomField{}, false, invalidInput("default_value is not valid json")
		}
		requestDefault = compact.String()
	}
	hashOptions := make([]FieldOptionInput, 0, len(options))
	for _, option := range options {
		hashOptions = append(hashOptions, FieldOptionInput{Label: option.label, Color: option.color})
	}
	claim, err := idempotency.Begin(ctx, tx, "custom_field.create", input.IdempotencyKey, struct {
		Name, Kind, Description, MemberScope, DefaultValue string
		Options                                            []FieldOptionInput
	}{name, kind, description, memberScope, requestDefault, hashOptions})
	if err != nil {
		return CustomField{}, false, err
	}
	if claim.Replay {
		item, err := s.getCustomField(ctx, tx, claim.ResourceID)
		return item, true, err
	}
	if err := checkMemberRef(ctx, tx, memberRef); err != nil {
		return CustomField{}, false, err
	}
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.custom_fields
		 WHERE organization_id = openfox_private.current_organization_id() AND archived_at IS NULL`).Scan(&count); err != nil {
		return CustomField{}, false, err
	}
	if count >= MaxCustomFields {
		return CustomField{}, false, ErrLimitReached
	}
	fieldID, err := membership.NewID()
	if err != nil {
		return CustomField{}, false, err
	}
	var storedDefault *string
	if defaultValue != nil {
		text := string(defaultValue)
		storedDefault = &text
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.custom_fields (
			organization_id, id, name, kind, description, default_value, member_scope, created_by
		) VALUES (
			openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5::jsonb, NULLIF($6, ''), $7::uuid
		)`, fieldID, name, kind, description, storedDefault, memberScope, actor.UserID); err != nil {
		return CustomField{}, false, configDatabaseError(err)
	}
	if len(options) > 0 {
		ids := make([]string, 0, len(options))
		labels := make([]string, 0, len(options))
		colors := make([]string, 0, len(options))
		positions := make([]int32, 0, len(options))
		for index, option := range options {
			ids = append(ids, option.id)
			labels = append(labels, option.label)
			colors = append(colors, option.color)
			positions = append(positions, int32(index))
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.custom_field_options (organization_id, field_id, id, label, color, position)
			SELECT openfox_private.current_organization_id(), $1::uuid, v.id, v.label, v.color, v.position
			  FROM unnest($2::uuid[], $3::text[], $4::text[], $5::int[]) AS v(id, label, color, position)`,
			fieldID, ids, labels, colors, positions); err != nil {
			return CustomField{}, false, configDatabaseError(err)
		}
	}
	if err := idempotency.Complete(ctx, tx, claim, fieldID); err != nil {
		return CustomField{}, false, err
	}
	item, err := s.getCustomField(ctx, tx, fieldID)
	return item, false, err
}

type lockedField struct {
	Kind         string
	MemberScope  string
	DefaultValue json.RawMessage
	Version      int64
}

// lockField 锁定未归档字段；不存在或已归档都返回 404。
func lockField(ctx context.Context, tx pgx.Tx, fieldID string) (lockedField, error) {
	var item lockedField
	var scope *string
	var defaultValue []byte
	err := tx.QueryRow(ctx, `
		SELECT kind, member_scope, default_value, version FROM openfox.custom_fields
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND archived_at IS NULL
		   FOR UPDATE`, fieldID).Scan(&item.Kind, &scope, &defaultValue, &item.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedField{}, configNotFound("custom field not found")
	}
	if err != nil {
		return lockedField{}, err
	}
	if scope != nil {
		item.MemberScope = *scope
	}
	if defaultValue != nil {
		item.DefaultValue = json.RawMessage(defaultValue)
	}
	return item, nil
}

func activeOptionIDs(ctx context.Context, tx pgx.Tx, fieldID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM openfox.custom_field_options
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND field_id = $1::uuid AND archived_at IS NULL`, fieldID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

func (s Service) UpdateCustomField(ctx context.Context, tx pgx.Tx, organizationID, fieldID string, actor Actor, input CustomFieldUpdateInput) (CustomField, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return CustomField{}, err
	}
	if !membership.ValidateUUID(fieldID) {
		return CustomField{}, configNotFound("custom field not found")
	}
	if input.Version == 0 {
		return CustomField{}, ErrInvalidVersion
	}
	var name, description *string
	if input.Name != nil {
		value, err := normalizeConfigName(*input.Name, 64)
		if err != nil {
			return CustomField{}, err
		}
		name = &value
	}
	if input.Description != nil {
		value, err := validateDescription(*input.Description)
		if err != nil {
			return CustomField{}, err
		}
		description = &value
	}
	current, err := lockField(ctx, tx, fieldID)
	if err != nil {
		return CustomField{}, err
	}
	if uint64(current.Version) != input.Version {
		return CustomField{}, ErrVersionConflict
	}
	scope := current.MemberScope
	if input.MemberScope != nil {
		if current.Kind != "MEMBER" {
			return CustomField{}, invalidInput("member_scope is only valid for MEMBER fields")
		}
		scope = strings.ToUpper(strings.TrimSpace(*input.MemberScope))
		if scope != "ALL" && scope != "HUMANS" && scope != "AGENTS" {
			return CustomField{}, invalidInput("member_scope must be ALL, HUMANS or AGENTS")
		}
	}
	// 生效的默认值（新传的或原有的）必须满足生效的类型约束。
	effective := current.DefaultValue
	if input.DefaultValue.Set {
		effective = input.DefaultValue.Value
	}
	var optionIDs map[string]bool
	if current.Kind == "SELECT" || current.Kind == "MULTI_SELECT" {
		if optionIDs, err = activeOptionIDs(ctx, tx, fieldID); err != nil {
			return CustomField{}, err
		}
	}
	normalizedDefault, memberRef, err := NormalizeFieldValue(current.Kind, scope, effective, optionIDs)
	if err != nil {
		return CustomField{}, err
	}
	if input.DefaultValue.Set {
		if err := checkMemberRef(ctx, tx, memberRef); err != nil {
			return CustomField{}, err
		}
	}
	var storedDefault *string
	if normalizedDefault != nil {
		text := string(normalizedDefault)
		storedDefault = &text
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.custom_fields
		   SET name = COALESCE($2, name), description = COALESCE($3, description),
		       default_value = $4::jsonb, member_scope = CASE WHEN kind = 'MEMBER' THEN $5 ELSE NULL END,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $6`,
		fieldID, name, description, storedDefault, scope, int64(input.Version))
	if err != nil {
		return CustomField{}, configDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return CustomField{}, ErrVersionConflict
	}
	return s.getCustomField(ctx, tx, fieldID)
}

// DeleteCustomField 把未绑定任务类型的字段归档；已有绑定返回 CUSTOM_FIELD_IN_USE。
func (s Service) DeleteCustomField(ctx context.Context, tx pgx.Tx, organizationID, fieldID string, actor Actor) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(fieldID) {
		return configNotFound("custom field not found")
	}
	if _, err := lockField(ctx, tx, fieldID); err != nil {
		return err
	}
	var bound bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.task_type_fields
		 WHERE organization_id = openfox_private.current_organization_id() AND field_id = $1::uuid)`,
		fieldID).Scan(&bound); err != nil {
		return err
	}
	if bound {
		return ErrCustomFieldInUse
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.custom_fields
		   SET archived_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, fieldID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return configNotFound("custom field not found")
	}
	return nil
}

func scanFieldOption(row interface{ Scan(...any) error }) (FieldOption, error) {
	var option FieldOption
	var version int64
	if err := row.Scan(&option.ID, &option.Label, &option.Color, &option.Position, &option.ArchivedAt, &version); err != nil {
		return FieldOption{}, err
	}
	var err error
	option.Version, err = positiveVersion(version)
	return option, err
}

const fieldOptionColumns = `id::text, label, color, position, archived_at, version`

func (s Service) getFieldOption(ctx context.Context, tx pgx.Tx, fieldID, optionID string) (FieldOption, error) {
	if !membership.ValidateUUID(optionID) {
		return FieldOption{}, configNotFound("option not found")
	}
	option, err := scanFieldOption(tx.QueryRow(ctx, `
		SELECT `+fieldOptionColumns+` FROM openfox.custom_field_options
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND field_id = $1::uuid AND id = $2::uuid`, fieldID, optionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return FieldOption{}, configNotFound("option not found")
	}
	return option, err
}

type activeOption struct {
	ID       string
	Label    string
	Position int
}

func listActiveOptions(ctx context.Context, tx pgx.Tx, fieldID string) ([]activeOption, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, label, position FROM openfox.custom_field_options
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND field_id = $1::uuid AND archived_at IS NULL
		 ORDER BY position, id`, fieldID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]activeOption, 0)
	for rows.Next() {
		var item activeOption
		if err := rows.Scan(&item.ID, &item.Label, &item.Position); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func requireOptionField(kind string) error {
	if kind != "SELECT" && kind != "MULTI_SELECT" {
		return invalidInput("only SELECT and MULTI_SELECT fields have options")
	}
	return nil
}

// CreateFieldOption 在字段选项末尾追加一个选项；返回的 bool 为 true 表示幂等重放。
func (s Service) CreateFieldOption(ctx context.Context, tx pgx.Tx, organizationID, fieldID string, actor Actor, input FieldOptionCreateInput) (FieldOption, bool, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return FieldOption{}, false, err
	}
	if !membership.ValidateUUID(fieldID) {
		return FieldOption{}, false, configNotFound("custom field not found")
	}
	label, color, err := normalizeOptionInput(input.Label, input.Color)
	if err != nil {
		return FieldOption{}, false, err
	}
	claim, err := idempotency.Begin(ctx, tx, "custom_field_option.create", input.IdempotencyKey, struct {
		FieldID, Label, Color string
	}{strings.ToLower(fieldID), label, color})
	if err != nil {
		return FieldOption{}, false, err
	}
	if claim.Replay {
		option, err := s.getFieldOption(ctx, tx, fieldID, claim.ResourceID)
		return option, true, err
	}
	field, err := lockField(ctx, tx, fieldID)
	if err != nil {
		return FieldOption{}, false, err
	}
	if err := requireOptionField(field.Kind); err != nil {
		return FieldOption{}, false, err
	}
	existing, err := listActiveOptions(ctx, tx, fieldID)
	if err != nil {
		return FieldOption{}, false, err
	}
	if len(existing) >= MaxFieldOptions {
		return FieldOption{}, false, ErrLimitReached
	}
	position := 0
	for _, option := range existing {
		if strings.EqualFold(option.Label, label) {
			return FieldOption{}, false, ErrNameTaken
		}
		if option.Position >= position {
			position = option.Position + 1
		}
	}
	optionID, err := membership.NewID()
	if err != nil {
		return FieldOption{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.custom_field_options (organization_id, field_id, id, label, color, position)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, $5)`,
		fieldID, optionID, label, color, position); err != nil {
		return FieldOption{}, false, configDatabaseError(err)
	}
	if err := idempotency.Complete(ctx, tx, claim, optionID); err != nil {
		return FieldOption{}, false, err
	}
	option, err := s.getFieldOption(ctx, tx, fieldID, optionID)
	return option, false, err
}

// UpdateFieldOption 修改选项；改 position 时锁住字段行并重编号该字段全部未归档选项。
func (s Service) UpdateFieldOption(ctx context.Context, tx pgx.Tx, organizationID, fieldID, optionID string, actor Actor, input FieldOptionUpdateInput) (FieldOption, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return FieldOption{}, err
	}
	if !membership.ValidateUUID(fieldID) {
		return FieldOption{}, configNotFound("custom field not found")
	}
	if !membership.ValidateUUID(optionID) {
		return FieldOption{}, configNotFound("option not found")
	}
	if input.Version == 0 {
		return FieldOption{}, ErrInvalidVersion
	}
	if input.Position != nil && *input.Position < 0 {
		return FieldOption{}, invalidInput("position must not be negative")
	}
	var label, color *string
	if input.Label != nil {
		normalized, _, err := normalizeOptionInput(*input.Label, "")
		if err != nil {
			return FieldOption{}, err
		}
		label = &normalized
	}
	if input.Color != nil {
		if err := validateColor(*input.Color); err != nil {
			return FieldOption{}, err
		}
		color = input.Color
	}
	field, err := lockField(ctx, tx, fieldID)
	if err != nil {
		return FieldOption{}, err
	}
	if err := requireOptionField(field.Kind); err != nil {
		return FieldOption{}, err
	}
	existing, err := listActiveOptions(ctx, tx, fieldID)
	if err != nil {
		return FieldOption{}, err
	}
	var target *activeOption
	for index := range existing {
		if existing[index].ID == strings.ToLower(optionID) {
			target = &existing[index]
		}
	}
	if target == nil {
		return FieldOption{}, configNotFound("option not found")
	}
	var currentVersion int64
	if err := tx.QueryRow(ctx, `
		SELECT version FROM openfox.custom_field_options
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND field_id = $1::uuid AND id = $2::uuid`, fieldID, optionID).Scan(&currentVersion); err != nil {
		return FieldOption{}, err
	}
	if uint64(currentVersion) != input.Version {
		return FieldOption{}, ErrVersionConflict
	}
	if label != nil {
		for _, option := range existing {
			if option.ID != target.ID && strings.EqualFold(option.Label, *label) {
				return FieldOption{}, ErrNameTaken
			}
		}
	}
	targetPosition := target.Position
	if input.Position != nil {
		ordered := make([]string, 0, len(existing))
		current := make(map[string]int, len(existing))
		for _, option := range existing {
			current[option.ID] = option.Position
			if option.ID != target.ID {
				ordered = append(ordered, option.ID)
			}
		}
		insertAt := *input.Position
		if insertAt > len(ordered) {
			insertAt = len(ordered)
		}
		ordered = append(ordered[:insertAt], append([]string{target.ID}, ordered[insertAt:]...)...)
		ids := make([]string, 0)
		positions := make([]int32, 0)
		for index, id := range ordered {
			if id == target.ID {
				targetPosition = index
				continue
			}
			if current[id] != index {
				ids = append(ids, id)
				positions = append(positions, int32(index))
			}
		}
		if len(ids) > 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE openfox.custom_field_options o
				   SET position = v.position
				  FROM unnest($2::uuid[], $3::int[]) AS v(id, position)
				 WHERE o.organization_id = openfox_private.current_organization_id()
				   AND o.field_id = $1::uuid AND o.id = v.id`, fieldID, ids, positions); err != nil {
				return FieldOption{}, err
			}
		}
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.custom_field_options
		   SET label = COALESCE($3, label), color = COALESCE($4, color), position = $5,
		       version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND field_id = $1::uuid AND id = $2::uuid AND version = $6`,
		fieldID, optionID, label, color, targetPosition, int64(input.Version))
	if err != nil {
		return FieldOption{}, configDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return FieldOption{}, ErrVersionConflict
	}
	return s.getFieldOption(ctx, tx, fieldID, optionID)
}

// DeleteFieldOption 归档选项；若它是字段默认值（或默认值数组的一员）则同步清理默认值。
func (s Service) DeleteFieldOption(ctx context.Context, tx pgx.Tx, organizationID, fieldID, optionID string, actor Actor) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(fieldID) {
		return configNotFound("custom field not found")
	}
	if !membership.ValidateUUID(optionID) {
		return configNotFound("option not found")
	}
	optionID = strings.ToLower(optionID)
	field, err := lockField(ctx, tx, fieldID)
	if err != nil {
		return err
	}
	if err := requireOptionField(field.Kind); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.custom_field_options
		   SET archived_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND field_id = $1::uuid AND id = $2::uuid AND archived_at IS NULL`, fieldID, optionID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return configNotFound("option not found")
	}
	if field.DefaultValue == nil {
		return nil
	}
	var cleared *string
	changed := false
	if field.Kind == "SELECT" {
		var current string
		if json.Unmarshal(field.DefaultValue, &current) == nil && strings.EqualFold(current, optionID) {
			changed = true
		}
	} else {
		var current []string
		if json.Unmarshal(field.DefaultValue, &current) == nil {
			kept := make([]string, 0, len(current))
			for _, id := range current {
				if !strings.EqualFold(id, optionID) {
					kept = append(kept, id)
				}
			}
			if len(kept) != len(current) {
				changed = true
				if len(kept) > 0 {
					encoded, err := json.Marshal(kept)
					if err != nil {
						return err
					}
					text := string(encoded)
					cleared = &text
				}
			}
		}
	}
	if !changed {
		return nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.custom_fields
		   SET default_value = $2::jsonb, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, fieldID, cleared)
	return err
}

// BindFieldToType 把字段绑定到空间的任务类型（幂等）；字段必须未归档。
func (s Service) BindFieldToType(ctx context.Context, tx pgx.Tx, organizationID, spaceID, typeID, fieldID string, actor Actor) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(typeID) {
		return ErrTaskTypeNotFound
	}
	if !membership.ValidateUUID(fieldID) {
		return configNotFound("custom field not found")
	}
	var typeExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.task_types
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND archived_at IS NULL)`, spaceID, typeID).Scan(&typeExists); err != nil {
		return err
	}
	if !typeExists {
		return ErrTaskTypeNotFound
	}
	// FOR SHARE 与删除字段时的 FOR UPDATE 互斥：字段被归档后绑定会拿到 0 行。
	var lockedID string
	if err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.custom_fields
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND archived_at IS NULL
		   FOR SHARE`, fieldID).Scan(&lockedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return configNotFound("custom field not found")
		}
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_type_fields (organization_id, space_id, task_type_id, field_id, position, created_by)
		SELECT openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid,
		       COALESCE(max(position) + 1, 0), $4::uuid
		  FROM openfox.task_type_fields
		 WHERE organization_id = openfox_private.current_organization_id() AND task_type_id = $2::uuid
		ON CONFLICT DO NOTHING`, spaceID, typeID, fieldID, actor.UserID)
	return configDatabaseError(err)
}

// UnbindFieldFromType 解除绑定（幂等）；任务上已有取值保留。
func (s Service) UnbindFieldFromType(ctx context.Context, tx pgx.Tx, organizationID, spaceID, typeID, fieldID string, actor Actor) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(typeID) {
		return ErrTaskTypeNotFound
	}
	if !membership.ValidateUUID(fieldID) {
		return configNotFound("custom field not found")
	}
	_, err := tx.Exec(ctx, `
		DELETE FROM openfox.task_type_fields
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND task_type_id = $2::uuid AND field_id = $3::uuid`, spaceID, typeID, fieldID)
	return err
}

// ListTypeFields 返回绑定到任务类型的未归档字段，按绑定 position 排序（任务详情用）。
func (s Service) ListTypeFields(ctx context.Context, tx pgx.Tx, organizationID, spaceID, typeID string, actor Actor) ([]CustomField, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(typeID) {
		return nil, ErrTaskTypeNotFound
	}
	var typeExists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.task_types
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid)`, spaceID, typeID).Scan(&typeExists); err != nil {
		return nil, err
	}
	if !typeExists {
		return nil, ErrTaskTypeNotFound
	}
	rows, err := tx.Query(ctx, `
		SELECT tf.field_id::text FROM openfox.task_type_fields tf
		  JOIN openfox.custom_fields cf ON cf.organization_id = tf.organization_id AND cf.id = tf.field_id
		 WHERE tf.organization_id = openfox_private.current_organization_id()
		   AND tf.task_type_id = $1::uuid AND cf.archived_at IS NULL
		 ORDER BY tf.position, tf.field_id`, typeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	order := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		order = append(order, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(order) == 0 {
		return []CustomField{}, nil
	}
	fields, err := s.customFieldsQuery(ctx, tx, `AND cf.id = ANY($1::uuid[])`, order)
	if err != nil {
		return nil, err
	}
	rank := make(map[string]int, len(order))
	for index, id := range order {
		rank[id] = index
	}
	sort.SliceStable(fields, func(i, j int) bool { return rank[fields[i].ID] < rank[fields[j].ID] })
	return fields, nil
}

// ListOrganizationTaskTypes 返回调用者可读的 ACTIVE 空间及其启用的任务类型和绑定字段 id（两条查询）。
func (s Service) ListOrganizationTaskTypes(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) ([]OrgTaskTypeSpace, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT s.id::text, s.name, s.prefix, tt.id::text, tt.name, tt.icon
		  FROM openfox.spaces s
		  LEFT JOIN openfox.task_types tt
		    ON tt.organization_id = s.organization_id AND tt.space_id = s.id
		   AND tt.enabled AND tt.archived_at IS NULL
		 WHERE s.organization_id = openfox_private.current_organization_id()
		   AND s.status = 'ACTIVE' AND `+SpaceReadableSQL+`
		 ORDER BY s.created_at, s.id, tt.created_at, tt.id`, strings.ToUpper(string(actor.Role)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	spaces := make([]OrgTaskTypeSpace, 0)
	typeIDs := make([]string, 0)
	for rows.Next() {
		var spaceID, spaceName, spacePrefix string
		var typeID, typeName, icon *string
		if err := rows.Scan(&spaceID, &spaceName, &spacePrefix, &typeID, &typeName, &icon); err != nil {
			return nil, err
		}
		if n := len(spaces); n == 0 || spaces[n-1].SpaceID != spaceID {
			spaces = append(spaces, OrgTaskTypeSpace{SpaceID: spaceID, SpaceName: spaceName,
				SpacePrefix: spacePrefix, TaskTypes: []OrgTaskType{}})
		}
		if typeID != nil && typeName != nil {
			last := &spaces[len(spaces)-1]
			last.TaskTypes = append(last.TaskTypes, OrgTaskType{ID: *typeID, Name: *typeName, Icon: icon, FieldIDs: []string{}})
			typeIDs = append(typeIDs, *typeID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(typeIDs) == 0 {
		return spaces, nil
	}
	fieldRows, err := tx.Query(ctx, `
		SELECT tf.task_type_id::text, tf.field_id::text FROM openfox.task_type_fields tf
		  JOIN openfox.custom_fields cf ON cf.organization_id = tf.organization_id AND cf.id = tf.field_id
		 WHERE tf.organization_id = openfox_private.current_organization_id()
		   AND tf.task_type_id = ANY($1::uuid[]) AND cf.archived_at IS NULL
		 ORDER BY tf.task_type_id, tf.position, tf.field_id`, typeIDs)
	if err != nil {
		return nil, err
	}
	defer fieldRows.Close()
	fieldsByType := make(map[string][]string)
	for fieldRows.Next() {
		var typeID, fieldID string
		if err := fieldRows.Scan(&typeID, &fieldID); err != nil {
			return nil, err
		}
		fieldsByType[typeID] = append(fieldsByType[typeID], fieldID)
	}
	if err := fieldRows.Err(); err != nil {
		return nil, err
	}
	for spaceIndex := range spaces {
		for typeIndex := range spaces[spaceIndex].TaskTypes {
			if ids, ok := fieldsByType[spaces[spaceIndex].TaskTypes[typeIndex].ID]; ok {
				spaces[spaceIndex].TaskTypes[typeIndex].FieldIDs = ids
			}
		}
	}
	return spaces, nil
}
