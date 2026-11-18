package integrations

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/telegram"
	"openfox/internal/membership"
)

const telegramBindingNotice = "请在 OpenFox 的个人设置中关联此 Telegram 机器人，再发送消息。"

// Invoked only for a fresh, authenticated ordinary message. It never includes
// organization/space identifiers or caller-supplied text. A group must already
// have an explicitly enabled route; the route fence survives queue delay.
func queueTelegramBindingNotice(ctx context.Context, tx pgx.Tx, r telegramControl, m telegram.Message, epoch string) error {
	var route string
	if m.Chat.ID < 0 {
		err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.telegram_routes WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 AND epoch_id=NULLIF($3,'')::uuid AND closed_at IS NULL AND authorization_generation=$4`, r.bot.ID, int64(m.Chat.ID), epoch, r.authGeneration).Scan(&route)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO openfox.telegram_chat_limits(organization_id,bot_id,chat_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2) ON CONFLICT DO NOTHING`, r.bot.ID, int64(m.Chat.ID)); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.telegram_chat_limits SET notice_after=clock_timestamp()+interval '10 minutes' WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 AND notice_after<=clock_timestamp()`, r.bot.ID, int64(m.Chat.ID))
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	id, err := membership.NewID()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO openfox.im_reply_outbox(organization_id,id,bot_id,chat_id,body,reply_to_remote_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5)`, id, r.bot.ID, m.Chat.ID.String(), telegramBindingNotice, m.ID.String()); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.telegram_replies(organization_id,outbox_id,bot_id,chat_id,reply_to_message_id,authorization_generation,body,body_hash,notice,notice_route_id)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3,$4,$5,$6,$7,true,NULLIF($8,'')::uuid)`, id, r.bot.ID, int64(m.Chat.ID), int64(m.ID), r.authGeneration, telegramBindingNotice, TokenHash(telegramBindingNotice), route)
	return err
}

func telegramNoticeProof(ctx context.Context, tx pgx.Tx, r telegramControl, id string) (telegramInputProof, bool, error) {
	var p telegramInputProof
	var body string
	var fresh bool
	err := tx.QueryRow(ctx, `SELECT bot_id::text,chat_id,reply_to_message_id,authorization_generation,COALESCE(notice_route_id::text,''),body,created_at>clock_timestamp()-interval '10 minutes'
 FROM openfox.telegram_replies WHERE organization_id=openfox_private.current_organization_id() AND outbox_id=$1::uuid AND notice`, id).Scan(&p.bot, &p.chat, &p.remoteMessage, &p.generation, &p.route, &body, &fresh)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if !fresh || body != telegramBindingNotice || p.bot != r.bot.ID || p.generation != r.authGeneration || r.bot.State != "ACTIVE" || r.parentStatus != StatusActive {
		return p, true, ErrForbidden
	}
	p.kind = "p2p"
	if p.chat < 0 {
		p.kind = "group"
		var valid bool
		err = tx.QueryRow(ctx, `SELECT r.closed_at IS NULL AND r.bot_id=$2::uuid AND r.chat_id=$3 AND r.authorization_generation=$4 AND e.enabled AND e.public_reply_consent AND e.authorization_generation=$4
 FROM openfox.telegram_routes r JOIN openfox.telegram_group_epochs e ON e.organization_id=r.organization_id AND e.id=r.epoch_id
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.id=$1::uuid FOR SHARE OF e`, p.route, p.bot, p.chat, p.generation).Scan(&valid)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !valid) {
			return p, true, ErrForbidden
		}
		if err != nil {
			return p, true, err
		}
	}
	return p, true, nil
}
