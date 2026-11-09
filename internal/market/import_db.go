package market

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/collaboration"
	"openfox/internal/skills"
)

// ImportOptions 是导入选项。
type ImportOptions struct {
	// Source 写入 market_catalog.source，标识导入数据的来源。
	Source string
	// Prune 删除库里有、而这次数据（selected.json / templates.json）里没有的技能与模板。
	Prune bool
	// DryRun 只解析和比较，不写存储也不写库。
	DryRun bool
}

// Report 是导入汇总。不含文件内容。
type Report struct {
	DryRun             bool   `json:"dry_run"`
	SkillsImported     int    `json:"skills_imported"`
	SkillsUpdated      int    `json:"skills_updated"`
	SkillsUnchanged    int    `json:"skills_unchanged"`
	SkillsDeleted      int    `json:"skills_deleted"`
	TemplatesImported  int    `json:"templates_imported"`
	TemplatesUpdated   int    `json:"templates_updated"`
	TemplatesUnchanged int    `json:"templates_unchanged"`
	TemplatesDeleted   int    `json:"templates_deleted"`
	SkippedSkills      []Skip `json:"skipped_skills"`
	SkippedTemplates   []Skip `json:"skipped_templates"`
}

type storedFile struct {
	skills.Entry
	Mime string
}

// Import 把解析后的数据写入市场目录。连接后以 openfox_migrator 身份执行（市场表只有它能写）。
// 顺序：事务前把新增 / 新版本技能的文件写入存储；一个事务里写库；提交后删除被替换的旧存储对象。
// 事务失败时清理本次写入的存储对象。导入是幂等的：同 ID 同版本跳过。
func Import(ctx context.Context, conn *pgx.Conn, storage artifacts.Storage, data *ImportData, opts ImportOptions) (Report, error) {
	report := Report{DryRun: opts.DryRun, SkippedSkills: data.SkippedSkills, SkippedTemplates: data.SkippedTemplate}
	if report.SkippedSkills == nil {
		report.SkippedSkills = []Skip{}
	}
	if report.SkippedTemplates == nil {
		report.SkippedTemplates = []Skip{}
	}
	if storage == nil && !opts.DryRun {
		return report, errors.New("market import requires file storage (OPENFOX_STORAGE_ROOT)")
	}
	if opts.Prune && (len(data.SelectedIDs) == 0 || len(data.TemplateIDs) == 0) {
		return report, errors.New("refusing to prune: the import data has no skills or no templates")
	}
	if opts.Source == "" {
		opts.Source = "builtin"
	}
	if _, err := conn.Exec(ctx, `SET ROLE openfox_migrator`); err != nil {
		return report, fmt.Errorf("set role openfox_migrator: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `RESET ROLE`) }()

	skillVersions, err := loadVersions(ctx, conn, `SELECT skill_id, version FROM openfox.market_skills`)
	if err != nil {
		return report, err
	}
	templateVersions, err := loadVersions(ctx, conn, `SELECT template_id, version FROM openfox.market_templates`)
	if err != nil {
		return report, err
	}
	var changedSkills []importSkill
	for _, skill := range data.Skills {
		current, exists := skillVersions[skill.ID]
		switch {
		case exists && current == skill.Version:
			report.SkillsUnchanged++
		case exists:
			report.SkillsUpdated++
			changedSkills = append(changedSkills, skill)
		default:
			report.SkillsImported++
			changedSkills = append(changedSkills, skill)
		}
	}
	var changedTemplates []importTemplate
	for _, template := range data.Templates {
		current, exists := templateVersions[template.ID]
		switch {
		case exists && current == template.Version:
			report.TemplatesUnchanged++
		case exists:
			report.TemplatesUpdated++
			changedTemplates = append(changedTemplates, template)
		default:
			report.TemplatesImported++
			changedTemplates = append(changedTemplates, template)
		}
	}
	selected, listed := stringSet(data.SelectedIDs), stringSet(data.TemplateIDs)
	if opts.Prune {
		for id := range skillVersions {
			if !selected[id] {
				report.SkillsDeleted++
			}
		}
		for id := range templateVersions {
			if !listed[id] {
				report.TemplatesDeleted++
			}
		}
	}
	if opts.DryRun {
		return report, nil
	}

	// 事务前：写存储。
	files := map[string][]storedFile{}
	var created []string
	cleanup := func() { skills.DeleteKeys(context.Background(), storage, created) }
	for _, skill := range changedSkills {
		for _, file := range skill.Files {
			content, err := readAll(file)
			if err != nil {
				cleanup()
				return report, fmt.Errorf("read %s/%s: %w", skill.ID, file.Path, err)
			}
			objectID, err := collaboration.NewID()
			if err != nil {
				cleanup()
				return report, err
			}
			key := "market/" + skill.ID + "/" + objectID
			// 空文件也要能写入：Put 的上限必须大于 0。
			if _, err := storage.Put(ctx, key, bytes.NewReader(content), int64(len(content))+1); err != nil {
				cleanup()
				return report, fmt.Errorf("store %s/%s: %w", skill.ID, file.Path, err)
			}
			created = append(created, key)
			files[skill.ID] = append(files[skill.ID], storedFile{
				Entry: skills.Entry{Path: file.Path, Key: key, Size: int64(len(content)), SHA256: file.SHA256}, Mime: file.Mime,
			})
		}
	}

	removed, err := writeCatalog(ctx, conn, data, opts, changedSkills, changedTemplates, files, selected, listed)
	if err != nil {
		cleanup()
		return report, err
	}
	skills.DeleteKeys(ctx, storage, removed)
	return report, nil
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func loadVersions(ctx context.Context, conn *pgx.Conn, sql string) (map[string]string, error) {
	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := map[string]string{}
	for rows.Next() {
		var id, version string
		if err := rows.Scan(&id, &version); err != nil {
			return nil, err
		}
		versions[id] = version
	}
	return versions, rows.Err()
}

func jsonText(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// writeCatalog 在一个事务里写库，返回提交后可删除的旧存储键。
func writeCatalog(ctx context.Context, conn *pgx.Conn, data *ImportData, opts ImportOptions, changedSkills []importSkill,
	changedTemplates []importTemplate, files map[string][]storedFile, selected, listed map[string]bool) ([]string, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// 同一时间只允许一个导入。
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('openfox.market.import', 0))`); err != nil {
		return nil, err
	}
	var removed []string
	collectKeys := func(sql string, args ...any) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			removed = append(removed, key)
		}
		return rows.Err()
	}
	for _, skill := range changedSkills {
		if err := collectKeys(`SELECT file_key FROM openfox.market_skill_files WHERE skill_id = $1`, skill.ID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM openfox.market_skill_files WHERE skill_id = $1`, skill.ID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.market_skills (skill_id, version, digest, name, description, icon, category_ids, tags, stats, publisher,
			                                   origin, featured, downloads, file_count, total_bytes, search_text)
			VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7, $8::jsonb, $9::jsonb, $10::jsonb, $11, $12, $13, $14, $15, $16)
			ON CONFLICT (skill_id) DO UPDATE SET
			    version = EXCLUDED.version, digest = EXCLUDED.digest, name = EXCLUDED.name, description = EXCLUDED.description,
			    icon = EXCLUDED.icon, category_ids = EXCLUDED.category_ids, tags = EXCLUDED.tags, stats = EXCLUDED.stats,
			    publisher = EXCLUDED.publisher, origin = EXCLUDED.origin, featured = EXCLUDED.featured,
			    downloads = EXCLUDED.downloads, file_count = EXCLUDED.file_count, total_bytes = EXCLUDED.total_bytes,
			    search_text = EXCLUDED.search_text, imported_at = clock_timestamp()`,
			skill.ID, skill.Version, skill.Digest, jsonText(skill.Name), jsonText(skill.Description), skill.Icon, skill.CategoryIDs,
			jsonText(skill.Tags), jsonText(skill.Stats), jsonText(skill.Publisher), skill.Origin, skill.Featured, skill.Downloads,
			len(skill.Files), skill.TotalBytes, skill.SearchText); err != nil {
			return nil, fmt.Errorf("write skill %s: %w", skill.ID, err)
		}
		for _, file := range files[skill.ID] {
			if _, err := tx.Exec(ctx, `INSERT INTO openfox.market_skill_files (skill_id, path, file_key, size_bytes, sha256, mime)
				VALUES ($1, $2, $3, $4, $5, $6)`, skill.ID, file.Path, file.Key, file.Size, file.SHA256, file.Mime); err != nil {
				return nil, fmt.Errorf("write file %s/%s: %w", skill.ID, file.Path, err)
			}
		}
	}
	for _, template := range changedTemplates {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.market_templates (template_id, version, name, job_title, tagline, description, category_ids, keywords, avatar,
			                                      quick_prompts, instructions, skill_refs, origin, position, published_at, search_text)
			VALUES ($1, $2, $3::jsonb, $4::jsonb, $5::jsonb, $6::jsonb, $7, $8, $9::jsonb, $10::jsonb, $11::jsonb, $12::jsonb, $13, $14, $15, $16)
			ON CONFLICT (template_id) DO UPDATE SET
			    version = EXCLUDED.version, name = EXCLUDED.name, job_title = EXCLUDED.job_title, tagline = EXCLUDED.tagline,
			    description = EXCLUDED.description, category_ids = EXCLUDED.category_ids, keywords = EXCLUDED.keywords,
			    avatar = EXCLUDED.avatar, quick_prompts = EXCLUDED.quick_prompts, instructions = EXCLUDED.instructions,
			    skill_refs = EXCLUDED.skill_refs, origin = EXCLUDED.origin, position = EXCLUDED.position,
			    published_at = EXCLUDED.published_at, search_text = EXCLUDED.search_text, imported_at = clock_timestamp()`,
			template.ID, template.Version, jsonText(template.Name), jsonText(template.JobTitle), jsonText(template.Tagline),
			jsonText(template.Description), template.CategoryIDs, template.Keywords, jsonText(template.Avatar),
			jsonText(nonNilTexts(template.QuickPrompts)), jsonText(template.Instructions), jsonText(template.SkillRefs),
			template.Origin, template.Position, template.PublishedAt, template.SearchText); err != nil {
			return nil, fmt.Errorf("write template %s: %w", template.ID, err)
		}
	}
	if opts.Prune {
		if err := collectKeys(`SELECT file_key FROM openfox.market_skill_files WHERE skill_id <> ALL($1::text[])`, mapKeys(selected)); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM openfox.market_skills WHERE skill_id <> ALL($1::text[])`, mapKeys(selected)); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM openfox.market_templates WHERE template_id <> ALL($1::text[])`, mapKeys(listed)); err != nil {
			return nil, err
		}
	}

	// 分类数量按本平台实际导入的数量重算。
	skillCounts, err := categoryCounts(ctx, tx, `SELECT c, count(*) FROM openfox.market_skills, unnest(category_ids) AS c GROUP BY c`)
	if err != nil {
		return nil, err
	}
	templateCounts, err := categoryCounts(ctx, tx, `SELECT c, count(*) FROM openfox.market_templates, unnest(category_ids) AS c GROUP BY c`)
	if err != nil {
		return nil, err
	}
	categories := make([]map[string]any, 0, len(data.Categories))
	for _, category := range data.Categories {
		copied := map[string]any{}
		for key, value := range category {
			copied[key] = value
		}
		id, _ := copied["id"].(string)
		copied["skillCount"], copied["templateCount"] = skillCounts[id], templateCounts[id]
		categories = append(categories, copied)
	}
	// 目录没变时不改 imported_at（重复导入不产生变化）。
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.market_catalog (id, source, catalog_version, digest, categories, packs)
		VALUES (1, $1, $2, $3, $4::jsonb, $5::jsonb)
		ON CONFLICT (id) DO UPDATE SET source = EXCLUDED.source, catalog_version = EXCLUDED.catalog_version,
		    digest = EXCLUDED.digest, categories = EXCLUDED.categories, packs = EXCLUDED.packs, imported_at = clock_timestamp()
		 WHERE (market_catalog.source, market_catalog.catalog_version, market_catalog.digest, market_catalog.categories, market_catalog.packs)
		       IS DISTINCT FROM (EXCLUDED.source, EXCLUDED.catalog_version, EXCLUDED.digest, EXCLUDED.categories, EXCLUDED.packs)`,
		opts.Source, data.CatalogVersion, data.Digest, jsonText(categories), string(data.Packs)); err != nil {
		return nil, fmt.Errorf("write catalog: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return removed, nil
}

func nonNilTexts(values []Text) []Text {
	if values == nil {
		return []Text{}
	}
	return values
}

func mapKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	return keys
}

func categoryCounts(ctx context.Context, tx pgx.Tx, sql string) (map[string]int, error) {
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		counts[id] = count
	}
	return counts, rows.Err()
}
