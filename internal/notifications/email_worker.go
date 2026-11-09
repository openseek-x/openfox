package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/tenancy"
)

const (
	emailPollInterval  = 2 * time.Second
	emailScanLimit     = 100
	emailMaxAttempts   = 6
	emailCleanupBatch  = 100
	emailWorkerActorID = "00000000-0000-0000-0000-000000000000"
)

type Runner interface {
	RunControlPlane(context.Context, func(context.Context, pgx.Tx) error) error
	RunForOrganization(context.Context, string, tenancy.Source, tenancy.OrganizationSelector, func(context.Context, pgx.Tx) error) error
}

type Sender interface {
	Send(context.Context, string, string, string) error
}

type EmailWorker struct {
	Pool      Runner
	Sender    Sender
	Logger    *slog.Logger
	Instance  string
	PublicURL string
	Interval  time.Duration

	cursor string
}

type emailItem struct {
	ID              string
	RecipientUserID string
	Kind            string
	Summary         string
	Email           string
	Attempts        int
	ExpiresAt       time.Time
}

func (w *EmailWorker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

func (w *EmailWorker) instance() string {
	if w.Instance == "" {
		w.Instance = "email-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return w.Instance
}

func (w *EmailWorker) Run(ctx context.Context) error {
	if w.Pool == nil || (w.Sender != nil && strings.TrimSpace(w.PublicURL) == "") {
		return errors.New("notifications: email worker is not configured")
	}
	interval := w.Interval
	if interval <= 0 {
		interval = emailPollInterval
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if err := w.RunOnce(ctx); err != nil && ctx.Err() == nil {
			w.logger().Warn("email_notification_pass_failed", "error", err.Error())
		}
		timer.Reset(interval)
	}
}

func (w *EmailWorker) RunOnce(ctx context.Context) error {
	if w.Pool == nil || (w.Sender != nil && strings.TrimSpace(w.PublicURL) == "") {
		return errors.New("notifications: email worker is not configured")
	}
	var organizations []string
	err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var after any
		if w.cursor != "" {
			after = w.cursor
		}
		rows, err := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.list_email_notification_organizations($1::uuid, $2, $3)`, after, emailScanLimit, w.Sender != nil)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var organizationID string
			if err := rows.Scan(&organizationID); err != nil {
				return err
			}
			organizations = append(organizations, organizationID)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, organizationID := range organizations {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.RunOrganization(ctx, organizationID); err != nil {
			w.logger().Warn("email_notification_organization_failed", "organization_id", organizationID, "error", err.Error())
		}
		w.cursor = organizationID
	}
	if len(organizations) < emailScanLimit {
		w.cursor = ""
	}
	return nil
}

func (w *EmailWorker) inOrganization(ctx context.Context, organizationID string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: emailWorkerActorID, AuthorizationVersion: 1}
	return w.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), fn)
}

func (w *EmailWorker) RunOrganization(ctx context.Context, organizationID string) error {
	var item *emailItem
	err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := cleanupEmailItems(ctx, tx); err != nil {
			return err
		}
		if w.Sender == nil {
			return nil
		}
		claimed, err := w.claim(ctx, tx)
		if err != nil || claimed == nil {
			return err
		}
		item = claimed
		if !claimed.ExpiresAt.After(time.Now()) {
			item = nil
			return finishEmailItem(ctx, tx, claimed.ID, w.instance(), "SKIPPED", "expired")
		}
		var active, enabled, direct bool
		var disabled []string
		err = tx.QueryRow(ctx, `
			SELECT u.email, COALESCE(m.status = 'ACTIVE' AND c.status = 'ACTIVE', false),
			       COALESCE(ch.enabled, false), COALESCE(ch.direct_messages, false),
			       COALESCE(ch.disabled_events, '{}'::text[])
			  FROM openfox.users u
			  LEFT JOIN openfox.organization_members m
			    ON m.organization_id = openfox_private.current_organization_id() AND m.user_id = u.id
			  LEFT JOIN openfox.password_credentials c ON c.user_id = u.id
			  LEFT JOIN openfox.user_notification_channels ch ON ch.user_id = u.id AND ch.channel = 'email'
			 WHERE u.id = $1::uuid`, claimed.RecipientUserID).
			Scan(&claimed.Email, &active, &enabled, &direct, &disabled)
		if errors.Is(err, pgx.ErrNoRows) {
			item = nil
			return finishEmailItem(ctx, tx, claimed.ID, w.instance(), "SKIPPED", "recipient_unavailable")
		}
		if err != nil {
			return err
		}
		reason := ""
		switch {
		case !active:
			reason = "recipient_unavailable"
		case !enabled:
			reason = "channel_disabled"
		case claimed.Kind == "DIRECT_MESSAGE" && !direct:
			reason = "event_disabled"
		case contains(disabled, emailEventForKind(claimed.Kind)):
			reason = "event_disabled"
		}
		if reason != "" {
			item = nil
			return finishEmailItem(ctx, tx, claimed.ID, w.instance(), "SKIPPED", reason)
		}
		return nil
	})
	if err != nil || item == nil {
		return err
	}
	link := strings.TrimRight(w.PublicURL, "/") + "/o/" + url.PathEscape(strings.ToLower(organizationID)) + "/inbox"
	body := strings.TrimSpace(item.Summary) + "\n\n在 OpenFox 中查看：" + link
	sendErr := w.Sender.Send(ctx, item.Email, "OpenFox 通知", body)
	return w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		if sendErr == nil {
			return finishEmailItem(ctx, tx, item.ID, w.instance(), "DONE", "")
		}
		if item.Attempts+1 >= emailMaxAttempts || !item.ExpiresAt.After(time.Now()) {
			w.logger().Warn("email_notification_delivery_failed", "organization_id", organizationID, "notification_item_id", item.ID, "error", sendErr.Error())
			return failEmailItem(ctx, tx, item.ID, w.instance())
		}
		return deferEmailItem(ctx, tx, item.ID, w.instance(), item.Attempts+1)
	})
}

func (w *EmailWorker) claim(ctx context.Context, tx pgx.Tx) (*emailItem, error) {
	var item emailItem
	err := tx.QueryRow(ctx, `
		WITH due AS (
			SELECT id FROM openfox.email_notification_outbox
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND ((status = 'PENDING' AND (next_attempt_at <= clock_timestamp() OR expires_at <= clock_timestamp()))
			     OR (status = 'SENDING' AND (lease_expires_at IS NULL OR lease_expires_at <= clock_timestamp())))
			 ORDER BY next_attempt_at, created_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		UPDATE openfox.email_notification_outbox q
		   SET status = 'SENDING', lease_owner = $1,
		       lease_expires_at = clock_timestamp() + interval '3 minutes', updated_at = clock_timestamp()
		  FROM due
		 WHERE q.organization_id = openfox_private.current_organization_id() AND q.id = due.id
		RETURNING q.id::text, q.recipient_user_id::text, q.kind, q.safe_summary, q.attempts, q.expires_at`,
		w.instance()).Scan(&item.ID, &item.RecipientUserID, &item.Kind, &item.Summary, &item.Attempts, &item.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func finishEmailItem(ctx context.Context, tx pgx.Tx, itemID, instance, status, reason string) error {
	result, err := tx.Exec(ctx, `
		UPDATE openfox.email_notification_outbox
		   SET status = $3, last_error = $4, lease_owner = '', lease_expires_at = NULL,
		       finished_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND status = 'SENDING' AND lease_owner = $2`, itemID, instance, status, reason)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("notifications: email item %s lease lost", itemID)
	}
	return nil
}

func deferEmailItem(ctx context.Context, tx pgx.Tx, itemID, instance string, attempts int) error {
	wait := 30 * time.Second
	for index := 1; index < attempts && wait < 30*time.Minute; index++ {
		wait *= 2
	}
	if wait > 30*time.Minute {
		wait = 30 * time.Minute
	}
	wait = time.Duration(float64(wait) * (0.8 + rand.Float64()*0.4))
	result, err := tx.Exec(ctx, `
		UPDATE openfox.email_notification_outbox
		   SET status = 'PENDING', attempts = $3,
		       next_attempt_at = clock_timestamp() + make_interval(secs => $4::float8),
		       last_error = 'delivery_failed', lease_owner = '', lease_expires_at = NULL,
		       updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND status = 'SENDING' AND lease_owner = $2`, itemID, instance, attempts, wait.Seconds())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("notifications: email item %s lease lost", itemID)
	}
	return nil
}

func failEmailItem(ctx context.Context, tx pgx.Tx, itemID, instance string) error {
	result, err := tx.Exec(ctx, `
		UPDATE openfox.email_notification_outbox
		   SET status = 'FAILED', attempts = attempts + 1, last_error = 'delivery_failed',
		       lease_owner = '', lease_expires_at = NULL, finished_at = clock_timestamp(),
		       updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND status = 'SENDING' AND lease_owner = $2`, itemID, instance)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("notifications: email item %s lease lost", itemID)
	}
	return nil
}

func cleanupEmailItems(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `
		WITH expired AS (
			SELECT id FROM openfox.email_notification_outbox
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND expires_at <= clock_timestamp()
			   AND (status = 'PENDING' OR (status = 'SENDING'
			        AND (lease_expires_at IS NULL OR lease_expires_at <= clock_timestamp())))
			 ORDER BY expires_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		UPDATE openfox.email_notification_outbox q
		   SET status = 'SKIPPED', last_error = 'expired', lease_owner = '', lease_expires_at = NULL,
		       finished_at = clock_timestamp(), updated_at = clock_timestamp()
		  FROM expired
		 WHERE q.organization_id = openfox_private.current_organization_id() AND q.id = expired.id`, emailCleanupBatch); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		DELETE FROM openfox.email_notification_outbox q
		 USING (SELECT id FROM openfox.email_notification_outbox
		         WHERE organization_id = openfox_private.current_organization_id()
		           AND finished_at < clock_timestamp() - interval '30 days'
		         ORDER BY finished_at LIMIT $1) old
		 WHERE q.organization_id = openfox_private.current_organization_id() AND q.id = old.id`, emailCleanupBatch)
	return err
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func emailEventForKind(kind string) string {
	switch kind {
	case "DIRECT_MESSAGE":
		return "human_chat"
	case "TASK_ASSIGNED", "TRIAGE_NEW":
		return "assign"
	case "TASK_UPDATED":
		return "status"
	case "TASK_REACTION", "TASK_COMMENT", "REPLY":
		return "comment"
	case "MENTION":
		return "mention"
	case "AUTOMATION_RESULT":
		return "automation"
	default:
		return "agent"
	}
}
