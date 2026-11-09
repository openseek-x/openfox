package docs

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
)

// 写入：文档集、节点、附件。授权由 API 层先行完成（Access）；这里做业务规则、并发控制与数据写入。
// 并发约定：
//   - 写节点与文档集前先取容器的 advisory lock（按键排序加锁，避免两个事务交叉死锁）；
//   - 移动先取容器锁，再锁源节点 FOR UPDATE，并复核版本号；
//   - 乐观锁：UPDATE / DELETE 都带 version = $v，0 行时再查一次区分不存在与版本冲突。

// containerKey 返回容器的锁键：文档集为 "s:<set_id>"，我的文档为 "p:<space_id>:<user_id>"。
func containerKey(c Container, spaceID, userID string) string {
	if c.IsPersonal() {
		return "p:" + spaceID + ":" + userID
	}
	return "s:" + *c.SetID
}

// lockContainers 按排序后的键依次取 advisory lock（同一键只取一次）。
func lockContainers(ctx context.Context, tx pgx.Tx, keys ...string) error {
	slices.Sort(keys)
	for i, key := range keys {
		if i > 0 && keys[i-1] == key {
			continue
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('openfox.doc:' || openfox_private.current_organization_id()::text || ':' || $1, 0))`, key); err != nil {
			return err
		}
	}
	return nil
}

// iconColumns 把图标转换为 icon_kind / icon / icon_upload_id 三列的值；nil 表示没有图标。
func iconColumns(ctx context.Context, tx pgx.Tx, icon *SetIcon) (kind, value, upload any, err error) {
	if icon == nil {
		return nil, nil, nil, nil
	}
	kind, value, upload, err = collaboration.NormalizeViewIcon(icon.Kind, icon.Value)
	if err != nil {
		return nil, nil, nil, err
	}
	if upload != nil {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.uploaded_icons
			WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid)`, upload).Scan(&exists); err != nil {
			return nil, nil, nil, err
		}
		if !exists {
			return nil, nil, nil, ErrIconInvalid
		}
	}
	return kind, value, upload, nil
}

// CreateSet 新建文档集。锁空间行后计数，每个空间最多 200 个。
func CreateSet(ctx context.Context, tx pgx.Tx, spaceID, actorID, setID string, input SetInput) (Set, error) {
	name, err := NormalizeSetName(input.Name)
	if err != nil {
		return Set{}, err
	}
	kind, value, upload, err := iconColumns(ctx, tx, input.Icon)
	if err != nil {
		return Set{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM openfox.spaces
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, spaceID); err != nil {
		return Set{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.doc_sets
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid`, spaceID).Scan(&count); err != nil {
		return Set{}, err
	}
	if count >= MaxSetsPerSpace {
		return Set{}, ErrSetLimit
	}
	var position int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(position) + 1, 0) FROM openfox.doc_sets
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid`, spaceID).Scan(&position); err != nil {
		return Set{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO openfox.doc_sets
		(organization_id, id, space_id, name, icon_kind, icon, icon_upload_id, position, created_by, version)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, $5, $6::uuid, $7, $8::uuid, 1)`,
		setID, spaceID, name, kind, value, upload, position, actorID); err != nil {
		return Set{}, err
	}
	return LoadSet(ctx, tx, spaceID, setID)
}

// UpdateSet 修改文档集（名称与图标）。version 不匹配返回 409，不存在返回 404。
func UpdateSet(ctx context.Context, tx pgx.Tx, spaceID, setID string, input SetInput, version uint64) (Set, error) {
	name, err := NormalizeSetName(input.Name)
	if err != nil {
		return Set{}, err
	}
	kind, value, upload, err := iconColumns(ctx, tx, input.Icon)
	if err != nil {
		return Set{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.doc_sets
		   SET name = $3, icon_kind = $4, icon = $5, icon_upload_id = $6::uuid, updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND version = $7`,
		spaceID, setID, name, kind, value, upload, int64(version))
	if err != nil {
		return Set{}, err
	}
	if tag.RowsAffected() == 0 {
		if _, err := LoadSet(ctx, tx, spaceID, setID); err != nil {
			return Set{}, err
		}
		return Set{}, ErrVersionConflict
	}
	return LoadSet(ctx, tx, spaceID, setID)
}

// DeleteSet 删除文档集及其全部文档（级联），返回需要删除的存储键（调用方在提交后尽力删除）。
func DeleteSet(ctx context.Context, tx pgx.Tx, spaceID, setID string, version uint64) ([]string, error) {
	if err := lockContainers(ctx, tx, "s:"+setID); err != nil {
		return nil, err
	}
	var current int64
	err := tx.QueryRow(ctx, `SELECT version FROM openfox.doc_sets
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid FOR UPDATE`,
		spaceID, setID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if uint64(current) != version {
		return nil, ErrVersionConflict
	}
	keys, err := fileKeys(ctx, tx, `SELECT n.file_key FROM openfox.doc_nodes n
		WHERE n.organization_id = openfox_private.current_organization_id() AND n.set_id = $1::uuid AND n.file_key IS NOT NULL
		UNION ALL
		SELECT a.file_key FROM openfox.doc_attachments a
		  JOIN openfox.doc_nodes n ON n.organization_id = a.organization_id AND n.id = a.node_id
		 WHERE a.organization_id = openfox_private.current_organization_id() AND n.set_id = $1::uuid`, setID)
	if err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM openfox.doc_sets
		WHERE organization_id = openfox_private.current_organization_id()
		  AND space_id = $1::uuid AND id = $2::uuid AND version = $3`, spaceID, setID, int64(version))
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrVersionConflict
	}
	return keys, nil
}

// fileKeys 执行返回单列文件键的查询，忽略空值。
func fileKeys(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key *string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		if key != nil && *key != "" {
			keys = append(keys, *key)
		}
	}
	return keys, rows.Err()
}

// subtreeStats 统计以 nodeID 为根的子树：节点数与最大相对层数（根为 1），并判断是否包含 parentID。
type subtreeStats struct {
	Count       int
	Height      int
	ContainsIDs bool
}

func subtreeOf(ctx context.Context, tx pgx.Tx, nodeID string, parentID *string) (subtreeStats, error) {
	var stats subtreeStats
	err := tx.QueryRow(ctx, `WITH RECURSIVE sub AS (
		  SELECT n.id, 1 AS depth FROM openfox.doc_nodes n
		   WHERE n.organization_id = openfox_private.current_organization_id() AND n.id = $1::uuid
		  UNION ALL
		  SELECT c.id, sub.depth + 1 FROM openfox.doc_nodes c
		    JOIN sub ON c.parent_id = sub.id
		   WHERE c.organization_id = openfox_private.current_organization_id()
		)
		SELECT count(*), COALESCE(max(depth), 0), COALESCE(bool_or(id = $2::uuid), false) FROM sub`,
		nodeID, parentID).Scan(&stats.Count, &stats.Height, &stats.ContainsIDs)
	return stats, err
}

// countContainer 统计容器内的节点数。
func countContainer(ctx context.Context, tx pgx.Tx, spaceID, actorID string, c Container) (int, error) {
	var count int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.doc_nodes n
		WHERE n.organization_id = openfox_private.current_organization_id() AND n.space_id = $1::uuid AND `+containerCond(c),
		spaceID, containerArg(c, actorID)).Scan(&count)
	return count, err
}

// parentFolderOK 确认父节点是容器内的文件夹，并返回它的层数。
func parentFolderOK(ctx context.Context, tx pgx.Tx, spaceID, actorID string, c Container, parentID string) (int, error) {
	var kind string
	err := tx.QueryRow(ctx, `SELECT n.kind FROM openfox.doc_nodes n
		WHERE n.organization_id = openfox_private.current_organization_id() AND n.space_id = $1::uuid AND `+containerCond(c)+`
		  AND n.id = $3::uuid`, spaceID, containerArg(c, actorID), parentID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrParentInvalid
	}
	if err != nil {
		return 0, err
	}
	if kind != KindFolder {
		return 0, ErrParentInvalid
	}
	return nodeDepth(ctx, tx, parentID)
}

// CreateNode 新建节点（文件夹 / 文档 / 表格 / 上传的文件）。节点放在兄弟节点最后。
func CreateNode(ctx context.Context, tx pgx.Tx, spaceID, actorID string, input NodeInput) (Node, error) {
	title, err := NormalizeTitle(input.Title)
	if err != nil {
		return Node{}, err
	}
	var body any
	switch input.Kind {
	case KindFolder:
		if title == "" {
			return Node{}, ErrFolderTitle
		}
		if input.Body != nil {
			return Node{}, ErrBodyNotAllowed
		}
	case KindDoc, KindSheet:
		text := ""
		if input.Body != nil {
			text = *input.Body
		}
		if len(text) > MaxBodyBytes {
			return Node{}, ErrBodyTooLarge
		}
		body = text
	case KindFile:
		if input.File == nil || input.Body != nil {
			return Node{}, ErrKindInvalid
		}
	default:
		return Node{}, ErrKindInvalid
	}
	c := input.Container
	if err := lockContainers(ctx, tx, containerKey(c, spaceID, actorID)); err != nil {
		return Node{}, err
	}
	if !c.IsPersonal() {
		if err := ensureSet(ctx, tx, spaceID, *c.SetID); err != nil {
			return Node{}, err
		}
	}
	var parentDepth int
	if input.ParentID != nil {
		if parentDepth, err = parentFolderOK(ctx, tx, spaceID, actorID, c, *input.ParentID); err != nil {
			return Node{}, err
		}
		if parentDepth+1 > MaxDepth {
			return Node{}, ErrDepthExceeded
		}
	}
	count, err := countContainer(ctx, tx, spaceID, actorID, c)
	if err != nil {
		return Node{}, err
	}
	if count >= MaxNodesPerContainer {
		return Node{}, ErrNodeLimit
	}
	var position int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(n.position) + 1, 0) FROM openfox.doc_nodes n
		WHERE n.organization_id = openfox_private.current_organization_id() AND n.space_id = $1::uuid AND `+containerCond(c)+`
		  AND n.parent_id IS NOT DISTINCT FROM $3::uuid`, spaceID, containerArg(c, actorID), input.ParentID).Scan(&position); err != nil {
		return Node{}, err
	}
	var setID, ownerID, fileKey, fileExt, contentType, sha256 any
	var size any
	if c.IsPersonal() {
		ownerID = actorID
	} else {
		setID = *c.SetID
	}
	if input.File != nil {
		fileKey, contentType, size, sha256 = input.File.Key, input.File.ContentType, input.File.SizeBytes, input.File.SHA256
		if input.File.Ext != "" {
			fileExt = input.File.Ext
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO openfox.doc_nodes
		(organization_id, id, space_id, set_id, owner_user_id, parent_id, kind, title, body, file_key, file_ext, content_type, size_bytes, sha256,
		 position, created_by, updated_by, version)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8, $9, $10, $11, $12, $13,
		        $14, $15::uuid, $15::uuid, 1)`,
		input.ID, spaceID, setID, ownerID, input.ParentID, input.Kind, title, body, fileKey, fileExt, contentType, size, sha256,
		position, actorID); err != nil {
		return Node{}, err
	}
	return GetNode(ctx, tx, spaceID, input.ID)
}

// GetNode 读取节点（不含正文）。
func GetNode(ctx context.Context, tx pgx.Tx, spaceID, nodeID string) (Node, error) {
	rows, err := tx.Query(ctx, nodeSelect+` AND n.space_id = $1::uuid AND n.id = $2::uuid`, spaceID, nodeID)
	if err != nil {
		return Node{}, err
	}
	nodes, err := collectNodes(rows)
	if err != nil {
		return Node{}, err
	}
	if len(nodes) == 0 {
		return Node{}, ErrNotFound
	}
	return nodes[0], nil
}

// UpdateNode 重命名或保存正文。ref 为授权时读取的引用；version 与 ref.Version 不一致返回 409。
func UpdateNode(ctx context.Context, tx pgx.Tx, ref Ref, actorID string, version uint64, title, body *string) error {
	if version != ref.Version {
		return ErrVersionConflict
	}
	var newTitle, newBody *string
	if title != nil {
		value, err := NormalizeTitle(*title)
		if err != nil {
			return err
		}
		if ref.Kind == KindFolder && value == "" {
			return ErrFolderTitle
		}
		newTitle = &value
	}
	if body != nil {
		if ref.Kind != KindDoc && ref.Kind != KindSheet {
			return ErrBodyNotAllowed
		}
		if len(*body) > MaxBodyBytes {
			return ErrBodyTooLarge
		}
		newBody = body
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.doc_nodes
		   SET title = COALESCE($3, title), body = COALESCE($4, body), updated_by = $5::uuid,
		       updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND version = $6`,
		ref.SpaceID, ref.ID, newTitle, newBody, actorID, int64(version))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// 0 行：节点可能已被删除（404），否则是版本冲突（409）。
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.doc_nodes
			WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid)`,
			ref.SpaceID, ref.ID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		return ErrVersionConflict
	}
	return nil
}

// lockNode 锁定节点行并返回它的当前引用，用于容器锁之后的复核。
func lockNode(ctx context.Context, tx pgx.Tx, spaceID, nodeID string) (Ref, error) {
	var ref Ref
	var setID, ownerID, parentID *string
	var version int64
	err := tx.QueryRow(ctx, `SELECT n.id::text, n.space_id::text, n.set_id::text, n.owner_user_id::text, n.parent_id::text, n.kind, n.version
		  FROM openfox.doc_nodes n
		 WHERE n.organization_id = openfox_private.current_organization_id() AND n.space_id = $1::uuid AND n.id = $2::uuid
		 FOR UPDATE`, spaceID, nodeID).Scan(&ref.ID, &ref.SpaceID, &setID, &ownerID, &parentID, &ref.Kind, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ref{}, ErrNotFound
	}
	if err != nil {
		return Ref{}, err
	}
	ref.Container = containerOf(setID, ownerID)
	ref.ParentID = parentID
	ref.Version = uint64(version)
	return ref, nil
}

// DeleteNode 删除节点及其子树，返回需要删除的存储键。
func DeleteNode(ctx context.Context, tx pgx.Tx, ref Ref, actorID string, version uint64) ([]string, error) {
	if version != ref.Version {
		return nil, ErrVersionConflict
	}
	if err := lockContainers(ctx, tx, containerKey(ref.Container, ref.SpaceID, actorID)); err != nil {
		return nil, err
	}
	locked, err := lockNode(ctx, tx, ref.SpaceID, ref.ID)
	if err != nil {
		return nil, err
	}
	if locked.Version != ref.Version {
		return nil, ErrVersionConflict
	}
	keys, err := fileKeys(ctx, tx, subtreeFileKeysSQL, ref.ID)
	if err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM openfox.doc_nodes
		WHERE organization_id = openfox_private.current_organization_id()
		  AND space_id = $1::uuid AND id = $2::uuid AND version = $3`, ref.SpaceID, ref.ID, int64(version))
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrVersionConflict
	}
	return keys, nil
}

// subtreeFileKeysSQL 返回子树中 file 节点与附件的存储键（$1 为子树根）。
const subtreeFileKeysSQL = `WITH RECURSIVE sub AS (
	  SELECT n.id FROM openfox.doc_nodes n
	   WHERE n.organization_id = openfox_private.current_organization_id() AND n.id = $1::uuid
	  UNION ALL
	  SELECT c.id FROM openfox.doc_nodes c JOIN sub ON c.parent_id = sub.id
	   WHERE c.organization_id = openfox_private.current_organization_id()
	)
	SELECT n.file_key FROM openfox.doc_nodes n JOIN sub ON sub.id = n.id
	 WHERE n.organization_id = openfox_private.current_organization_id() AND n.file_key IS NOT NULL
	UNION ALL
	SELECT a.file_key FROM openfox.doc_attachments a JOIN sub ON sub.id = a.node_id
	 WHERE a.organization_id = openfox_private.current_organization_id()`

// MoveInput 是移动的目标位置。Authorize 在移动前检查源与目标的写权限（源空间与目标空间可能不同）。
type MoveInput struct {
	Version       uint64
	TargetSpaceID string
	Target        Container
	ParentID      *string
	BeforeID      *string
}

// MoveNode 移动节点及其子树，可以跨容器与跨空间。
// authorize 对给定空间与容器做写权限判断（源与目标各调用一次，目标在源之后）。
func MoveNode(ctx context.Context, tx pgx.Tx, ref Ref, actorID string, input MoveInput,
	authorize func(spaceID string, c Container) error) error {
	if err := authorize(ref.SpaceID, ref.Container); err != nil {
		return err
	}
	if err := authorize(input.TargetSpaceID, input.Target); err != nil {
		return err
	}
	if input.Version != ref.Version {
		return ErrVersionConflict
	}
	target := input.Target
	if err := lockContainers(ctx, tx, containerKey(ref.Container, ref.SpaceID, actorID),
		containerKey(target, input.TargetSpaceID, actorID)); err != nil {
		return err
	}
	if !target.IsPersonal() {
		if err := ensureSet(ctx, tx, input.TargetSpaceID, *target.SetID); err != nil {
			return err
		}
	}
	locked, err := lockNode(ctx, tx, ref.SpaceID, ref.ID)
	if err != nil {
		return err
	}
	if locked.Version != ref.Version || containerKey(locked.Container, ref.SpaceID, actorID) != containerKey(ref.Container, ref.SpaceID, actorID) {
		return ErrVersionConflict
	}
	stats, err := subtreeOf(ctx, tx, ref.ID, input.ParentID)
	if err != nil {
		return err
	}
	if stats.ContainsIDs {
		return ErrMoveIntoSelf
	}
	parentDepth := 0
	if input.ParentID != nil {
		if parentDepth, err = parentFolderOK(ctx, tx, input.TargetSpaceID, actorID, target, *input.ParentID); err != nil {
			return err
		}
	}
	if parentDepth+stats.Height > MaxDepth {
		return ErrDepthExceeded
	}
	if containerKey(target, input.TargetSpaceID, actorID) != containerKey(ref.Container, ref.SpaceID, actorID) {
		count, err := countContainer(ctx, tx, input.TargetSpaceID, actorID, target)
		if err != nil {
			return err
		}
		if count+stats.Count > MaxNodesPerContainer {
			return ErrNodeLimit
		}
	}
	position, err := placeSibling(ctx, tx, ref.ID, input, actorID)
	if err != nil {
		return err
	}
	var setID, ownerID any
	if target.IsPersonal() {
		ownerID = actorID
	} else {
		setID = *target.SetID
	}
	if _, err := tx.Exec(ctx, `WITH RECURSIVE sub AS (
		  SELECT n.id FROM openfox.doc_nodes n
		   WHERE n.organization_id = openfox_private.current_organization_id() AND n.id = $1::uuid
		  UNION ALL
		  SELECT c.id FROM openfox.doc_nodes c JOIN sub ON c.parent_id = sub.id
		   WHERE c.organization_id = openfox_private.current_organization_id()
		)
		UPDATE openfox.doc_nodes d
		   SET space_id = $2::uuid, set_id = $3::uuid, owner_user_id = $4::uuid, version = d.version + 1
		 WHERE d.organization_id = openfox_private.current_organization_id() AND d.id IN (SELECT id FROM sub)`,
		ref.ID, input.TargetSpaceID, setID, ownerID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.doc_nodes
		   SET parent_id = $2::uuid, position = $3, updated_by = $4::uuid, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
		ref.ID, input.ParentID, position, actorID)
	return err
}

// placeSibling 计算移动后的 position。BeforeID 为空时放到最后；否则插入到该兄弟之前并重排目标父节点下的兄弟。
func placeSibling(ctx context.Context, tx pgx.Tx, nodeID string, input MoveInput, actorID string) (int, error) {
	target := input.Target
	if input.BeforeID == nil {
		var position int
		err := tx.QueryRow(ctx, `SELECT COALESCE(max(n.position) + 1, 0) FROM openfox.doc_nodes n
			WHERE n.organization_id = openfox_private.current_organization_id() AND n.space_id = $1::uuid AND `+containerCond(target)+`
			  AND n.parent_id IS NOT DISTINCT FROM $3::uuid AND n.id <> $4::uuid`,
			input.TargetSpaceID, containerArg(target, actorID), input.ParentID, nodeID).Scan(&position)
		return position, err
	}
	if *input.BeforeID == nodeID {
		return 0, ErrBeforeInvalid
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text FROM openfox.doc_nodes n
		WHERE n.organization_id = openfox_private.current_organization_id() AND n.space_id = $1::uuid AND `+containerCond(target)+`
		  AND n.parent_id IS NOT DISTINCT FROM $3::uuid AND n.id <> $4::uuid
		ORDER BY n.position, n.title, n.id`,
		input.TargetSpaceID, containerArg(target, actorID), input.ParentID, nodeID)
	if err != nil {
		return 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	index := slices.Index(ids, *input.BeforeID)
	if index < 0 {
		return 0, ErrBeforeInvalid
	}
	ids = slices.Insert(ids, index, nodeID)
	positions := make([]int, len(ids))
	for i := range positions {
		positions[i] = i
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.doc_nodes d SET position = x.pos
		  FROM unnest($1::text[], $2::int[]) AS x(id, pos)
		 WHERE d.organization_id = openfox_private.current_organization_id() AND d.id = x.id::uuid`, ids, positions); err != nil {
		return 0, err
	}
	return index, nil
}

// InsertAttachment 写入附件记录。节点必须在该空间内，否则返回 404（插入与存在性检查在同一条语句里完成）。
func InsertAttachment(ctx context.Context, tx pgx.Tx, spaceID, nodeID, actorID, attachmentID, name string, info FileInfo) (Attachment, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO openfox.doc_attachments
		(organization_id, id, node_id, name, file_key, content_type, size_bytes, sha256, created_by)
		SELECT n.organization_id, $1::uuid, n.id, $2, $3, $4, $5, $6, $7::uuid
		  FROM openfox.doc_nodes n
		 WHERE n.organization_id = openfox_private.current_organization_id()
		   AND n.space_id = $8::uuid AND n.id = $9::uuid`,
		attachmentID, name, info.Key, info.ContentType, info.SizeBytes, info.SHA256, actorID, spaceID, nodeID)
	if err != nil {
		return Attachment{}, err
	}
	if tag.RowsAffected() == 0 {
		return Attachment{}, ErrNotFound
	}
	return Attachment{ID: attachmentID, NodeID: nodeID, Name: name, ContentType: info.ContentType, SizeBytes: info.SizeBytes}, nil
}
