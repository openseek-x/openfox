package stage3

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations"
)

func TestAsanaIdentityClaimOwnerCompetitionAndLastDisconnect(t *testing.T) {
	e := newAsanaEnv(t)
	parent := e.connect("101")
	first := e.authorize(e.member(), integrations.AsanaStartInput{Purpose: "personal", ConnectionID: parent.ID})
	e.fake.mu.Lock()
	e.fake.email = s3Email(s3OtherID)
	e.fake.mu.Unlock()
	second := e.authorize(e.other(), integrations.AsanaStartInput{Purpose: "personal", ConnectionID: parent.ID})
	type result struct {
		user, session string
		conn          integrations.Connection
		err           error
	}
	responses := make(chan result, 2)
	start := make(chan struct{})
	for _, request := range []result{{user: s3MemberID, session: first.ID}, {user: s3OtherID, session: second.ID}} {
		go func(request result) {
			<-start
			request.err = e.tx(request.user, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
				var err error
				request.conn, _, err = e.svc.CompleteAsanaPersonal(ctx, tx, e.org, actor, request.session)
				return err
			})
			responses <- request
		}(request)
	}
	close(start)
	var winner, loser result
	for i := 0; i < 2; i++ {
		r := <-responses
		if r.err == nil {
			winner = r
		} else if errors.Is(r.err, integrations.ErrPersonalInUse) {
			loser = r
		} else {
			t.Fatal(r.err)
		}
	}
	if winner.user == "" || loser.user == "" {
		t.Fatal("identity competition did not serialize")
	}
	// Hold the last disconnect uncommitted. A different owner cannot claim the
	// still-visible identity; retry after commit uses the fresh verified proof.
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- e.tx(winner.user, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
			_, err := e.svc.Disconnect(ctx, tx, e.org, actor, winner.conn.ID, winner.conn.Version, "")
			if err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not hold")
	}
	err := e.tx(loser.user, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
		_, _, err := e.svc.CompleteAsanaPersonal(ctx, tx, e.org, actor, loser.session)
		return err
	})
	if !errors.Is(err, integrations.ErrPersonalInUse) {
		t.Fatalf("uncommitted disconnect allowed transfer: %v", err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	err = e.tx(loser.user, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
		_, _, err := e.svc.CompleteAsanaPersonal(ctx, tx, e.org, actor, loser.session)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	e.scalar(&owner, `SELECT owner_user_id::text FROM openfox.integration_asana_identity_claims WHERE organization_id=$1::uuid AND subject_id='200'`, e.org)
	if owner != loser.user {
		t.Fatal("claim did not transfer after last disconnect")
	}
	var count int
	e.scalar(&count, `SELECT count(*) FROM openfox.integration_connections WHERE organization_id=$1::uuid AND asana_identity_subject='200'`, e.org)
	if count != 1 {
		t.Fatal("more than one owner remains")
	}
}
func TestAsanaRefreshLateResultAfterDisconnect(t *testing.T) {
	e := newAsanaEnv(t)
	parent := e.connect("101")
	e.exec(`UPDATE openfox.integration_asana_grants SET expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid`, e.org)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.fake.mu.Lock()
	e.fake.refreshHook = func() { once.Do(func() { close(entered) }); <-release }
	e.fake.mu.Unlock()
	result := make(chan error, 1)
	go func() {
		_, err := e.runtime.Do(e.ctx, e.org, parent.ID, 0, 0, func(string) error { return nil })
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no refresh")
	}
	e.owner().must(200, "POST", "integrations/connections/"+parent.ID+"/disconnect", map[string]any{"version": parent.Version, "confirm_phrase": integrations.ConfirmPhrase("Asana")}, nil)
	close(release)
	if err := <-result; !errors.Is(err, integrations.ErrVersionConflict) {
		t.Fatalf("late refresh result %v", err)
	}
	var cleared bool
	e.scalar(&cleared, `SELECT status='DISCONNECTED' AND credentials_sealed IS NULL AND credentials_key_id='' AND refresh_lease IS NULL FROM openfox.integration_asana_grants WHERE organization_id=$1::uuid`, e.org)
	if !cleared {
		t.Fatal("late refresh resurrected secrets")
	}
}
func TestAsanaProjectsLateResultChecksConnectionAndActor(t *testing.T) {
	for _, mode := range []string{"disconnect", "revoke"} {
		t.Run(mode, func(t *testing.T) {
			e := newAsanaEnv(t)
			parent := e.connect("101")
			owner := e.owner()
			entered, release := make(chan struct{}), make(chan struct{})
			var releasing sync.Once
			defer releasing.Do(func() { close(release) })
			var once sync.Once
			// The server-owned transport still performs the actual TLS HTTP request.
			original := e.client.Transport
			e.client.Transport = asanaHoldTransport{next: original, hold: func() { once.Do(func() { close(entered) }); <-release }}
			done := make(chan s3Response, 1)
			go func() { done <- owner.do("GET", "imports/asana/projects?connection_id="+parent.ID, nil) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("no held project request")
			}
			if mode == "disconnect" {
				owner.must(200, "POST", "integrations/connections/"+parent.ID+"/disconnect", map[string]any{"version": parent.Version, "confirm_phrase": integrations.ConfirmPhrase("Asana")}, nil)
			} else {
				var before, after uint64
				e.scalar(&before, `SELECT authorization_version FROM openfox.organization_members WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, s3OwnerID)
				if err := e.tx(s3OwnerID, func(ctx context.Context, tx pgx.Tx, actor collaboration.Actor) error {
					if _, err := tx.Exec(ctx, `UPDATE openfox.organization_members SET role='OWNER' WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, s3OtherID); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `UPDATE openfox.organization_members SET role='MEMBER' WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, s3OwnerID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				e.scalar(&after, `SELECT authorization_version FROM openfox.organization_members WHERE organization_id=$1::uuid AND user_id=$2::uuid`, e.org, s3OwnerID)
				if after <= before {
					t.Fatal("role change did not revoke authorization version")
				}
			}
			releasing.Do(func() { close(release) })
			response := <-done
			if response.Status == 200 {
				t.Fatalf("late result leaked %s", response.Body)
			}
		})
	}
}

type asanaHoldTransport struct {
	next http.RoundTripper
	hold func()
}

func (t asanaHoldTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(r)
	if r.URL.Path == "/api/1.0/workspaces/101/projects" {
		t.hold()
	}
	return response, err
}

func TestAsanaMaintenanceIndependentGrants(t *testing.T) {
	for _, mode := range []string{"busy", "old_app"} {
		t.Run(mode, func(t *testing.T) {
			e := newAsanaEnv(t)
			e.connect("101")
			e.fake.mu.Lock()
			e.fake.orgID = "300"
			e.fake.mu.Unlock()
			e.connect("102")
			var first, second string
			e.scalar(&first, `SELECT id::text FROM openfox.integration_asana_grants WHERE organization_id=$1::uuid ORDER BY id LIMIT 1`, e.org)
			e.scalar(&second, `SELECT id::text FROM openfox.integration_asana_grants WHERE organization_id=$1::uuid ORDER BY id DESC LIMIT 1`, e.org)
			e.exec(`UPDATE openfox.integration_asana_grants SET expires_at=clock_timestamp()-interval '1 second' WHERE organization_id=$1::uuid`, e.org)
			if mode == "busy" {
				e.exec(`UPDATE openfox.integration_asana_grants SET refresh_lease=id,refresh_lease_until=clock_timestamp()+interval '30 seconds' WHERE id=$1::uuid`, first)
			} else {
				e.exec(`UPDATE openfox.integration_asana_grants SET app_client_id='previous-app' WHERE id=$1::uuid`, first)
			}
			err := e.runtime.MaintainOrganization(e.ctx, e.org)
			if mode == "busy" && err != nil {
				t.Fatal(err)
			}
			if mode == "old_app" && err == nil {
				t.Fatal("missing bounded failure summary")
			}
			if mode == "old_app" {
				var connection string
				e.scalar(&connection, `SELECT id::text FROM openfox.integration_connections WHERE asana_grant_id=$1::uuid`, first)
				parent := e.parent(connection)
				e.owner().expectError(409, "ASANA_APP_CHANGED", "POST", "integrations/asana/oauth/start", integrations.AsanaStartInput{Purpose: "reauthorize", ConnectionID: connection, Version: parent.Version})
			}
			var renewed bool
			e.scalar(&renewed, `SELECT credentials_version=3 AND expires_at>clock_timestamp()+interval '50 minutes' FROM openfox.integration_asana_grants WHERE id=$1::uuid`, second)
			e.fake.mu.Lock()
			refreshes := e.fake.refreshes
			e.fake.mu.Unlock()
			if !renewed || refreshes != 1 {
				t.Fatalf("second grant not refreshed: renewed=%v calls=%d", renewed, refreshes)
			}
		})
	}
}
