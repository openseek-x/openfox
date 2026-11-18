package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/qq"
	"openfox/internal/integrations/qqconnector"
	"openfox/internal/membership"
)

// QQSessionManager bounds only owned SDK processes. Persistent session state
// remains authoritative; an expired lease is UNKNOWN and is never re-spawned.
type QQSessionManager struct {
	mu      sync.Mutex
	jobs    map[string]context.CancelFunc
	closing bool
	wg      sync.WaitGroup
}

func NewQQSessionManager() *QQSessionManager {
	return &QQSessionManager{jobs: map[string]context.CancelFunc{}}
}
func (m *QQSessionManager) start(ctx context.Context, key string, run func(context.Context)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || len(m.jobs) >= 5 || m.jobs[key] != nil {
		return false
	}
	child, cancel := context.WithCancel(ctx)
	m.jobs[key] = cancel
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		defer func() { m.mu.Lock(); delete(m.jobs, key); m.mu.Unlock() }()
		run(child)
	}()
	return true
}
func (m *QQSessionManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	m.closing = true
	for _, cancel := range m.jobs {
		cancel()
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(15 * time.Second):
		return errors.New("qq: owned connector shutdown deadline")
	}
}
func (s Service) qqConnector() qqconnector.Config {
	return qqconnector.Config{NodePath: s.QQConnectorNodePath, Root: s.QQConnectorRoot}
}

type QQQRSession struct {
	ID           string    `json:"id"`
	BotID        string    `json:"bot_id"`
	Status       string    `json:"status"`
	StatusReason string    `json:"status_reason"`
	Version      uint64    `json:"version"`
	Generation   uint64    `json:"generation"`
	QRURL        string    `json:"qr_url"`
	HasCandidate bool      `json:"has_candidate"`
	ExpiresAt    time.Time `json:"expires_at"`
}
type QQQRStartInput struct {
	Version        uint64 `json:"version"`
	IdempotencyKey string `json:"idempotency_key"`
}
type QQQRActionInput struct {
	Version       uint64 `json:"version"`
	RemoteChecked bool   `json:"remote_checked"`
}

const qqQRColumns = `id::text,bot_id::text,status,status_reason,version,generation,qr_url,candidate_sealed IS NOT NULL,expires_at`

func scanQQQR(row pgx.Row) (QQQRSession, error) {
	var v QQQRSession
	e := row.Scan(&v.ID, &v.BotID, &v.Status, &v.StatusReason, &v.Version, &v.Generation, &v.QRURL, &v.HasCandidate, &v.ExpiresAt)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return v, e
}
func (s Service) GetQQQRSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (QQQRSession, error) {
	if !membership.ValidateUUID(id) {
		return QQQRSession{}, ErrNotFound
	}
	v, e := scanQQQR(tx.QueryRow(ctx, `SELECT `+qqQRColumns+` FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id))
	if e != nil {
		return v, e
	}
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return v, e
	}
	b, e := loadQQBot(ctx, tx, v.BotID)
	if e != nil {
		return v, e
	}
	e = qqBotStopManager(ctx, tx, a, b)
	return v, e
}
func (s Service) StartQQQRSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, bot string, in QQQRStartInput) (QQQRSession, error) {
	if !s.QQConfigured() || !s.qqConnector().Configured() {
		return QQQRSession{}, ErrNotConfigured
	}
	if in.Version == 0 || len(in.IdempotencyKey) < 16 || len(in.IdempotencyKey) > 128 {
		return QQQRSession{}, ErrInvalidRequest
	}
	a, e := qqActor(ctx, tx, org, actor)
	if e != nil {
		return QQQRSession{}, e
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "qq-qr:"+org+":"+a.UserID+":"+in.IdempotencyKey); e != nil {
		return QQQRSession{}, e
	}
	b, e := lockQQBot(ctx, tx, bot)
	if e != nil {
		return QQQRSession{}, e
	}
	if e = qqBotManager(ctx, tx, a, b); e != nil {
		return QQQRSession{}, e
	}
	raw, _ := json.Marshal([]string{bot})
	hash := sha256.Sum256(raw)
	var previous []byte
	var id string
	e = tx.QueryRow(ctx, `SELECT id::text,request_hash FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND actor_id=$1::uuid AND operation_key=$2`, a.UserID, in.IdempotencyKey).Scan(&id, &previous)
	if e == nil {
		if !bytes.Equal(hash[:], previous) {
			return QQQRSession{}, ErrVersionConflict
		}
		return s.GetQQQRSession(ctx, tx, org, a, id)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return QQQRSession{}, e
	}
	// A verified App uses explicit same-App credential recovery. Never launch a
	// new remote QR creation to replace an already bound or uncertain App.
	if b.removed || b.Version != in.Version || b.AppID != "" || (b.State != "PREPARED" && b.State != "NEEDS_REAUTH") {
		return QQQRSession{}, ErrVersionConflict
	}
	var pending bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND status IN ('PENDING','POLLING','WAITING_SCAN','UNKNOWN'))`, bot).Scan(&pending); e != nil {
		return QQQRSession{}, e
	}
	if pending {
		return QQQRSession{}, ErrVersionConflict
	}
	id, e = membership.NewID()
	if e != nil {
		return QQQRSession{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.qq_qr_sessions(organization_id,id,bot_id,actor_id,actor_version,generation,operation_key,request_hash,expires_at) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,clock_timestamp()+interval '10 minutes')`, id, bot, a.UserID, a.AuthorizationVersion, b.Generation, in.IdempotencyKey, hash[:])
	if e != nil {
		return QQQRSession{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, bot)
	if e != nil {
		return QQQRSession{}, e
	}
	return s.GetQQQRSession(ctx, tx, org, a, id)
}
func (s Service) CancelQQQRSession(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in QQQRActionInput) (QQQRSession, error) {
	v, e := s.GetQQQRSession(ctx, tx, org, actor, id)
	if e != nil {
		return v, e
	}
	b, e := lockQQBot(ctx, tx, v.BotID)
	if e != nil {
		return v, e
	}
	v, e = scanQQQR(tx.QueryRow(ctx, `SELECT `+qqQRColumns+` FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id))
	if e != nil {
		return v, e
	}
	if v.Version != in.Version {
		return v, ErrVersionConflict
	}
	if v.Status == "CANCELLED" || v.Status == "EXPIRED" || v.Status == "FAILED" {
		return v, nil
	}
	if v.Status == "DONE" {
		return v, ErrVersionConflict
	}
	if v.Status == "UNKNOWN" && !in.RemoteChecked {
		return v, nil
	}
	state, reason := "CANCELLED", "cancelled_before_start"
	if v.Status != "PENDING" {
		state, reason = "UNKNOWN", "remote_outcome_unknown"
		if v.Status == "UNKNOWN" && in.RemoteChecked {
			var stopped bool
			if e = tx.QueryRow(ctx, `SELECT status_reason='local_helper_stopped' OR expires_at+interval '8 seconds'<=clock_timestamp() FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&stopped); e != nil {
				return v, e
			}
			if !stopped {
				return v, ErrVersionConflict
			}
			state, reason = "CANCELLED", "remote_checked_by_manager"
		}
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET status=$2,status_reason=$3,version=version+1,qr_url='',lease_owner='',lease_expires_at=NULL,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, state, reason)
	if e != nil {
		return v, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_bot_controls SET version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid`, b.ID)
	if e != nil {
		return v, e
	}
	return s.GetQQQRSession(ctx, tx, org, actor, id)
}

type qqQRWork struct {
	Session QQQRSession
	Actor   collaboration.Actor
	Lease   string
}

func (w *IMWorker) processQQSessions(ctx context.Context, org string) error {
	if w.QQSessions == nil || !w.Service.qqConnector().Configured() {
		return nil
	}
	var ids []string
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND status='PENDING' AND expires_at>clock_timestamp() ORDER BY created_at,id LIMIT 5`)
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
		key := org + ":" + id
		w.QQSessions.start(ctx, key, func(ctx context.Context) {
			if e := w.runQQSession(ctx, org, id); e != nil && !errors.Is(e, context.Canceled) {
				w.logger().Warn("QQ connector session stopped", "organization_id", org, "session_id", id, "reason", "session_unavailable")
			}
		})
	}
	return nil
}

func (w *IMWorker) claimQQSession(ctx context.Context, tx pgx.Tx, org, id string) (qqQRWork, bool, error) {
	var job qqQRWork
	var bot, actor string
	var version uint64
	e := tx.QueryRow(ctx, `SELECT bot_id::text,actor_id::text,actor_version FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&bot, &actor, &version)
	if e != nil {
		return job, false, e
	}
	b, e := lockQQBot(ctx, tx, bot)
	if e != nil {
		return job, false, e
	}
	job.Session, e = scanQQQR(tx.QueryRow(ctx, `SELECT `+qqQRColumns+` FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id))
	if e != nil {
		return job, false, e
	}
	if job.Session.Status != "PENDING" || !job.Session.ExpiresAt.After(time.Now()) {
		return job, false, nil
	}
	job.Actor, e = qqActor(ctx, tx, org, collaboration.Actor{UserID: actor, AuthorizationVersion: version})
	if e == nil {
		e = qqBotManager(ctx, tx, job.Actor, b)
	}
	if qqAuthorityDenied(e) || b.removed || b.Generation != job.Session.Generation || b.AppID != "" || (b.State != "PREPARED" && b.State != "NEEDS_REAUTH") {
		_, e = tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET status='FAILED',status_reason='authority_changed',version=version+1,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
		return job, false, e
	}
	if e != nil {
		return job, false, e
	}
	job.Lease, e = randomBase64URL(24)
	if e != nil {
		return job, false, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET status='POLLING',version=version+1,lease_owner=$2,lease_expires_at=LEAST(expires_at,clock_timestamp()+interval '30 seconds') WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, job.Lease)
	return job, e == nil, e
}
func (w *IMWorker) qqSessionCurrent(ctx context.Context, tx pgx.Tx, org string, job qqQRWork) (QQBot, error) {
	b, e := lockQQBot(ctx, tx, job.Session.BotID)
	if e != nil {
		return b, e
	}
	if b.removed || b.Generation != job.Session.Generation || b.State == "DISCONNECTED" || b.State == "CANCELLED" {
		return b, ErrForbidden
	}
	a, e := qqActor(ctx, tx, org, job.Actor)
	if e != nil {
		return b, e
	}
	if e = qqBotManager(ctx, tx, a, b); e != nil {
		return b, e
	}
	var current bool
	e = tx.QueryRow(ctx, `SELECT status IN ('POLLING','WAITING_SCAN') AND generation=$2 AND lease_owner=$3 AND lease_expires_at>clock_timestamp() AND expires_at>clock_timestamp() FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, job.Session.ID, job.Session.Generation, job.Lease).Scan(&current)
	if e != nil {
		return b, e
	}
	if !current {
		return b, ErrVersionConflict
	}
	return b, nil
}

func (w *IMWorker) runQQSession(ctx context.Context, org, id string) error {
	// Artifact hashing occurs before the claim, outside any database transaction.
	if e := w.Service.qqConnector().Validate(); e != nil {
		return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var bot string
			if e := tx.QueryRow(ctx, `SELECT bot_id::text FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&bot); e != nil {
				return e
			}
			if _, e := lockQQBot(ctx, tx, bot); e != nil {
				return e
			}
			_, e := tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET status='FAILED',status_reason='connector_not_verified',version=version+1,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='PENDING'`, id)
			return e
		})
	}
	var job qqQRWork
	claimed := false
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		job, claimed, e = w.claimQQSession(ctx, tx, org, id)
		return e
	})
	if e != nil || !claimed {
		return e
	}
	runctx, cancel := context.WithDeadline(ctx, job.Session.ExpiresAt)
	defer cancel()
	heartbeatDone := make(chan struct{})
	heartbeatStopped := make(chan struct{})
	var succeeded atomic.Bool
	go func() {
		defer close(heartbeatStopped)
		timer := time.NewTicker(2 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-heartbeatDone:
				return
			case <-runctx.Done():
				return
			case <-timer.C:
				if succeeded.Load() {
					return
				}
				e := w.inOrganization(runctx, org, func(ctx context.Context, tx pgx.Tx) error {
					if _, e := w.qqSessionCurrent(ctx, tx, org, job); e != nil {
						return e
					}
					_, e := tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET lease_expires_at=LEAST(expires_at,clock_timestamp()+interval '30 seconds') WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
					return e
				})
				if e != nil {
					if !succeeded.Load() {
						cancel()
					}
					return
				}
			}
		}
	}()
	var work QQConnectWork
	e = w.Service.qqConnector().Run(runctx, func(ctx context.Context, event qqconnector.Event) error {
		if event.Kind == "success" {
			succeeded.Store(true)
		}
		var candidate QQConnectWork
		err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			candidate, err = w.acceptQQSDKEvent(ctx, tx, org, job, event)
			return err
		})
		if err == nil && candidate.BotID != "" {
			work = candidate
		}
		return err
	})
	// Only the callback transaction can supply a durable credential candidate.
	// Stop the heartbeat before token verification changes the install generation.
	succeeded.Store(work.BotID != "")
	close(heartbeatDone)
	<-heartbeatStopped
	if work.BotID != "" {
		result := ExecuteQQConnection(ctx, w.Service.qqClient(), work)
		return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			b, e := w.Service.FinishQQConnection(ctx, tx, org, job.Actor, work, result)
			if e != nil {
				return e
			}
			reason := "credentials_rejected"
			if b.State == "WAITING_WEBHOOK" {
				reason = "webhook_pending"
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET status_reason=$2,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='DONE'`, id, reason)
			return e
		})
	}
	cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancelCleanup()
	return w.inOrganization(cleanup, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, x := lockQQBot(ctx, tx, job.Session.BotID); x != nil {
			return x
		}
		// Run has joined its only child before this write. A cancellation clears
		// the lease immediately, so accept the same immutable session/generation
		// in UNKNOWN as well; this is local stop proof, never remote non-creation.
		_, x := tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET status='UNKNOWN',status_reason='local_helper_stopped',qr_url='',version=version+1,lease_owner='',lease_expires_at=NULL,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND generation=$3 AND (status='UNKNOWN' OR lease_owner=$2 AND status IN ('POLLING','WAITING_SCAN'))`, id, job.Lease, job.Session.Generation)
		return x
	})
}

// Recover only a durably sealed SDK result, with current manager/version proof.
// It uses the existing prepare/HTTP/finish contract and never restarts QR creation.
func (s Service) PrepareQQQRCandidate(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) (QQConnectWork, error) {
	v, e := s.GetQQQRSession(ctx, tx, org, actor, id)
	if e != nil {
		return QQConnectWork{}, e
	}
	b, e := lockQQBot(ctx, tx, v.BotID)
	if e != nil {
		return QQConnectWork{}, e
	}
	var blob []byte
	var current bool
	e = tx.QueryRow(ctx, `SELECT version=$2 AND status='DONE',candidate_sealed FROM openfox.qq_qr_sessions WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id, version).Scan(&current, &blob)
	if e != nil {
		return QQConnectWork{}, e
	}
	if !current || len(blob) == 0 {
		return QQConnectWork{}, ErrVersionConflict
	}
	raw, e := s.open(blob, qqAAD(org, id, "qr-candidate"))
	if e != nil {
		return QQConnectWork{}, e
	}
	var creds qq.Credentials
	if json.Unmarshal(raw, &creds) != nil || !creds.Valid() {
		return QQConnectWork{}, ErrNeedsReauth
	}
	work, e := s.PrepareQQConnection(ctx, tx, org, actor, b.ID, QQConnectInput{Version: b.Version, AppID: creds.AppID, AppSecret: creds.AppSecret})
	if e != nil {
		return work, e
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET version=version+1,status_reason='validating_credentials' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id)
	return work, e
}

// Only the owned connector calls this internal callback boundary. It is not an
// HTTP API: credentials and URL events must be produced by the fixed SDK runner.
func (w *IMWorker) acceptQQSDKEvent(ctx context.Context, tx pgx.Tx, org string, job qqQRWork, event qqconnector.Event) (QQConnectWork, error) {
	var work QQConnectWork
	b, err := w.qqSessionCurrent(ctx, tx, org, job)
	if err != nil {
		return work, err
	}
	switch event.Kind {
	case "qr":
		_, err = tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET status='WAITING_SCAN',qr_url=$2,status_reason='',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job.Session.ID, event.URL)
	case "expired":
		_, err = tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET status='POLLING',qr_url='',status_reason='qr_refreshing',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job.Session.ID)
	case "success":
		candidate := qq.Credentials{AppID: event.AppID, AppSecret: event.AppSecret}
		if !candidate.Valid() {
			return work, ErrInvalidRequest
		}
		raw, _ := json.Marshal(candidate)
		blob, key, e := w.Service.seal(raw, qqAAD(org, job.Session.ID, "qr-candidate"))
		if e != nil {
			return work, e
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.qq_qr_sessions SET status='DONE',status_reason='validating_credentials',candidate_sealed=$2,candidate_key_id=$3,qr_url='',lease_owner='',lease_expires_at=NULL,version=version+1,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job.Session.ID, blob, key)
		if err != nil {
			return work, err
		}
		work, err = w.Service.PrepareQQConnection(ctx, tx, org, job.Actor, b.ID, QQConnectInput{Version: b.Version, AppID: candidate.AppID, AppSecret: candidate.AppSecret})
	default:
		return work, ErrInvalidRequest
	}
	return work, err
}
