package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"math"
	"openfox/internal/integrations/discord"
	"openfox/internal/membership"
	"time"
)

type discordAttempt struct {
	control           discordControl
	id, token         string
	fence, generation int64
	resume            *discord.Resume
	gateway           discord.GatewayBot
}

func (discordAttempt) String() string     { return "discordAttempt{[REDACTED]}" }
func (v discordAttempt) GoString() string { return v.String() }

// lockDiscordLease shares exactly the existing manager lease. No independent
// owner or in-memory sequence is allowed to authorize a callback.
func (m *LongConnManager) lockDiscordLease(ctx context.Context, tx pgx.Tx, key sessionKey) (discordControl, error) {
	var bot string
	e := tx.QueryRow(ctx, `SELECT bot_id::text FROM openfox.discord_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, key.connectionID).Scan(&bot)
	if e != nil {
		return discordControl{}, e
	}
	r, e := loadDiscordControl(ctx, tx, bot, true)
	if e != nil {
		return r, e
	}
	if r.bot.State != "ACTIVE" || r.parentStatus != StatusActive {
		return r, ErrDiscordSessionState
	}
	var valid bool
	e = tx.QueryRow(ctx, `SELECT lease_owner=$2 AND lease_expires_at>clock_timestamp() FROM openfox.im_connection_states WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR UPDATE`, key.connectionID, m.instance()).Scan(&valid)
	if e != nil {
		return r, e
	}
	if !valid {
		return r, ErrDiscordSessionState
	}
	return r, nil
}
func (m *LongConnManager) prepareDiscordAttempt(ctx context.Context, key sessionKey) (discordAttempt, error) {
	var a discordAttempt
	e := m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		r, e := m.lockDiscordLease(ctx, tx, key)
		if e != nil {
			return e
		}
		a.control = r
		cred, e := m.Service.openCredentials(key.organizationID, key.connectionID, r.credentials)
		a.token = cred.AccessToken
		return e
	})
	if e != nil {
		return a, e
	}
	a.gateway, e = m.Service.discordClient().GetGatewayBot(ctx, a.token)
	if e != nil {
		return a, e
	}
	if a.gateway.Starts.ResetAfter > math.MaxInt64/int64(time.Millisecond) {
		return a, discord.ErrResponse
	}
	if a.gateway.Shards != 1 {
		return a, ErrDiscordUnsupported
	}
	e = m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		r, e := m.lockDiscordLease(ctx, tx, key)
		if e != nil {
			return e
		}
		if r.authGeneration != a.control.authGeneration || r.tokenVersion != a.control.tokenVersion {
			return ErrDiscordSessionState
		}
		var sealed []byte
		var seq, remaining int64
		var reset, notBefore *time.Time
		e = tx.QueryRow(ctx, `SELECT resume_sealed,durable_seq,identify_remaining,identify_reset_at,identify_not_before FROM openfox.discord_gateway_sessions WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR UPDATE`, key.connectionID).Scan(&sealed, &seq, &remaining, &reset, &notBefore)
		if e != nil {
			return e
		}
		if len(sealed) > 0 {
			plain, e := m.Service.open(sealed, discordAAD(key.organizationID, "gateway", key.connectionID))
			if e != nil {
				return e
			}
			var resume discord.Resume
			if json.Unmarshal(plain, &resume) != nil {
				return ErrDiscordSessionState
			}
			resume.Sequence = seq
			a.resume = &resume
		}
		now := time.Now()
		if notBefore != nil && notBefore.After(now) {
			return &discord.Error{Kind: discord.ErrRateLimited, Code: "identify_cooldown", RetryAfter: notBefore.Sub(now)}
		}
		if a.resume == nil {
			if reset == nil || !reset.After(now) {
				remaining = a.gateway.Starts.Remaining
				v := now.Add(time.Duration(a.gateway.Starts.ResetAfter) * time.Millisecond)
				reset = &v
			} else if a.gateway.Starts.Remaining < remaining {
				remaining = a.gateway.Starts.Remaining
			}
			if remaining <= 0 {
				delay := time.Second
				if reset != nil && reset.After(now) {
					delay = reset.Sub(now)
				}
				return &discord.Error{Kind: discord.ErrRateLimited, Code: "identify_exhausted", RetryAfter: delay}
			}
			remaining--
			v := now.Add(5 * time.Second)
			notBefore = &v
		}
		id, e := membership.NewID()
		if e != nil {
			return e
		}
		a.id = id
		return tx.QueryRow(ctx, `UPDATE openfox.discord_gateway_sessions SET attempt_id=$2::uuid,lease_fence=lease_fence+1,identify_remaining=$3,identify_reset_at=$4,identify_not_before=$5,gateway_url=$6 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND authorization_generation=$7 AND token_version=$8 RETURNING lease_fence,session_generation`, key.connectionID, id, remaining, reset, notBefore, a.gateway.URL, r.authGeneration, r.tokenVersion).Scan(&a.fence, &a.generation)
	})
	return a, e
}
func (m *LongConnManager) checkDiscordAttempt(ctx context.Context, tx pgx.Tx, key sessionKey, a discordAttempt) (discordControl, error) {
	r, e := m.lockDiscordLease(ctx, tx, key)
	if e != nil {
		return r, e
	}
	if r.authGeneration != a.control.authGeneration || r.tokenVersion != a.control.tokenVersion {
		return r, ErrDiscordSessionState
	}
	var valid bool
	e = tx.QueryRow(ctx, `SELECT attempt_id=$2::uuid AND lease_fence=$3 AND session_generation=$4 AND authorization_generation=$5 AND token_version=$6 FROM openfox.discord_gateway_sessions WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR UPDATE`, key.connectionID, a.id, a.fence, a.generation, r.authGeneration, r.tokenVersion).Scan(&valid)
	if e != nil {
		return r, e
	}
	if !valid {
		return r, ErrDiscordSessionState
	}
	return r, nil
}
func (m *LongConnManager) persistDiscordDispatch(ctx context.Context, key sessionKey, a discordAttempt, event discord.Dispatch) error {
	return m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		r, e := m.checkDiscordAttempt(ctx, tx, key, a)
		if e != nil {
			return e
		}
		var seq int64
		if e = tx.QueryRow(ctx, `SELECT durable_seq FROM openfox.discord_gateway_sessions WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, key.connectionID).Scan(&seq); e != nil {
			return e
		}
		if event.Sequence < seq {
			return ErrDiscordSessionState
		}
		if event.Type == "READY" {
			var ready discord.Ready
			if json.Unmarshal(event.Data, &ready) != nil || ready.User.ID != r.bot.RemoteBotID || ready.Application.ID != r.bot.ApplicationID {
				return ErrDiscordSessionState
			}
			raw, _ := json.Marshal(discord.Resume{SessionID: ready.SessionID, URL: ready.ResumeURL, Sequence: event.Sequence})
			sealed, _, e := m.Service.seal(raw, discordAAD(key.organizationID, "gateway", key.connectionID))
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE openfox.discord_gateway_sessions SET resume_sealed=$2,gap_finished_at=CASE WHEN gap_started_at IS NOT NULL THEN clock_timestamp() ELSE gap_finished_at END WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, key.connectionID, sealed); e != nil {
				return e
			}
		}
		if e = receiveDiscordDispatch(ctx, tx, r, event); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE openfox.discord_gateway_sessions SET durable_seq=$2,durable_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, key.connectionID, event.Sequence); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.im_connection_states SET state='CONNECTED',last_event_at=clock_timestamp(),last_error='',failure_count=0,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, key.connectionID)
		return e
	})
}
func (m *LongConnManager) finishDiscordAttempt(ctx context.Context, key sessionKey, a discordAttempt, result discord.GatewayResult, cause error) error {
	return m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		r, e := m.checkDiscordAttempt(ctx, tx, key, a)
		if e != nil {
			return e
		}
		code := "gateway_disconnected"
		var de *discord.Error
		if errors.As(cause, &de) && de.Code != "" {
			code = de.Code
		}
		if result.IdentifyRequired {
			if _, e = tx.Exec(ctx, `UPDATE openfox.discord_gateway_sessions SET resume_sealed=NULL,durable_seq=-1,session_generation=session_generation+1,gap_count=gap_count+1,gap_started_at=clock_timestamp(),gap_finished_at=NULL,gap_reason=$2 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, key.connectionID, code); e != nil {
				return e
			}
		}
		permanent := errors.Is(cause, discord.ErrAuth) || errors.Is(cause, discord.ErrForbidden) || errors.Is(cause, ErrDiscordUnsupported) || errors.Is(cause, discord.ErrUnsupported)
		if permanent {
			if _, e = tx.Exec(ctx, `UPDATE openfox.discord_bot_controls SET state='ATTENTION',last_error=$2,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, r.bot.ID, code); e != nil {
				return e
			}
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.im_connection_states SET state=CASE WHEN $2 THEN 'FAILED' ELSE 'RECONNECTING' END,last_error=$3,failure_count=failure_count+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, key.connectionID, permanent, code)
		return e
	})
}
func (m *LongConnManager) runDiscordSession(ctx context.Context, key sessionKey) {
	failures := 0
	for ctx.Err() == nil {
		a, e := m.prepareDiscordAttempt(ctx, key)
		if e == nil {
			intents := discord.IntentGuilds | discord.IntentGuildMessages | discord.IntentDirectMessages
			if a.control.bot.MessageContentEnabled {
				intents |= discord.IntentMessageContent
			}
			var result discord.GatewayResult
			result, e = m.Service.discordClient().RunGateway(ctx, discord.GatewayConfig{Token: a.token, BotID: a.control.bot.RemoteBotID, ApplicationID: a.control.bot.ApplicationID, Gateway: a.gateway, Intents: intents, Resume: a.resume, OnDispatch: func(c context.Context, d discord.Dispatch) error { return m.persistDiscordDispatch(c, key, a, d) }})
			if ctx.Err() != nil {
				return
			}
			// Persistence failures stop immediately: no new callback or socket may run
			// after the database failed to prove this owner's lease.
			var de *discord.Error
			if errors.As(e, &de) && de.Code == "gateway_persistence_failed" {
				return
			}
			if settle := m.finishDiscordAttempt(ctx, key, a, result, e); settle != nil {
				return
			}
		}
		if errors.Is(e, ErrDiscordSessionState) {
			return
		}
		if errors.Is(e, discord.ErrAuth) || errors.Is(e, discord.ErrForbidden) || errors.Is(e, ErrDiscordUnsupported) || errors.Is(e, discord.ErrUnsupported) {
			// Preparation can fail before an attempt id exists. Fence on the current
			// lease and matching token generation before marking attention.
			_ = m.inOrganization(ctx, key.organizationID, func(c context.Context, tx pgx.Tx) error {
				r, err := m.lockDiscordLease(c, tx, key)
				if err != nil {
					return err
				}
				if r.tokenVersion != a.control.tokenVersion {
					return ErrDiscordSessionState
				}
				_, err = tx.Exec(c, `UPDATE openfox.discord_bot_controls SET state='ATTENTION',last_error='gateway_capability',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, r.bot.ID)
				return err
			})
			return
		}
		failures++
		delay := m.backoff(e, failures)
		var de *discord.Error
		if errors.As(e, &de) && de.RetryAfter > delay {
			delay = de.RetryAfter
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
