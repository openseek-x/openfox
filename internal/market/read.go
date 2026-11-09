package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/skills"
)

// 市场读取。目录不属于任何组织，没有 RLS；调用方先确认组织成员身份（租户事务）。

const skillColumns = `skill_id, version, name, description, icon, category_ids, tags, publisher, origin, featured, downloads,
       COALESCE(NULLIF(stats->>'stars', '')::bigint, 0), file_count, total_bytes`

func scanSkill(row pgx.Row, extra ...any) (Skill, error) {
	var s Skill
	dest := append([]any{&s.MarketSkillID, &s.Version, &s.Name, &s.Description, &s.Icon, &s.CategoryIDs, &s.Tags, &s.Publisher,
		&s.Origin, &s.Featured, &s.Downloads, &s.Stars, &s.FileCount, &s.TotalBytes}, extra...)
	err := row.Scan(dest...)
	if s.Name == nil {
		s.Name = Text{}
	}
	if s.Description == nil {
		s.Description = Text{}
	}
	if s.CategoryIDs == nil {
		s.CategoryIDs = []string{}
	}
	if s.Tags == nil {
		s.Tags = []Text{}
	}
	return s, err
}

// likePattern 把搜索词转成 ILIKE 的包含匹配（配合 ESCAPE '\'），转义 \ % _。
func likePattern(q string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
}

// 目录里存放的分类与精选包保持导入格式的 camelCase 结构，读取时转成契约字段。
type catalogCategory struct {
	ID            string `json:"id"`
	Name          Text   `json:"name"`
	Order         int    `json:"order"`
	SkillCount    int    `json:"skillCount"`
	TemplateCount int    `json:"templateCount"`
}

type catalogPack struct {
	ID      string `json:"id"`
	Name    Text   `json:"name"`
	Tint    string `json:"tint"`
	Members []struct {
		SkillID string  `json:"skillId"`
		Name    Text    `json:"name"`
		Icon    *string `json:"icon"`
	} `json:"members"`
}

// GetCatalog 返回分类、精选包与数量；还没导入时返回空目录。
func GetCatalog(ctx context.Context, tx pgx.Tx) (Catalog, error) {
	result := Catalog{Categories: []Category{}, Packs: []Pack{}}
	var categories, packs []byte
	var importedAt time.Time
	err := tx.QueryRow(ctx, `SELECT source, catalog_version, imported_at, categories, packs FROM openfox.market_catalog WHERE id = 1`).
		Scan(&result.Source, &result.CatalogVersion, &importedAt, &categories, &packs)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Catalog{}, err
	}
	if err == nil {
		result.ImportedAt = &importedAt
	}
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM openfox.market_skills), (SELECT count(*) FROM openfox.market_templates)`).
		Scan(&result.Counts.Skills, &result.Counts.Templates); err != nil {
		return Catalog{}, err
	}
	if len(categories) > 0 {
		var raw []catalogCategory
		if err := json.Unmarshal(categories, &raw); err != nil {
			return Catalog{}, err
		}
		for _, item := range raw {
			if item.Name == nil {
				item.Name = Text{}
			}
			result.Categories = append(result.Categories, Category{
				ID: item.ID, Name: item.Name, Order: item.Order, SkillCount: item.SkillCount, TemplateCount: item.TemplateCount,
			})
		}
	}
	if len(packs) == 0 {
		return result, nil
	}
	var raw []catalogPack
	if err := json.Unmarshal(packs, &raw); err != nil {
		return Catalog{}, err
	}
	rows, err := tx.Query(ctx, `SELECT skill_id FROM openfox.market_skills`)
	if err != nil {
		return Catalog{}, err
	}
	defer rows.Close()
	available := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return Catalog{}, err
		}
		available[id] = true
	}
	if err := rows.Err(); err != nil {
		return Catalog{}, err
	}
	for _, item := range raw {
		pack := Pack{ID: item.ID, Name: item.Name, Tint: item.Tint, Members: []PackMember{}}
		if pack.Name == nil {
			pack.Name = Text{}
		}
		for _, member := range item.Members {
			name := member.Name
			if name == nil {
				name = Text{}
			}
			pack.Members = append(pack.Members, PackMember{MarketSkillID: member.SkillID, Name: name, Icon: member.Icon, Available: available[member.SkillID]})
		}
		result.Packs = append(result.Packs, pack)
	}
	return result, nil
}

// sortKeys 返回排序键表达式：A、B 为 bigint，T 为文本，全部按升序比较。
// 「降序」的键取负数，这样行比较 (ka, kb, kt, skill_id) > 游标 就能覆盖所有排序。
func sortKeys(sort string) (a, b, t string, ok bool) {
	switch sort {
	case "default":
		return `(NOT featured)::int::bigint`, `-downloads`, `''`, true
	case "downloads":
		return `-downloads`, `0::bigint`, `''`, true
	case "newest":
		return `-(floor(extract(epoch from imported_at) * 1000000))::bigint`, `0::bigint`, `''`, true
	case "name":
		return `0::bigint`, `0::bigint`, `(lower(COALESCE(NULLIF(name->>'zh-Hans', ''), NULLIF(name->>'en', ''), skill_id)) COLLATE "C")`, true
	}
	return "", "", "", false
}

// ListSkills 返回一页市场技能。ids 非空时按请求顺序返回这些技能（忽略其他筛选与分页）。
func ListSkills(ctx context.Context, tx pgx.Tx, query ListQuery) (SkillPage, error) {
	if len(query.IDs) > 0 {
		return listSkillsByIDs(ctx, tx, query.IDs)
	}
	sortName := query.Sort
	if sortName == "" {
		sortName = "default"
	}
	keyA, keyB, keyT, ok := sortKeys(sortName)
	if !ok {
		return SkillPage{}, ErrInvalidQuery
	}
	limit := query.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaxListLimit || utf8.RuneCountInString(query.Q) > MaxQueryRunes {
		return SkillPage{}, ErrInvalidQuery
	}
	where, args := " WHERE true", []any{}
	if query.Category != "" {
		args = append(args, query.Category)
		where += fmt.Sprintf(" AND $%d = ANY(category_ids)", len(args))
	}
	if query.Q != "" {
		args = append(args, likePattern(query.Q))
		where += fmt.Sprintf(` AND search_text ILIKE $%d ESCAPE '\'`, len(args))
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.market_skills`+where, args...).Scan(&total); err != nil {
		return SkillPage{}, err
	}
	after := ""
	if query.Cursor != "" {
		cursor, err := DecodeCursor(query.Cursor)
		if err != nil || cursor.Sort != sortName {
			return SkillPage{}, ErrInvalidCursor
		}
		args = append(args, cursor.A, cursor.B, cursor.T, cursor.ID)
		n := len(args)
		after = fmt.Sprintf(" WHERE (ka, kb, kt, skill_id) > ($%d::bigint, $%d::bigint, $%d::text, $%d::text)", n-3, n-2, n-1, n)
	}
	args = append(args, limit+1)
	sql := `SELECT ` + skillColumns + `, ka, kb, kt FROM (
		SELECT skill_id, version, name, description, icon, category_ids, tags, publisher, origin, featured, downloads, stats,
		       file_count, total_bytes, ` + keyA + ` AS ka, ` + keyB + ` AS kb, ` + keyT + ` AS kt
		  FROM openfox.market_skills` + where + `) x` + after + fmt.Sprintf(` ORDER BY ka, kb, kt, skill_id LIMIT $%d`, len(args))
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return SkillPage{}, err
	}
	defer rows.Close()
	page := SkillPage{Items: []Skill{}, Total: total}
	var last cursorPayload
	for rows.Next() {
		var key cursorPayload
		s, err := scanSkill(rows, &key.A, &key.B, &key.T)
		if err != nil {
			return SkillPage{}, err
		}
		if len(page.Items) == limit {
			// 多取的一行只用来判断还有没有下一页。
			cursor := EncodeCursor(last)
			page.NextCursor = &cursor
			break
		}
		key.Sort, key.ID = sortName, s.MarketSkillID
		last = key
		page.Items = append(page.Items, s)
	}
	return page, rows.Err()
}

func listSkillsByIDs(ctx context.Context, tx pgx.Tx, ids []string) (SkillPage, error) {
	if len(ids) > MaxIDs {
		return SkillPage{}, ErrInvalidQuery
	}
	found, err := loadSkills(ctx, tx, ids)
	if err != nil {
		return SkillPage{}, err
	}
	page := SkillPage{Items: []Skill{}}
	seen := map[string]bool{}
	for _, id := range ids {
		if skill, ok := found[id]; ok && !seen[id] {
			seen[id] = true
			page.Items = append(page.Items, skill)
		}
	}
	page.Total = len(page.Items)
	return page, nil
}

// loadSkills 按 ID 取一批技能。
func loadSkills(ctx context.Context, tx pgx.Tx, ids []string) (map[string]Skill, error) {
	rows, err := tx.Query(ctx, `SELECT `+skillColumns+` FROM openfox.market_skills WHERE skill_id = ANY($1::text[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]Skill{}
	for rows.Next() {
		s, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		result[s.MarketSkillID] = s
	}
	return result, rows.Err()
}

// GetSkill 返回技能详情与文件清单（SKILL.md 置顶，其余按路径排序）。
func GetSkill(ctx context.Context, tx pgx.Tx, id string) (SkillDetail, error) {
	if !IDPattern.MatchString(id) {
		return SkillDetail{}, ErrNotFound
	}
	skill, err := scanSkill(tx.QueryRow(ctx, `SELECT `+skillColumns+` FROM openfox.market_skills WHERE skill_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SkillDetail{}, ErrNotFound
	}
	if err != nil {
		return SkillDetail{}, err
	}
	records, err := loadFiles(ctx, tx, id)
	if err != nil {
		return SkillDetail{}, err
	}
	detail := SkillDetail{Skill: skill, Files: make([]FileEntry, len(records))}
	for i, record := range records {
		detail.Files[i] = FileEntry{Path: record.Path, SizeBytes: record.Size, Mime: record.Mime}
	}
	return detail, nil
}

// FileRecord 是市场文件记录（market_skill_files 的一行）。
type FileRecord struct {
	skills.Entry
	Mime string
}

func loadFiles(ctx context.Context, tx pgx.Tx, id string) ([]FileRecord, error) {
	rows, err := tx.Query(ctx, `SELECT path, file_key, size_bytes, sha256, mime FROM openfox.market_skill_files
		WHERE skill_id = $1 ORDER BY (path <> 'SKILL.md'), path COLLATE "C"`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	files := []FileRecord{}
	for rows.Next() {
		var f FileRecord
		if err := rows.Scan(&f.Path, &f.Key, &f.Size, &f.SHA256, &f.Mime); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// LoadFile 返回一个文件记录；技能或文件不存在返回 ErrNotFound / ErrFileNotFound。
func LoadFile(ctx context.Context, tx pgx.Tx, id, path string) (FileRecord, error) {
	if !IDPattern.MatchString(id) {
		return FileRecord{}, ErrNotFound
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.market_skills WHERE skill_id = $1)`, id).Scan(&exists); err != nil {
		return FileRecord{}, err
	}
	if !exists {
		return FileRecord{}, ErrNotFound
	}
	var f FileRecord
	err := tx.QueryRow(ctx, `SELECT path, file_key, size_bytes, sha256, mime FROM openfox.market_skill_files WHERE skill_id = $1 AND path = $2`,
		id, path).Scan(&f.Path, &f.Key, &f.Size, &f.SHA256, &f.Mime)
	if errors.Is(err, pgx.ErrNoRows) {
		return FileRecord{}, ErrFileNotFound
	}
	return f, err
}

// Source 是添加 / 更新用的市场技能内容：当前版本、名称与描述（多语言）和全部文件记录。
type Source struct {
	ID          string
	Version     string
	Name        Text
	Description Text
	// Label 是空间技能的 source_label：发布者名，没有时用 origin。
	Label string
	Files []FileRecord
}

// LoadSource 读取市场技能当前版本与文件记录；不存在返回 ErrNotFound。
func LoadSource(ctx context.Context, tx pgx.Tx, id string) (Source, error) {
	if !IDPattern.MatchString(id) {
		return Source{}, ErrNotFound
	}
	src := Source{ID: id}
	// One statement pairs a version with its exact manifest even while the market is refreshed.
	rows, err := tx.Query(ctx, `SELECT m.version,m.name,m.description,COALESCE(NULLIF(m.publisher->>'name',''),m.origin),
        f.path,f.file_key,f.size_bytes,f.sha256,f.mime
        FROM openfox.market_skills m JOIN openfox.market_skill_files f ON f.skill_id=m.skill_id
        WHERE m.skill_id=$1 ORDER BY (f.path <> 'SKILL.md'), f.path COLLATE "C"`, id)
	if err != nil {
		return Source{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var f FileRecord
		if err := rows.Scan(&src.Version, &src.Name, &src.Description, &src.Label, &f.Path, &f.Key, &f.Size, &f.SHA256, &f.Mime); err != nil {
			return Source{}, err
		}
		src.Files = append(src.Files, f)
	}
	if err := rows.Err(); err != nil {
		return Source{}, err
	}
	if len(src.Files) == 0 {
		return Source{}, ErrNotFound
	}
	return src, nil
}

const templateColumns = `template_id, version, name, job_title, tagline, description, category_ids, keywords, avatar,
       jsonb_array_length(skill_refs), origin`

func scanTemplate(row pgx.Row, extra ...any) (Template, error) {
	var t Template
	dest := append([]any{&t.MarketTemplateID, &t.Version, &t.Name, &t.JobTitle, &t.Tagline, &t.Description, &t.CategoryIDs,
		&t.Keywords, &t.Avatar, &t.SkillCount, &t.Origin}, extra...)
	err := row.Scan(dest...)
	for _, text := range []*Text{&t.Name, &t.JobTitle, &t.Tagline, &t.Description} {
		if *text == nil {
			*text = Text{}
		}
	}
	if t.CategoryIDs == nil {
		t.CategoryIDs = []string{}
	}
	if t.Keywords == nil {
		t.Keywords = []string{}
	}
	if t.Avatar == nil {
		t.Avatar = map[string]any{}
	}
	return t, err
}

// ListTemplates 返回一页模板（按 position、template_id）。
func ListTemplates(ctx context.Context, tx pgx.Tx, category, q string, limit int, cursorValue string) (TemplatePage, error) {
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaxListLimit || utf8.RuneCountInString(q) > MaxQueryRunes {
		return TemplatePage{}, ErrInvalidQuery
	}
	where, args := " WHERE true", []any{}
	if category != "" {
		args = append(args, category)
		where += fmt.Sprintf(" AND $%d = ANY(category_ids)", len(args))
	}
	if q != "" {
		args = append(args, likePattern(q))
		where += fmt.Sprintf(` AND search_text ILIKE $%d ESCAPE '\'`, len(args))
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.market_templates`+where, args...).Scan(&total); err != nil {
		return TemplatePage{}, err
	}
	if cursorValue != "" {
		cursor, err := DecodeCursor(cursorValue)
		if err != nil || cursor.Sort != "template" {
			return TemplatePage{}, ErrInvalidCursor
		}
		args = append(args, cursor.A, cursor.ID)
		where += fmt.Sprintf(` AND (position, template_id COLLATE "C") > ($%d::int, $%d::text)`, len(args)-1, len(args))
	}
	args = append(args, limit+1)
	rows, err := tx.Query(ctx, `SELECT `+templateColumns+`, position FROM openfox.market_templates`+where+
		fmt.Sprintf(` ORDER BY position, template_id COLLATE "C" LIMIT $%d`, len(args)), args...)
	if err != nil {
		return TemplatePage{}, err
	}
	defer rows.Close()
	page := TemplatePage{Items: []Template{}, Total: total}
	var last cursorPayload
	for rows.Next() {
		var position int64
		t, err := scanTemplate(rows, &position)
		if err != nil {
			return TemplatePage{}, err
		}
		if len(page.Items) == limit {
			cursor := EncodeCursor(last)
			page.NextCursor = &cursor
			break
		}
		last = cursorPayload{Sort: "template", A: position, ID: t.MarketTemplateID}
		page.Items = append(page.Items, t)
	}
	return page, rows.Err()
}

// templateRef 是模板里存放的技能引用。
type templateRef struct {
	SkillID string `json:"skillId"`
	Version string `json:"version"`
}

// TemplateRefs 返回模板引用的市场技能 ID（保持模板里的顺序，去重）。
func TemplateRefs(ctx context.Context, tx pgx.Tx, id string) ([]string, error) {
	if !IDPattern.MatchString(id) {
		return nil, ErrNotFound
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT skill_refs FROM openfox.market_templates WHERE template_id = $1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var refs []templateRef
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		if IDPattern.MatchString(ref.SkillID) && !seen[ref.SkillID] {
			seen[ref.SkillID] = true
			ids = append(ids, ref.SkillID)
		}
	}
	return ids, nil
}

// GetTemplate 返回模板详情：各语言指令、快速提示与引用的技能（标出本平台目录里有没有）。
func GetTemplate(ctx context.Context, tx pgx.Tx, id string) (TemplateDetail, error) {
	if !IDPattern.MatchString(id) {
		return TemplateDetail{}, ErrNotFound
	}
	var instructions, quick, refsRaw []byte
	t, err := scanTemplate(tx.QueryRow(ctx, `SELECT `+templateColumns+`, instructions, quick_prompts, skill_refs
		FROM openfox.market_templates WHERE template_id = $1`, id), &instructions, &quick, &refsRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return TemplateDetail{}, ErrNotFound
	}
	if err != nil {
		return TemplateDetail{}, err
	}
	detail := TemplateDetail{Template: t, Instructions: Text{}, QuickPrompts: []Text{}, Skills: []SkillRef{}}
	var files map[string]string
	if err := json.Unmarshal(instructions, &files); err != nil {
		return TemplateDetail{}, err
	}
	// 键形如 instructions.zh-Hans.md → 语言 zh-Hans。
	for key, value := range files {
		locale := strings.TrimSuffix(strings.TrimPrefix(key, "instructions."), ".md")
		if locale != "" {
			detail.Instructions[locale] = value
		}
	}
	if err := json.Unmarshal(quick, &detail.QuickPrompts); err != nil {
		return TemplateDetail{}, err
	}
	var refs []templateRef
	if err := json.Unmarshal(refsRaw, &refs); err != nil {
		return TemplateDetail{}, err
	}
	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.SkillID
	}
	found, err := loadSkills(ctx, tx, ids)
	if err != nil {
		return TemplateDetail{}, err
	}
	for _, ref := range refs {
		item := SkillRef{MarketSkillID: ref.SkillID, Version: ref.Version}
		if skill, ok := found[ref.SkillID]; ok {
			skill := skill
			item.Available, item.Skill = true, &skill
		}
		detail.Skills = append(detail.Skills, item)
	}
	return detail, nil
}

// LatestVersion 返回市场技能当前版本，不存在返回 ErrNotFound。
func LatestVersion(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var version string
	err := tx.QueryRow(ctx, `SELECT version FROM openfox.market_skills WHERE skill_id = $1`, id).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return version, err
}
