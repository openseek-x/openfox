package stage3

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"openfox/internal/api"
	"openfox/internal/notifications"
	"openfox/internal/tenancy"
)

type notificationMail struct {
	mu       sync.Mutex
	failures int
	messages []struct{ to, body string }
}

func (sender *notificationMail) Send(_ context.Context, to, _, body string) error {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.failures > 0 {
		sender.failures--
		return context.DeadlineExceeded
	}
	sender.messages = append(sender.messages, struct{ to, body string }{to, body})
	return nil
}

func (sender *notificationMail) snapshot() []struct{ to, body string } {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	return append([]struct{ to, body string }(nil), sender.messages...)
}

func insertEmailTestNotice(env *s3Env, recipientID, taskID, kind, key, summary string) {
	env.t.Helper()
	selection := tenancy.OrganizationSelection{OrganizationID: env.org,
		ActorID: "00000000-0000-0000-0000-000000000000", AuthorizationVersion: 1}
	err := env.database.RunForOrganization(env.ctx, env.org, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
			return selection, nil
		}), func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT openfox_private.enqueue_notification(
				$1::uuid,$2,NULL::uuid,NULL::uuid,$3::uuid,NULL::uuid,$4,$5)`,
				recipientID, kind, taskID, key, summary)
			return err
		})
	if err != nil {
		env.t.Fatal(err)
	}
}

func TestEmailNotificationDelivery(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	t.Cleanup(func() {
		env.exec(`DELETE FROM openfox.user_notification_channels WHERE user_id=$1::uuid AND channel='email'`, s3MemberID)
		env.exec(`UPDATE openfox.users SET email=$2 WHERE id=$1::uuid`, s3MemberID, s3Email(s3MemberID))
	})
	sender := &notificationMail{}
	auth := env.auth
	auth.EmailSender = sender
	env.server = httptestServer(env, api.NewWithDispatch(auth, env.database, false, nil,
		api.DefaultAuthRateLimits, api.DispatchOptions{}))
	env.clients = map[string]*s3Client{}
	client := env.client(s3MemberID)
	task := env.createTask(env.client(s3OwnerID), env.space.ID, nil)
	worker := &notifications.EmailWorker{Pool: env.database, Sender: sender, PublicURL: "https://openfox.example", Instance: s3NewID(t)}
	insert := func(kind, summary string) string {
		t.Helper()
		key := "email-test:" + s3NewID(t)
		insertEmailTestNotice(env, s3MemberID, task.ID, kind, key, summary)
		return key
	}
	queue := func(key string) (string, int) {
		t.Helper()
		var status string
		var attempts int
		env.scalar(&status, `SELECT q.status FROM openfox.email_notification_outbox q
			JOIN openfox.notifications n ON n.organization_id=q.organization_id AND n.id=q.notification_id
			WHERE n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, key)
		env.scalar(&attempts, `SELECT q.attempts FROM openfox.email_notification_outbox q
			JOIN openfox.notifications n ON n.organization_id=q.organization_id AND n.id=q.notification_id
			WHERE n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, key)
		return status, attempts
	}
	run := func() {
		t.Helper()
		if err := worker.RunOnce(env.ctx); err != nil {
			t.Fatal(err)
		}
	}

	before := insert("MENTION", "Before opting in")
	if count := env.count(`SELECT count(*) FROM openfox.email_notification_outbox q
		JOIN openfox.notifications n ON n.organization_id=q.organization_id AND n.id=q.notification_id
		WHERE n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, before); count != 0 {
		t.Fatalf("default email queue count = %d, want 0", count)
	}
	var channels struct {
		Channels []notificationChannelView `json:"channels"`
	}
	client.must(http.StatusOK, http.MethodGet, "/v1/me/notification-channels", nil, &channels)
	if len(channels.Channels) != 5 || !channels.Channels[3].Available || channels.Channels[3].Enabled {
		t.Fatalf("email channel = %+v, want available but off", channels.Channels)
	}
	client.must(http.StatusOK, http.MethodPut, "/v1/me/notification-channels/email",
		map[string]any{"enabled": true, "disabled_events": []string{}}, nil)
	key := insert("MENTION", "Safe summary only")
	run()
	if status, _ := queue(key); status != "DONE" {
		t.Fatalf("delivery status = %s", status)
	}
	messages := sender.snapshot()
	if len(messages) != 1 || messages[0].to != s3Email(s3MemberID) ||
		!strings.Contains(messages[0].body, "Safe summary only") ||
		!strings.Contains(messages[0].body, "/o/"+env.org+"/inbox") {
		t.Fatalf("sent email = %+v", messages)
	}
	run()
	if len(sender.snapshot()) != 1 {
		t.Fatal("completed outbox item was sent twice")
	}

	direct := insert("DIRECT_MESSAGE", "Private message arrived")
	if count := env.count(`SELECT count(*) FROM openfox.email_notification_outbox q
		JOIN openfox.notifications n ON n.organization_id=q.organization_id AND n.id=q.notification_id
		WHERE n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, direct); count != 0 {
		t.Fatalf("direct message queue count = %d, want 0", count)
	}
	client.must(http.StatusOK, http.MethodPut, "/v1/me/notification-channels/email",
		map[string]any{"enabled": true, "disabled_events": []string{}, "direct_messages": true}, nil)
	direct = insert("DIRECT_MESSAGE", "Private message arrived")
	run()
	if status, _ := queue(direct); status != "DONE" {
		t.Fatalf("direct message delivery = %s", status)
	}

	client.must(http.StatusOK, http.MethodPut, "/v1/me/notification-channels/email",
		map[string]any{"enabled": true, "disabled_events": []string{"mention"}, "direct_messages": false}, nil)
	filtered := insert("MENTION", "Filtered")
	run()
	if status, _ := queue(filtered); status != "SKIPPED" {
		t.Fatalf("filtered email = %s", status)
	}
	client.must(http.StatusOK, http.MethodPut, "/v1/me/notification-channels/email",
		map[string]any{"enabled": true, "disabled_events": []string{}}, nil)
	sender.mu.Lock()
	sender.failures = 1
	sender.mu.Unlock()
	retry := insert("MENTION", "Retry")
	run()
	if status, attempts := queue(retry); status != "PENDING" || attempts != 1 {
		t.Fatalf("retry state = %s, attempts %d", status, attempts)
	}
	env.exec(`UPDATE openfox.email_notification_outbox q SET next_attempt_at=clock_timestamp()
		FROM openfox.notifications n WHERE n.organization_id=q.organization_id AND n.id=q.notification_id
		AND n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, retry)
	run()
	if status, _ := queue(retry); status != "DONE" {
		t.Fatalf("retry completion = %s", status)
	}

	expired := insert("MENTION", "Expired")
	env.exec(`UPDATE openfox.email_notification_outbox q SET expires_at=clock_timestamp()-interval '1 second'
		FROM openfox.notifications n WHERE n.organization_id=q.organization_id AND n.id=q.notification_id
		AND n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, expired)
	run()
	if status, _ := queue(expired); status != "SKIPPED" {
		t.Fatalf("expired email = %s", status)
	}

	newEmail := "changed-" + s3NewID(t) + "@example.test"
	current := insert("MENTION", "Current email")
	env.exec(`UPDATE openfox.users SET email=$2 WHERE id=$1::uuid`, s3MemberID, newEmail)
	run()
	if status, _ := queue(current); status != "DONE" {
		t.Fatalf("changed email delivery = %s", status)
	}
	messages = sender.snapshot()
	if messages[len(messages)-1].to != newEmail {
		t.Fatalf("delivery destination = %s, want current email", messages[len(messages)-1].to)
	}

	queued := insert("MENTION", "Before disabling")
	client.must(http.StatusOK, http.MethodPut, "/v1/me/notification-channels/email",
		map[string]any{"enabled": false, "disabled_events": []string{}}, nil)
	run()
	if status, _ := queue(queued); status != "SKIPPED" {
		t.Fatalf("disabled email delivery = %s", status)
	}

	client.must(http.StatusOK, http.MethodPut, "/v1/me/notification-channels/email",
		map[string]any{"enabled": true, "disabled_events": []string{}}, nil)
	removed := insert("MENTION", "Before revocation")
	env.exec(`UPDATE openfox.organization_members SET status='REVOKED'
		WHERE organization_id=$1::uuid AND user_id=$2::uuid`, env.org, s3MemberID)
	run()
	if status, _ := queue(removed); status != "SKIPPED" {
		t.Fatalf("revoked recipient delivery = %s", status)
	}
}

func TestEmailNotificationRetryLimit(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	t.Cleanup(func() {
		env.exec(`DELETE FROM openfox.user_notification_channels WHERE user_id=$1::uuid AND channel='email'`, s3MemberID)
	})
	sender := &notificationMail{failures: 6}
	worker := &notifications.EmailWorker{Pool: env.database, Sender: sender, PublicURL: "https://openfox.example", Instance: s3NewID(t)}
	task := env.createTask(env.client(s3OwnerID), env.space.ID, nil)
	env.exec(`INSERT INTO openfox.user_notification_channels (user_id,channel,enabled)
		VALUES ($1::uuid,'email',true)`, s3MemberID)
	key := "email-retry:" + s3NewID(t)
	insertEmailTestNotice(env, s3MemberID, task.ID, "MENTION", key, "Retry limit")
	for attempt := 1; attempt <= 6; attempt++ {
		if err := worker.RunOnce(env.ctx); err != nil {
			t.Fatal(err)
		}
		if attempt < 6 {
			env.exec(`UPDATE openfox.email_notification_outbox SET next_attempt_at=clock_timestamp()
				WHERE organization_id=$1::uuid AND status='PENDING'`, env.org)
		}
	}
	var status string
	var attempts int
	env.scalar(&status, `SELECT q.status FROM openfox.email_notification_outbox q
		JOIN openfox.notifications n ON n.organization_id=q.organization_id AND n.id=q.notification_id
		WHERE n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, key)
	env.scalar(&attempts, `SELECT q.attempts FROM openfox.email_notification_outbox q
		JOIN openfox.notifications n ON n.organization_id=q.organization_id AND n.id=q.notification_id
		WHERE n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, key)
	if status != "FAILED" || attempts != 6 || len(sender.snapshot()) != 0 {
		t.Fatalf("terminal retry = %s, attempts %d, sent %d", status, attempts, len(sender.snapshot()))
	}
}

func TestEmailNotificationCleanupWithoutSMTP(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	t.Cleanup(func() {
		env.exec(`DELETE FROM openfox.user_notification_channels WHERE user_id=$1::uuid AND channel='email'`, s3MemberID)
	})
	task := env.createTask(env.client(s3OwnerID), env.space.ID, nil)
	env.exec(`INSERT INTO openfox.user_notification_channels (user_id,channel,enabled)
		VALUES ($1::uuid,'email',true)`, s3MemberID)
	key := "email-no-smtp:" + s3NewID(t)
	insertEmailTestNotice(env, s3MemberID, task.ID, "MENTION", key, "No SMTP")
	worker := &notifications.EmailWorker{Pool: env.database, Instance: s3NewID(t)}
	status := func() string {
		t.Helper()
		var value string
		env.scalar(&value, `SELECT q.status FROM openfox.email_notification_outbox q
			JOIN openfox.notifications n ON n.organization_id=q.organization_id AND n.id=q.notification_id
			WHERE n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, key)
		return value
	}
	if err := worker.RunOnce(env.ctx); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "PENDING" {
		t.Fatalf("unexpired email without SMTP = %s, want PENDING", got)
	}
	env.exec(`UPDATE openfox.email_notification_outbox q
		SET status='SENDING', lease_owner='other-worker', lease_expires_at=clock_timestamp()+interval '1 hour',
		    expires_at=clock_timestamp()-interval '1 second'
		FROM openfox.notifications n WHERE n.organization_id=q.organization_id AND n.id=q.notification_id
		AND n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, key)
	if err := worker.RunOnce(env.ctx); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "SENDING" {
		t.Fatalf("active delivery lease without SMTP = %s, want SENDING", got)
	}
	env.exec(`UPDATE openfox.email_notification_outbox q SET lease_expires_at=clock_timestamp()-interval '1 second'
		FROM openfox.notifications n WHERE n.organization_id=q.organization_id AND n.id=q.notification_id
		AND n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, key)
	if err := worker.RunOnce(env.ctx); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "SKIPPED" {
		t.Fatalf("expired email without SMTP = %s, want SKIPPED", got)
	}
	env.exec(`UPDATE openfox.email_notification_outbox q SET finished_at=clock_timestamp()-interval '31 days'
		FROM openfox.notifications n WHERE n.organization_id=q.organization_id AND n.id=q.notification_id
		AND n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, key)
	if err := worker.RunOnce(env.ctx); err != nil {
		t.Fatal(err)
	}
	if count := env.count(`SELECT count(*) FROM openfox.email_notification_outbox q
		JOIN openfox.notifications n ON n.organization_id=q.organization_id AND n.id=q.notification_id
		WHERE n.organization_id=$1::uuid AND n.dedupe_key=$2`, env.org, key); count != 0 {
		t.Fatalf("retained old email without SMTP = %d, want 0", count)
	}
}
