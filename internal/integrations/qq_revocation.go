package integrations

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

func (s Service) RevokeQQBinding(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) error {
	if !membership.ValidateUUID(id) || version == 0 {
		return ErrInvalidRequest
	}
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return e
	}
	var bot, owner, scope, epoch string
	e = tx.QueryRow(ctx, `SELECT bot_id::text,owner_user_id::text,scope,COALESCE(group_epoch_id::text,'') FROM openfox.qq_bindings WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&bot, &owner, &scope, &epoch)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	b, e := lockQQBot(ctx, tx, bot)
	if e != nil {
		return e
	}
	if owner != a.UserID {
		if e = qqBotStopManager(ctx, tx, a, b); e != nil {
			return e
		}
	}
	var got string
	if epoch != "" {
		if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.qq_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, epoch).Scan(&got); e != nil {
			return e
		}
	}
	var current uint64
	var active bool
	e = tx.QueryRow(ctx, `SELECT version,active FROM openfox.qq_bindings WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&current, &active)
	if e != nil {
		return e
	}
	if current != version {
		return ErrVersionConflict
	}
	if !active {
		return nil
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_bindings SET active=false,revoked_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND active AND (id=$2::uuid OR ($3='C2C' AND owner_user_id=$4::uuid))`, bot, id, scope, owner)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_routes r SET closed_at=clock_timestamp() WHERE r.organization_id=openfox_private.current_organization_id() AND r.bot_id=$1::uuid AND r.scope='C2C' AND r.closed_at IS NULL AND EXISTS(SELECT 1 FROM openfox.qq_bindings i WHERE i.organization_id=r.organization_id AND i.id=r.identity_id AND NOT i.active)`, bot)
	return e
}

func (s Service) DisableQQGroup(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) error {
	if !membership.ValidateUUID(id) || version == 0 {
		return ErrInvalidRequest
	}
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return e
	}
	var bot string
	e = tx.QueryRow(ctx, `SELECT bot_id::text FROM openfox.qq_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&bot)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	b, e := lockQQBot(ctx, tx, bot)
	if e != nil {
		return e
	}
	if e = qqBotStopManager(ctx, tx, a, b); e != nil {
		return e
	}
	var current uint64
	var disabled bool
	e = tx.QueryRow(ctx, `SELECT version,disabled_at IS NOT NULL FROM openfox.qq_group_epochs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&current, &disabled)
	if e != nil {
		return e
	}
	if current != version {
		return ErrVersionConflict
	}
	if disabled {
		return nil
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_group_epochs SET disabled_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_bindings SET active=false,revoked_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND group_epoch_id=$1::uuid AND active`, id)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_routes SET closed_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND group_epoch_id=$1::uuid AND closed_at IS NULL`, id)
	return e
}
