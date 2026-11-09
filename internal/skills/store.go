package skills

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

// 数据读写。所有语句都在租户事务里执行，依赖 RLS 限定组织；空间读写权限由调用方（API 层）先判断。

// visibleAgentSQL 限定「使用者」只包含调用者看得到的 Agent：未归档，且共享或是自己的私有 Agent。$2 为调用者。
const visibleAgentSQL = `ag.archived_at IS NULL AND (ag.visibility = 'SHARED' OR ag.owner_user_id = $2::uuid)`

const skillColumns = `
SELECT s.id::text, s.space_id::text, s.name, s.description, s.source_kind, s.source_label, s.source_url,
       s.file_count, s.total_bytes, s.created_by::text, COALESCE(NULLIF(u.full_name, ''), u.email),
       s.version, s.created_at, s.updated_at,
       (SELECT count(*) FROM openfox.agent_skill_loadout x
          JOIN openfox.agents ag ON ag.organization_id = x.organization_id AND ag.id = x.agent_id
         WHERE x.organization_id = s.organization_id AND x.id = s.id AND ` + visibleAgentSQL + `) AS used_count,
       s.market_skill_id, s.market_version,
       (SELECT m.version FROM openfox.market_skills m WHERE m.skill_id = s.market_skill_id) AS market_latest_version`

const skillFrom = `
  FROM openfox.skills s
  JOIN openfox.users u ON u.id = s.created_by
 WHERE s.organization_id = openfox_private.current_organization_id() AND s.space_id = $1::uuid`

func scanSkill(row pgx.Row, agentList bool) (Skill, error) {
	var s Skill
	dest := []any{&s.ID, &s.SpaceID, &s.Name, &s.Description, &s.SourceKind, &s.SourceLabel, &s.SourceURL,
		&s.FileCount, &s.TotalBytes, &s.CreatedBy.ID, &s.CreatedBy.Name, &s.Version, &s.CreatedAt, &s.UpdatedAt, &s.UsedCount,
		&s.MarketSkillID, &s.MarketVersion, &s.MarketLatestVersion}
	if agentList {
		dest = append(dest, &s.AgentOwned)
	}
	err := row.Scan(dest...)
	s.UsedBy = []AgentRef{}
	return s, err
}

// likePattern 把搜索词转成 ILIKE 的包含匹配，转义 \ % _。
func likePattern(q string) string {
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	return "%" + q + "%"
}

// List 返回空间的技能（最多 500 个）。sort：usage = 使用它的 Agent 数，updated / created 为时间倒序。
func List(ctx context.Context, tx pgx.Tx, spaceID, viewerID string, query ListQuery) ([]Skill, error) {
	sql := skillColumns + skillFrom
	args := []any{spaceID, viewerID}
	if query.CreatedBy != "" {
		args = append(args, query.CreatedBy)
		sql += ` AND s.created_by = $3::uuid`
	}
	if query.Q != "" {
		args = append(args, likePattern(query.Q))
		sql += ` AND (s.name ILIKE $` + itoa(len(args)) + ` OR s.description ILIKE $` + itoa(len(args)) + `)`
	}
	switch query.Sort {
	case "usage":
		sql += ` ORDER BY used_count DESC, s.updated_at DESC, s.id`
	case "created":
		sql += ` ORDER BY s.created_at DESC, s.id`
	default:
		sql += ` ORDER BY s.updated_at DESC, s.id`
	}
	sql += ` LIMIT ` + itoa(MaxPerSpace)
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Skill{}
	for rows.Next() {
		item, err := scanSkill(rows, false)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, fillUsedBy(ctx, tx, items, viewerID)
}

func itoa(n int) string { return strconv.Itoa(n) }

// fillUsedBy 为一批技能填上 used_by（最多 3 个 Agent，按装配顺序）。
func fillUsedBy(ctx context.Context, tx pgx.Tx, items []Skill, viewerID string) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	refs, err := agentRefs(ctx, tx, ids, viewerID, 3)
	if err != nil {
		return err
	}
	for i := range items {
		if list, ok := refs[items[i].ID]; ok {
			items[i].UsedBy = list
		}
	}
	return nil
}

// agentRefs 返回每个技能的使用者（每个技能最多 limit 个）。
func agentRefs(ctx context.Context, tx pgx.Tx, skillIDs []string, viewerID string, limit int) (map[string][]AgentRef, error) {
	rows, err := tx.Query(ctx, `
		SELECT skill_id, id, name, avatar FROM (
			SELECT x.id::text AS skill_id, ag.id::text AS id, ag.name, ag.avatar,
			       row_number() OVER (PARTITION BY x.id ORDER BY x.attached_at, ag.name, ag.id) AS rn
			  FROM openfox.agent_skill_loadout x
			  JOIN openfox.agents ag ON ag.organization_id = x.organization_id AND ag.id = x.agent_id
			 WHERE x.organization_id = openfox_private.current_organization_id()
			   AND x.id = ANY($1::uuid[]) AND `+visibleAgentSQL+`
		) t WHERE rn <= $3 ORDER BY skill_id, rn`, skillIDs, viewerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string][]AgentRef{}
	for rows.Next() {
		var skillID string
		var ref AgentRef
		var avatar []byte
		if err := rows.Scan(&skillID, &ref.ID, &ref.Name, &avatar); err != nil {
			return nil, err
		}
		if len(avatar) > 0 {
			ref.Avatar = avatar
		}
		result[skillID] = append(result[skillID], ref)
	}
	return result, rows.Err()
}

// Get 返回技能详情：文件清单（SKILL.md 置顶，其余按路径）与使用它的全部 Agent（最多 100 个）。
func Get(ctx context.Context, tx pgx.Tx, spaceID, skillID, viewerID string) (Detail, error) {
	if !membership.ValidateUUID(skillID) {
		return Detail{}, ErrNotFound
	}
	skill, err := scanSkill(tx.QueryRow(ctx, skillColumns+skillFrom+` AND s.id = $3::uuid`, spaceID, viewerID, skillID), false)
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	entries, err := loadEntries(ctx, tx, skillID)
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{Skill: skill, Files: make([]FileEntry, len(entries)), Agents: []AgentRef{}}
	for i, entry := range entries {
		detail.Files[i] = FileEntry{Path: entry.Path, SizeBytes: entry.Size}
	}
	refs, err := agentRefs(ctx, tx, []string{skillID}, viewerID, 100)
	if err != nil {
		return Detail{}, err
	}
	if list := refs[skillID]; list != nil {
		detail.Agents = list
		detail.UsedBy = list[:min(len(list), 3)]
	}
	return detail, nil
}

// loadEntries 读取技能的文件记录，SKILL.md 置顶，其余按路径排序。
func loadEntries(ctx context.Context, tx pgx.Tx, skillID string) ([]Entry, error) {
	rows, err := tx.Query(ctx, `
		SELECT path, file_key, size_bytes, sha256 FROM openfox.skill_files
		 WHERE organization_id = openfox_private.current_organization_id() AND skill_id = $1::uuid
		 ORDER BY (path <> 'SKILL.md'), path COLLATE "C"`, skillID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		var entry Entry
		if err := rows.Scan(&entry.Path, &entry.Key, &entry.Size, &entry.SHA256); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// LoadEntry 读取一个文件记录；技能或文件不存在返回 ErrNotFound / ErrFileNotFound。
func LoadEntry(ctx context.Context, tx pgx.Tx, spaceID, skillID, path string) (Entry, error) {
	if !membership.ValidateUUID(skillID) {
		return Entry{}, ErrNotFound
	}
	var filePath, key, digest *string
	var size *int64
	err := tx.QueryRow(ctx, `
		SELECT f.path, f.file_key, f.size_bytes, f.sha256
		  FROM openfox.skills s
		  LEFT JOIN openfox.skill_files f ON f.organization_id = s.organization_id AND f.skill_id = s.id AND f.path = $3
		 WHERE s.organization_id = openfox_private.current_organization_id() AND s.space_id = $1::uuid AND s.id = $2::uuid`,
		spaceID, skillID, path).Scan(&filePath, &key, &size, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	if filePath == nil {
		// 技能存在但没有这个文件（LEFT JOIN 得到 NULL 列）。
		return Entry{}, ErrFileNotFound
	}
	return Entry{Path: *filePath, Key: *key, Size: *size, SHA256: *digest}, nil
}

// Entries 返回技能的全部文件记录（供设备下载使用）。
func Entries(ctx context.Context, tx pgx.Tx, skillID string) ([]Entry, string, error) {
	var name string
	var ownedEntries []Entry
	err := tx.QueryRow(ctx, `SELECT name,NULL::jsonb FROM openfox.skills
     WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid
     UNION ALL SELECT name,files FROM openfox.agent_market_skill_installs
     WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, skillID).Scan(&name, &ownedEntries)
	if err == nil && ownedEntries != nil {
		return ownedEntries, name, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	entries, err := loadEntries(ctx, tx, skillID)
	return entries, name, err
}

// Meta 是写入前需要的技能当前状态。
type Meta struct {
	ID          string
	CreatedBy   string
	Version     int64
	Name        string
	Description string
	Entries     []Entry
	// 来自市场时的来源技能与版本。
	MarketSkillID *string
	MarketVersion *string
}

// Lock 锁定技能行（FOR UPDATE）并读出当前状态；forUpdate 为 false 时只读。
func Lock(ctx context.Context, tx pgx.Tx, spaceID, skillID string, forUpdate bool) (Meta, error) {
	if !membership.ValidateUUID(skillID) {
		return Meta{}, ErrNotFound
	}
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	var meta Meta
	err := tx.QueryRow(ctx, `
		SELECT id::text, created_by::text, version, name, description, market_skill_id, market_version FROM openfox.skills
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid`+lock,
		spaceID, skillID).Scan(&meta.ID, &meta.CreatedBy, &meta.Version, &meta.Name, &meta.Description, &meta.MarketSkillID, &meta.MarketVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return Meta{}, ErrNotFound
	}
	if err != nil {
		return Meta{}, err
	}
	meta.Entries, err = loadEntries(ctx, tx, skillID)
	return meta, err
}

// isNameTaken 判断是否违反同一空间内名称唯一。
func isNameTaken(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "skills_name_unique"
}

func insertFiles(ctx context.Context, tx pgx.Tx, skillID string, entries []Entry) error {
	paths := make([]string, len(entries))
	keys := make([]string, len(entries))
	sizes := make([]int64, len(entries))
	digests := make([]string, len(entries))
	for i, entry := range entries {
		paths[i], keys[i], sizes[i], digests[i] = entry.Path, entry.Key, entry.Size, entry.SHA256
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.skill_files (organization_id, skill_id, path, file_key, size_bytes, sha256)
		SELECT openfox_private.current_organization_id(), $1::uuid, t.p, t.k, t.sz, t.h
		  FROM unnest($2::text[], $3::text[], $4::bigint[], $5::text[]) AS t(p, k, sz, h)`,
		skillID, paths, keys, sizes, digests)
	return err
}

// CreateInput 是新建技能的输入；Entries 是已写入存储的文件记录。
type CreateInput struct {
	ID      string
	SpaceID string
	UserID  string
	Source  Source
	Package Package
	Entries []Entry
}

// LockSpaceCatalog serializes the look-up and creation of a space's market copy.
func LockSpaceCatalog(ctx context.Context, tx pgx.Tx, spaceID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
		'openfox.skills:' || openfox_private.current_organization_id()::text || ':' || $1, 0))`, spaceID)
	return err
}

// Create 新建技能。先取空间级 advisory lock，再计数，每个空间最多 500 个；同名返回 ErrNameTaken。
func Create(ctx context.Context, tx pgx.Tx, in CreateInput) error {
	if err := LockSpaceCatalog(ctx, tx, in.SpaceID); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.skills
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid`, in.SpaceID).Scan(&count); err != nil {
		return err
	}
	if count >= MaxPerSpace {
		return ErrLimit
	}
	if in.Source.MarketSkillID != nil {
		// 同一空间同一市场技能只添加一次；持有空间级锁，并发添加不会重复。
		if id, found, err := FindMarket(ctx, tx, in.SpaceID, *in.Source.MarketSkillID); err != nil {
			return err
		} else if found {
			return ErrMarketExists.WithDetails(map[string]any{"skill_id": id})
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.skills (organization_id, id, space_id, name, description, source_kind, source_label, source_url,
		                            file_count, total_bytes, created_by, updated_by,
		                            market_skill_id, market_version)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10::uuid, $10::uuid, $11, $12)`,
		in.ID, in.SpaceID, in.Package.Name, in.Package.Description, in.Source.Kind, in.Source.Label, in.Source.URL,
		len(in.Entries), in.Package.TotalBytes, in.UserID, in.Source.MarketSkillID, in.Source.MarketVersion)
	if isNameTaken(err) {
		return ErrNameTaken
	}
	if err != nil {
		return err
	}
	return insertFiles(ctx, tx, in.ID, in.Entries)
}

// Change 是保存修改里的一项：删除，或用 Entry（内容已写入存储）覆盖 / 新增。
type Change struct {
	Path   string
	Delete bool
	Entry  Entry
}

// ApplyChanges 把修改应用到现有文件清单，返回新的清单。校验：路径合法且不重复、不能删除 SKILL.md、
// 删除的文件必须存在、合计 ≤ 100 个文件和 5 MB、文件与目录不同名。
func ApplyChanges(existing []Entry, changes []Change) ([]Entry, error) {
	byPath := make(map[string]Entry, len(existing)+len(changes))
	for _, entry := range existing {
		byPath[entry.Path] = entry
	}
	seen := map[string]struct{}{}
	for _, change := range changes {
		if err := ValidatePath(change.Path); err != nil {
			return nil, err
		}
		if _, dup := seen[change.Path]; dup {
			return nil, ErrInvalid
		}
		seen[change.Path] = struct{}{}
		if change.Delete {
			if change.Path == SkillFile {
				return nil, ErrDeleteSkillMD
			}
			if _, ok := byPath[change.Path]; !ok {
				return nil, ErrFileNotFound
			}
			delete(byPath, change.Path)
			continue
		}
		byPath[change.Path] = change.Entry
	}
	if len(byPath) > MaxFiles {
		return nil, ErrTooLarge
	}
	result := make([]Entry, 0, len(byPath))
	var total int64
	for _, entry := range byPath {
		total += entry.Size
		result = append(result, entry)
	}
	if total > MaxTotalBytes {
		return nil, ErrTooLarge
	}
	slices.SortFunc(result, func(a, b Entry) int {
		if (a.Path == SkillFile) != (b.Path == SkillFile) {
			if a.Path == SkillFile {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	dirs := map[string]struct{}{}
	for _, entry := range result {
		for i := 0; i < len(entry.Path); i++ {
			if entry.Path[i] == '/' {
				dirs[entry.Path[:i]] = struct{}{}
			}
		}
	}
	for _, entry := range result {
		if _, clash := dirs[entry.Path]; clash {
			return nil, ErrInvalid
		}
	}
	return result, nil
}

// ReplaceInput 是保存修改的输入。调用方已在同一事务里 Lock（FOR UPDATE）并核对了权限与版本。
type ReplaceInput struct {
	SpaceID     string
	SkillID     string
	UserID      string
	Name        string
	Description string
	Entries     []Entry
	// MarketVersion 非空时同时更新 market_version（从市场更新）。
	MarketVersion *string
}

// Replace 整体替换文件清单并更新名称、描述与版本；返回不再被引用的旧存储键（提交后尽力删除）。
func Replace(ctx context.Context, tx pgx.Tx, meta Meta, in ReplaceInput) ([]string, error) {
	var total int64
	keep := make(map[string]struct{}, len(in.Entries))
	for _, entry := range in.Entries {
		total += entry.Size
		keep[entry.Key] = struct{}{}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.skills
		   SET name = $3, description = $4, file_count = $5, total_bytes = $6, updated_by = $7::uuid,
		       market_version = COALESCE($8, market_version),
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid`,
		in.SpaceID, in.SkillID, in.Name, in.Description, len(in.Entries), total, in.UserID, in.MarketVersion)
	if isNameTaken(err) {
		return nil, ErrNameTaken
	}
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM openfox.skill_files
		WHERE organization_id = openfox_private.current_organization_id() AND skill_id = $1::uuid`, in.SkillID); err != nil {
		return nil, err
	}
	if err := insertFiles(ctx, tx, in.SkillID, in.Entries); err != nil {
		return nil, err
	}
	var removed []string
	for _, entry := range meta.Entries {
		if _, ok := keep[entry.Key]; !ok {
			removed = append(removed, entry.Key)
		}
	}
	return removed, nil
}

// CheckVersion 在已锁定的状态上核对乐观锁版本。
func (m Meta) CheckVersion(version int64) error {
	if m.Version != version {
		return collaboration.ErrVersionConflict
	}
	return nil
}

// Remove 删除技能（文件记录与 Agent 装配随外键级联）；返回需要删除的存储键。
func Remove(ctx context.Context, tx pgx.Tx, spaceID string, meta Meta) ([]string, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM openfox.skills
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid`, spaceID, meta.ID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, ErrNotFound
	}
	keys := make([]string, len(meta.Entries))
	for i, entry := range meta.Entries {
		keys[i] = entry.Key
	}
	return keys, nil
}

// ListForAgent 返回 Agent 装配的技能（按 position）。
func ListForAgent(ctx context.Context, tx pgx.Tx, spaceID, agentID, viewerID string) ([]Skill, error) {
	rows, err := tx.Query(ctx, skillColumns+`, s.agent_owned
          FROM openfox.agent_skill_loadout s JOIN openfox.users u ON u.id=s.created_by
          WHERE s.organization_id=openfox_private.current_organization_id() AND s.space_id=$1::uuid AND s.agent_id=$3::uuid
          ORDER BY s.position,s.attached_at,s.name`, spaceID, viewerID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Skill{}
	for rows.Next() {
		item, err := scanSkill(rows, true)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, fillUsedBy(ctx, tx, items, viewerID)
}

// SetForAgent 整体替换 Agent 装配的技能。调用方已锁定并校验 Agent 可编辑。
// 最多 20 个、互不相同、全部属于该空间；顺序即 position。
func SetForAgent(ctx context.Context, tx pgx.Tx, spaceID, agentID, userID string, skillIDs []string) ([]string, error) {
	if len(skillIDs) > MaxAgentSkills {
		return nil, ErrTooManySkills
	}
	ids := make([]string, 0, len(skillIDs))
	seen := map[string]bool{}
	for _, id := range skillIDs {
		id = strings.ToLower(id)
		if !membership.ValidateUUID(id) || seen[id] {
			return nil, ErrSkillRefs
		}
		ids = append(ids, id)
		seen[id] = true
	}
	current, err := AgentSkillIDs(ctx, tx, agentID)
	if err != nil || slices.Equal(current, ids) {
		return nil, err
	}
	var count, names int
	err = tx.QueryRow(ctx, `SELECT count(*),count(DISTINCT name) FROM (
   SELECT name FROM openfox.skills WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND id=ANY($3::uuid[])
   UNION ALL SELECT name FROM openfox.agent_market_skill_installs WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND agent_id=$2::uuid AND id=ANY($3::uuid[])
 ) candidates`, spaceID, agentID, ids).Scan(&count, &names)
	if err != nil {
		return nil, err
	}
	if count != len(ids) {
		return nil, ErrSkillRefs
	}
	if names != count {
		return nil, ErrAgentNameTaken
	}
	// Uninstall only this Agent's omitted snapshots. Delete physical files only after commit.
	rows, err := tx.Query(ctx, `DELETE FROM openfox.agent_market_skill_installs WHERE organization_id=openfox_private.current_organization_id()
 AND agent_id=$1::uuid AND NOT(id=ANY($2::uuid[])) RETURNING files`, agentID, ids)
	if err != nil {
		return nil, err
	}
	var removed []string
	for rows.Next() {
		var files []Entry
		if err := rows.Scan(&files); err != nil {
			rows.Close()
			return nil, err
		}
		for _, f := range files {
			removed = append(removed, f.Key)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.agent_market_skill_installs i SET position=t.ord-1
   FROM unnest($2::uuid[]) WITH ORDINALITY AS t(id,ord)
   WHERE i.organization_id=openfox_private.current_organization_id() AND i.agent_id=$1::uuid AND i.id=t.id AND i.position<>t.ord-1`, agentID, ids); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM openfox.agent_skills WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1::uuid AND NOT(skill_id=ANY($2::uuid[]))`, agentID, ids); err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.agent_skills (organization_id,agent_id,skill_id,space_id,position,created_by)
   SELECT s.organization_id,$1::uuid,s.id,s.space_id,t.ord-1,$3::uuid
   FROM unnest($4::uuid[]) WITH ORDINALITY AS t(id,ord) JOIN openfox.skills s ON s.id=t.id
   WHERE s.organization_id=openfox_private.current_organization_id() AND s.space_id=$2::uuid
   ON CONFLICT (organization_id,agent_id,skill_id) DO UPDATE SET position=EXCLUDED.position
   WHERE openfox.agent_skills.position<>EXCLUDED.position`, agentID, spaceID, userID, ids)
	return removed, err
}

// FindMarket 返回空间里来自指定市场技能的技能 ID。
func FindMarket(ctx context.Context, tx pgx.Tx, spaceID, marketSkillID string) (string, bool, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.skills
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND market_skill_id = $2`,
		spaceID, marketSkillID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

// TemplateMarketSkills resolves reusable space copies and prioritizes a matching snapshot already on this Agent.
func TemplateMarketSkills(ctx context.Context, tx pgx.Tx, spaceID, agentID string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (market_skill_id) market_skill_id,id::text FROM (
   SELECT market_skill_id,id,false AS owned FROM openfox.skills WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND market_skill_id IS NOT NULL
   UNION ALL SELECT market_skill_id,id,true FROM openfox.agent_market_skill_installs WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$2::uuid AND space_id=$1::uuid
 ) candidates ORDER BY market_skill_id,owned DESC`, spaceID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var marketID, id string
		if err := rows.Scan(&marketID, &id); err != nil {
			return nil, err
		}
		result[marketID] = id
	}
	return result, rows.Err()
}

// AgentSkillIDs 返回 Agent 装配的技能 ID（按 position）。
func AgentSkillIDs(ctx context.Context, tx pgx.Tx, agentID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM openfox.agent_skill_loadout
		WHERE organization_id = openfox_private.current_organization_id() AND agent_id = $1::uuid
		ORDER BY position, attached_at, name`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// InstallForAgent is called with the Agent locked and an authorized, verified market snapshot.
// Append directly: historical bindings and their authorship are never rewritten by installation.
func InstallForAgent(ctx context.Context, tx pgx.Tx, spaceID, agentID, userID, id, source, version, label string, pkg Package, entries []Entry) error {
	var clash bool
	var pos int
	err := tx.QueryRow(ctx, `SELECT COALESCE(bool_or(name=$2),false),COALESCE(max(position)+1,0)
   FROM openfox.agent_skill_loadout WHERE organization_id=openfox_private.current_organization_id() AND agent_id=$1::uuid`, agentID, pkg.Name).Scan(&clash, &pos)
	if err != nil {
		return err
	}
	if clash {
		return ErrAgentNameTaken
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.agent_market_skill_installs
   (organization_id,id,space_id,agent_id,created_by,market_skill_id,market_version,source_label,name,description,files,total_bytes,position)
   VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8,$9,$10,$11,$12)`,
		id, spaceID, agentID, userID, source, version, label, pkg.Name, pkg.Description, entries, pkg.TotalBytes, pos)
	return err
}
