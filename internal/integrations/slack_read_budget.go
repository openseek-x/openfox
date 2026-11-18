package integrations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/slack"
)

// History/replies use the conservative one-request/minute contract. Other
// methods persist actual Retry-After. The App lock serializes this reservation
// with manual receipt verification; no network happens in its transaction.
func (w *IMWorker) slackRead(ctx context.Context, org, app, method, resource string, call func() error) error {
	if method == "history" || method == "replies" {
		resource = ""
	}
	var blocked time.Duration
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, app).Scan(&id); e != nil {
			return e
		}
		var until *time.Time
		if e := tx.QueryRow(ctx, `SELECT max(not_before) FROM openfox.slack_rate_limits WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND method=$2 AND (resource='' OR resource=$3)`, app, method, resource).Scan(&until); e != nil {
			return e
		}
		if until != nil && until.After(time.Now()) {
			blocked = time.Until(*until)
			return nil
		}
		if method == "history" || method == "replies" || method == "post" || method == "unfurl" {
			delay := time.Minute
			if method == "post" || method == "unfurl" {
				delay = time.Second
			}
			_, e := tx.Exec(ctx, `INSERT INTO openfox.slack_rate_limits(organization_id,app_id,method,resource,not_before) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4) ON CONFLICT(organization_id,app_id,method,resource) DO UPDATE SET not_before=GREATEST(openfox.slack_rate_limits.not_before,EXCLUDED.not_before)`, app, method, resource, time.Now().Add(delay))
			return e
		}
		return nil
	})
	if e != nil {
		return e
	}
	if blocked > 0 {
		return &slack.Error{Kind: slack.ErrRateLimited, Code: "local_read_budget", RetryAfter: blocked}
	}
	e = call()
	var upstream *slack.Error
	if errors.Is(e, slack.ErrRateLimited) && errors.As(e, &upstream) && upstream.RetryAfter > 0 {
		until := time.Now().Add(upstream.RetryAfter)
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		save := w.inOrganization(saveCtx, org, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO openfox.slack_rate_limits(organization_id,app_id,method,resource,not_before) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4) ON CONFLICT(organization_id,app_id,method,resource) DO UPDATE SET not_before=GREATEST(openfox.slack_rate_limits.not_before,EXCLUDED.not_before)`, app, method, resource, until)
			return err
		})
		if save != nil {
			return save
		}
	}
	return e
}
