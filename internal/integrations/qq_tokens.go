package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/qq"
)

func (s Service) qqClient() *qq.Client {
	if s.QQ != nil {
		return s.QQ
	}
	return &qq.Client{}
}
func (s Service) qqStoredToken(ctx context.Context, tx pgx.Tx, org, bot string) (qq.Token, error) {
	var blob []byte
	var out qq.Token
	if e := tx.QueryRow(ctx, `SELECT token_sealed FROM openfox.qq_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, bot).Scan(&blob); e != nil {
		return out, e
	}
	if len(blob) == 0 {
		return out, ErrNeedsReauth
	}
	plain, e := s.open(blob, qqAAD(org, bot, "token"))
	if e != nil {
		return out, e
	}
	if json.Unmarshal(plain, &out) != nil || out.AccessToken == "" {
		return out, ErrNeedsReauth
	}
	return out, nil
}

type qqRefresh struct {
	Bot         QQBot
	Lease       string
	Credentials qq.Credentials
}

func (w *IMWorker) processQQTokens(ctx context.Context, org string) error {
	var ids []string
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT bot_id::text FROM openfox.qq_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND state IN ('ACTIVE','WAITING_WEBHOOK') AND refresh_after<=clock_timestamp() AND (refresh_lease_expires_at IS NULL OR refresh_lease_expires_at<=clock_timestamp()) ORDER BY refresh_after,bot_id LIMIT 5`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	for _, id := range ids {
		var job qqRefresh
		claimed := false
		e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			b, e := lockQQBot(ctx, tx, id)
			if e != nil {
				return e
			}
			if b.removed || (b.State != "ACTIVE" && b.State != "WAITING_WEBHOOK") {
				return nil
			}
			var due bool
			var blob []byte
			e = tx.QueryRow(ctx, `SELECT q.refresh_after<=clock_timestamp() AND (q.refresh_lease_expires_at IS NULL OR q.refresh_lease_expires_at<=clock_timestamp()),c.credentials_sealed FROM openfox.qq_bot_controls q JOIN openfox.integration_connections c ON (c.organization_id,c.id)=(q.organization_id,q.connection_id) WHERE q.organization_id=openfox_private.current_organization_id() AND q.bot_id=$1::uuid`, id).Scan(&due, &blob)
			if e != nil || !due {
				return e
			}
			plain, e := w.Service.open(blob, qqAAD(org, id, "credentials"))
			if e != nil {
				return e
			}
			if json.Unmarshal(plain, &job.Credentials) != nil || !job.Credentials.Valid() || job.Credentials.AppID != b.AppID {
				return ErrNeedsReauth
			}
			job.Bot = b
			job.Lease, e = randomBase64URL(24)
			if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET refresh_lease_owner=$2,refresh_lease_expires_at=clock_timestamp()+interval '30 seconds' WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, id, job.Lease)
			claimed = e == nil
			return e
		})
		if e != nil {
			return e
		}
		if !claimed {
			continue
		}
		// Claim immediately before this request, never lease a batch then perform
		// serial HTTP calls while later leases silently expire.
		jobctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		token, cause := w.Service.qqClient().AccessToken(jobctx, job.Credentials)
		cancel()
		e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error { return w.finishQQRefresh(ctx, tx, org, job, token, cause) })
		if e != nil {
			return e
		}
	}
	return nil
}
func (w *IMWorker) finishQQRefresh(ctx context.Context, tx pgx.Tx, org string, job qqRefresh, token qq.Token, cause error) error {
	b, e := lockQQBot(ctx, tx, job.Bot.ID)
	if e != nil {
		return e
	}
	var current bool
	e = tx.QueryRow(ctx, `SELECT generation=$2 AND credential_version=$3 AND refresh_lease_owner=$4 AND refresh_lease_expires_at>clock_timestamp() AND state IN ('ACTIVE','WAITING_WEBHOOK') FROM openfox.qq_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, b.ID, job.Bot.Generation, job.Bot.CredentialVersion, job.Lease).Scan(&current)
	if e != nil || !current {
		return e
	}
	if cause == nil && token.AccessToken != "" && token.ExpiresAt.After(time.Now()) {
		raw, _ := json.Marshal(token)
		blob, key, e := w.Service.seal(raw, qqAAD(org, b.ID, "token"))
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET token_sealed=$2,token_key_id=$3,token_expires_at=$4,credential_version=credential_version+1,refresh_after=GREATEST(clock_timestamp()+interval '1 second',$4::timestamptz-interval '30 seconds'),refresh_lease_owner='',refresh_lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, b.ID, blob, key, token.ExpiresAt)
		return e
	}
	if errors.Is(cause, qq.ErrAuthentication) || errors.Is(cause, qq.ErrRejected) || errors.Is(cause, qq.ErrInvalid) {
		_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET state='NEEDS_REAUTH',status_reason='credentials_rejected',version=version+1,token_sealed=NULL,token_expires_at=NULL,refresh_after=NULL,refresh_lease_owner='',refresh_lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, b.ID)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='credentials_rejected',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, b.ConnectionID)
		return e
	}
	delay := time.Minute
	var upstream *qq.Error
	if errors.As(cause, &upstream) && errors.Is(cause, qq.ErrRateLimited) && upstream.RetryAfter > 0 {
		delay = upstream.RetryAfter
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET refresh_after=$2,refresh_lease_owner='',refresh_lease_expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, b.ID, time.Now().Add(delay))
	return e
}

// A 401 from a request using an older token must not invalidate a newly refreshed
// token. It schedules a bounded refresh, never restores a revoked installation.
func invalidateQQToken(ctx context.Context, tx pgx.Tx, bot string, generation, credentialVersion uint64) error {
	_, e := tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET token_sealed=NULL,token_key_id='',token_expires_at=NULL,refresh_after=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND generation=$2 AND credential_version=$3 AND state='ACTIVE'`, bot, generation, credentialVersion)
	return e
}
