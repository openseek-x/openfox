package docs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
)

// 数据读取：文档集、节点、面包屑、搜索。所有语句都在租户事务里执行，依赖 RLS 限定组织与「我的文档」可见性；
// 空间权限由调用方（API 层）通过 Access 判断后再调用这里。

const setSelect = `
SELECT s.id::text, s.space_id::text, s.name, s.icon_kind, s.icon, s.icon_upload_id::text, s.organization_id::text,
       s.position, s.created_by::text, s.created_at, s.updated_at, s.version
  FROM openfox.doc_sets s
 WHERE s.organization_id = openfox_private.current_organization_id()`

// nodeSelect 的参数约定：$1 为空间，其余参数由各查询追加。
const nodeSelect = `
SELECT n.id::text, n.space_id::text, n.set_id::text, n.owner_user_id::text, n.parent_id::text,
       n.kind, n.title, n.position, n.size_bytes, n.content_type, COALESCE(n.file_key, ''), COALESCE(n.file_ext, ''),
       n.created_by::text, n.updated_by::text, COALESCE(NULLIF(u.full_name, ''), u.email),
       n.created_at, n.updated_at, n.version,
       EXISTS (SELECT 1 FROM openfox.doc_nodes c WHERE c.organization_id = n.organization_id AND c.parent_id = n.id)
  FROM openfox.doc_nodes n
  JOIN openfox.users u ON u.id = n.updated_by
 WHERE n.organization_id = openfox_private.current_organization_id()`

// containerCond 限定容器：$2 为文档集 ID（set）或当前用户 ID（personal）。
func containerCond(c Container) string {
	if c.IsPersonal() {
		return "n.set_id IS NULL AND n.owner_user_id = $2::uuid"
	}
	return "n.set_id = $2::uuid"
}

// containerArg 返回 containerCond 的第二个参数。
func containerArg(c Container, userID string) string {
	if c.IsPersonal() {
		return userID
	}
	return *c.SetID
}

// containerOf 由数据库列推导容器。
func containerOf(setID, ownerUserID *string) Container {
	if setID != nil {
		return Container{Kind: ContainerSet, SetID: setID}
	}
	return Container{Kind: ContainerPersonal}
}

func scanSet(row pgx.Row) (Set, error) {
	var s Set
	var organizationID string
	var uploadID *string
	var version int64
	err := row.Scan(&s.ID, &s.SpaceID, &s.Name, &s.IconKind, &s.Icon, &uploadID, &organizationID,
		&s.Position, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt, &version)
	if err != nil {
		return Set{}, err
	}
	if uploadID != nil {
		url := collaboration.IconURL(organizationID, *uploadID)
		s.IconURL = &url
	}
	s.Version = uint64(version)
	return s, nil
}

// ListSets 返回空间的文档集，按 position、created_at。
func ListSets(ctx context.Context, tx pgx.Tx, spaceID string) ([]Set, error) {
	rows, err := tx.Query(ctx, setSelect+` AND s.space_id = $1::uuid ORDER BY s.position, s.created_at, s.id`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sets := []Set{}
	for rows.Next() {
		s, err := scanSet(rows)
		if err != nil {
			return nil, err
		}
		sets = append(sets, s)
	}
	return sets, rows.Err()
}

// LoadSet 读取空间内的一个文档集；不存在返回 ErrNotFound。
func LoadSet(ctx context.Context, tx pgx.Tx, spaceID, setID string) (Set, error) {
	s, err := scanSet(tx.QueryRow(ctx, setSelect+` AND s.space_id = $1::uuid AND s.id = $2::uuid`, spaceID, setID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Set{}, ErrNotFound
	}
	return s, err
}

// ensureSet 确认文档集属于该空间（同一事务内加 FOR SHARE，防止并发删除）。
func ensureSet(ctx context.Context, tx pgx.Tx, spaceID, setID string) error {
	var found int
	err := tx.QueryRow(ctx, `SELECT 1 FROM openfox.doc_sets WHERE organization_id = openfox_private.current_organization_id()
		AND space_id = $1::uuid AND id = $2::uuid FOR SHARE`, spaceID, setID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func scanNode(row pgx.Row) (Node, error) {
	var n Node
	var setID, ownerID, parentID, contentType *string
	var size *int64
	var fileKey, fileExt string
	var version int64
	err := row.Scan(&n.ID, &n.SpaceID, &setID, &ownerID, &parentID, &n.Kind, &n.Title, &n.Position,
		&size, &contentType, &fileKey, &fileExt, &n.CreatedBy, &n.UpdatedBy, &n.UpdatedByName,
		&n.CreatedAt, &n.UpdatedAt, &version, &n.HasChildren)
	if err != nil {
		return Node{}, err
	}
	n.Container = containerOf(setID, ownerID)
	n.ParentID = parentID
	n.SizeBytes = size
	n.ContentType = contentType
	n.FileKey = fileKey
	n.FileExt = fileExt
	n.Version = uint64(version)
	n.setID = setID
	n.ownerUserID = ownerID
	return n, nil
}

// ListNodes 返回容器里某个父节点（nil 为容器根）下的直接子节点，按 position、title，最多 500 条。
func ListNodes(ctx context.Context, tx pgx.Tx, spaceID, userID string, c Container, parentID *string) ([]Node, error) {
	rows, err := tx.Query(ctx, nodeSelect+` AND n.space_id = $1::uuid AND `+containerCond(c)+
		` AND n.parent_id IS NOT DISTINCT FROM $3::uuid ORDER BY n.position, n.title, n.id LIMIT $4`,
		spaceID, containerArg(c, userID), parentID, MaxListNodes)
	if err != nil {
		return nil, err
	}
	return collectNodes(rows)
}

// SearchNodes 按标题搜索：范围是我的文档加上空间内的文档集节点，最多 50 条。query 应已去除首尾空白。
func SearchNodes(ctx context.Context, tx pgx.Tx, spaceID, userID, query string) ([]Node, error) {
	rows, err := tx.Query(ctx, nodeSelect+` AND n.space_id = $1::uuid
		AND (n.set_id IS NOT NULL OR n.owner_user_id = $2::uuid)
		AND n.title ILIKE $3 ESCAPE '\' ORDER BY n.updated_at DESC, n.id LIMIT $4`,
		spaceID, userID, "%"+EscapeLike(query)+"%", MaxSearchNodes)
	if err != nil {
		return nil, err
	}
	return collectNodes(rows)
}

func collectNodes(rows pgx.Rows) ([]Node, error) {
	defer rows.Close()
	nodes := []Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// LoadRef 读取节点的引用信息；不存在或不可见返回 ErrNotFound。
func LoadRef(ctx context.Context, tx pgx.Tx, spaceID, nodeID string) (Ref, error) {
	var ref Ref
	var setID, ownerID, parentID *string
	var version int64
	err := tx.QueryRow(ctx, `SELECT n.id::text, n.space_id::text, n.set_id::text, n.owner_user_id::text, n.parent_id::text,
		       n.kind, n.version
		  FROM openfox.doc_nodes n
		 WHERE n.organization_id = openfox_private.current_organization_id()
		   AND n.space_id = $1::uuid AND n.id = $2::uuid`, spaceID, nodeID).
		Scan(&ref.ID, &ref.SpaceID, &setID, &ownerID, &parentID, &ref.Kind, &version)
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

// GetNodeDetail 读取节点（含正文与面包屑）；不存在或不可见返回 ErrNotFound。
func GetNodeDetail(ctx context.Context, tx pgx.Tx, spaceID, nodeID string) (NodeDetail, error) {
	node, err := GetNode(ctx, tx, spaceID, nodeID)
	if err != nil {
		return NodeDetail{}, err
	}
	detail := NodeDetail{Node: node, Breadcrumb: []Crumb{}}
	var body *string
	if err := tx.QueryRow(ctx, `SELECT n.body FROM openfox.doc_nodes n
		WHERE n.organization_id = openfox_private.current_organization_id() AND n.id = $1::uuid`, nodeID).Scan(&body); err != nil {
		return NodeDetail{}, err
	}
	detail.Body = body
	crumbs, err := breadcrumb(ctx, tx, nodeID)
	if err != nil {
		return NodeDetail{}, err
	}
	detail.Breadcrumb = crumbs
	return detail, nil
}

// breadcrumb 返回从容器根到父节点的路径（不含节点自己）。
func breadcrumb(ctx context.Context, tx pgx.Tx, nodeID string) ([]Crumb, error) {
	rows, err := tx.Query(ctx, `WITH RECURSIVE up AS (
		  SELECT n.id, n.parent_id, n.title, 1 AS depth
		    FROM openfox.doc_nodes n
		   WHERE n.organization_id = openfox_private.current_organization_id() AND n.id = $1::uuid
		  UNION ALL
		  SELECT p.id, p.parent_id, p.title, up.depth + 1
		    FROM openfox.doc_nodes p
		    JOIN up ON p.id = up.parent_id
		   WHERE p.organization_id = openfox_private.current_organization_id()
		)
		SELECT id::text, title FROM up WHERE depth > 1 ORDER BY depth DESC`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	crumbs := []Crumb{}
	for rows.Next() {
		var c Crumb
		if err := rows.Scan(&c.ID, &c.Title); err != nil {
			return nil, err
		}
		crumbs = append(crumbs, c)
	}
	return crumbs, rows.Err()
}

// nodeDepth 返回节点在树中的层数（根节点为 1）。节点不存在时返回 0。
func nodeDepth(ctx context.Context, tx pgx.Tx, nodeID string) (int, error) {
	var depth *int
	err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (
		  SELECT n.id, n.parent_id, 1 AS depth
		    FROM openfox.doc_nodes n
		   WHERE n.organization_id = openfox_private.current_organization_id() AND n.id = $1::uuid
		  UNION ALL
		  SELECT p.id, p.parent_id, up.depth + 1
		    FROM openfox.doc_nodes p
		    JOIN up ON p.id = up.parent_id
		   WHERE p.organization_id = openfox_private.current_organization_id()
		)
		SELECT max(depth) FROM up`, nodeID).Scan(&depth)
	if err != nil {
		return 0, err
	}
	if depth == nil {
		return 0, nil
	}
	return *depth, nil
}

// LoadAttachment 读取附件及其所属节点的空间；不存在或不在该空间返回 ErrNotFound。
func LoadAttachment(ctx context.Context, tx pgx.Tx, spaceID, attachmentID string) (Attachment, error) {
	var a Attachment
	var size int64
	err := tx.QueryRow(ctx, `SELECT a.id::text, a.node_id::text, a.name, a.content_type, a.size_bytes, a.file_key, n.space_id::text
		  FROM openfox.doc_attachments a
		  JOIN openfox.doc_nodes n ON n.organization_id = a.organization_id AND n.id = a.node_id
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND a.id = $1::uuid AND n.space_id = $2::uuid`, attachmentID, spaceID).
		Scan(&a.ID, &a.NodeID, &a.Name, &a.ContentType, &size, &a.FileKey, &a.SpaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, err
	}
	a.SizeBytes = size
	return a, nil
}
