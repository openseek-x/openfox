package conversations

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// 阶段 12f 快捷条目收藏：快捷回复（REPLY）与快捷指令（COMMAND）的个人收藏，列表中置顶显示。
// 收藏只属于本人；收藏时必须对条目当前可见；重复收藏、重复取消都幂等。
// 条目失效（软删除、对本人不再可见）时收藏行成为孤行：列表只读取仍可见条目的标记，不会泄露。

const (
	favoriteTypeReply   = "REPLY"
	favoriteTypeCommand = "COMMAND"
)

// favoriteIDs 返回本人在某类条目上的收藏 ID 集合（条目数有上限，收藏行随之有界）。
func favoriteIDs(ctx context.Context, tx pgx.Tx, actor Actor, entryType string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT entry_id::text FROM openfox.quick_entry_favorites
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND user_id = $1::uuid AND entry_type = $2`, actor.UserID, entryType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		set[id] = true
	}
	return set, rows.Err()
}

// markQuickReplyFavorites 填充 Favorite，并把收藏项稳定地排到各自分组的前面。
func markQuickReplyFavorites(ctx context.Context, tx pgx.Tx, actor Actor, items ...*[]QuickReply) error {
	set, err := favoriteIDs(ctx, tx, actor, favoriteTypeReply)
	if err != nil {
		return err
	}
	for _, group := range items {
		for index := range *group {
			(*group)[index].Favorite = set[(*group)[index].ID]
		}
		list := *group
		sort.SliceStable(list, func(i, j int) bool { return list[i].Favorite && !list[j].Favorite })
	}
	return nil
}

func markQuickCommandFavorites(ctx context.Context, tx pgx.Tx, actor Actor, groups ...*[]QuickCommand) error {
	set, err := favoriteIDs(ctx, tx, actor, favoriteTypeCommand)
	if err != nil {
		return err
	}
	for _, group := range groups {
		for index := range *group {
			(*group)[index].Favorite = set[(*group)[index].ID]
		}
		list := *group
		sort.SliceStable(list, func(i, j int) bool { return list[i].Favorite && !list[j].Favorite })
	}
	return nil
}

func setFavorite(ctx context.Context, tx pgx.Tx, actor Actor, entryType, id string, on bool) error {
	if on {
		_, err := tx.Exec(ctx, `
			INSERT INTO openfox.quick_entry_favorites (organization_id, user_id, entry_type, entry_id)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3::uuid)
			ON CONFLICT DO NOTHING`, actor.UserID, entryType, id)
		return mapError(err)
	}
	_, err := tx.Exec(ctx, `
		DELETE FROM openfox.quick_entry_favorites
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND user_id = $1::uuid AND entry_type = $2 AND entry_id = $3::uuid`, actor.UserID, entryType, id)
	return err
}

// SetQuickReplyFavorite 收藏 / 取消收藏一条对本人可见的快捷回复。取消收藏同样要求条目可见，
// 以免通过 404 / 204 差异探测他人个人条目的存在性。
func (s Service) SetQuickReplyFavorite(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, id string, on bool) (QuickReply, error) {
	if err := s.quickReplyActor(ctx, tx, organizationID, actor); err != nil {
		return QuickReply{}, err
	}
	item, err := s.loadQuickReplyAny(ctx, tx, id, actor)
	if err != nil {
		return QuickReply{}, err
	}
	if err := setFavorite(ctx, tx, actor, favoriteTypeReply, id, on); err != nil {
		return QuickReply{}, err
	}
	item.Favorite = on
	return item, nil
}

// SetQuickCommandFavorite 收藏 / 取消收藏一条对本人可见的快捷指令（空间可读，且为共享或本人私有）。
func (s Service) SetQuickCommandFavorite(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID, id string, on bool) (QuickCommand, error) {
	access, err := s.spaceQuickCommandAccess(ctx, tx, organizationID, actor, spaceID)
	if err != nil {
		return QuickCommand{}, err
	}
	item, err := s.loadQuickCommand(ctx, tx, organizationID, actor, access, spaceID, id)
	if err != nil {
		return QuickCommand{}, err
	}
	if !membership.ValidateUUID(id) {
		return QuickCommand{}, ErrNotFound
	}
	if err := setFavorite(ctx, tx, actor, favoriteTypeCommand, id, on); err != nil {
		return QuickCommand{}, err
	}
	item.Favorite = on
	return item, nil
}
