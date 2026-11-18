package stage3

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/collaboration"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

type spaceCreatorView struct {
	SpaceView
	Creator *collaboration.SpaceCreator `json:"creator"`
}

func TestSpaceCreatorLifecycle(t *testing.T) {
	env := newS3Env(t)
	admin, owner := env.admin(), env.owner()
	var previousName string
	env.scalar(&previousName, `SELECT full_name FROM openfox.users WHERE id = $1::uuid`, s3AdminID)
	t.Cleanup(func() {
		env.exec(`UPDATE openfox.users SET full_name = $2 WHERE id = $1::uuid`, s3AdminID, previousName)
	})
	admin.must(http.StatusOK, http.MethodPatch, "/v1/me", map[string]any{"full_name": "Original creator"}, nil)

	var created, replay spaceCreatorView
	body := map[string]any{"name": "Creator " + s3Suffix(t), "visibility": "shared"}
	key := "creator-" + s3Suffix(t)
	admin.must(http.StatusCreated, http.MethodPost, "spaces", body, &created, "Idempotency-Key", key)
	admin.must(http.StatusOK, http.MethodPost, "spaces", body, &replay, "Idempotency-Key", key)
	if created.Creator == nil || created.Creator.ID != s3AdminID || created.Creator.Name == nil ||
		*created.Creator.Name != "Original creator" || created.Creator.Status != "active" ||
		replay.ID != created.ID || replay.Creator == nil || replay.Creator.ID != s3AdminID {
		t.Fatalf("creator / replay = %+v / %+v", created, replay)
	}
	var copied spaceCreatorView
	owner.must(http.StatusCreated, http.MethodPost, "spaces", map[string]any{
		"name": "Copy " + s3Suffix(t), "copy_from_space_id": created.ID,
	}, &copied)
	if copied.Creator == nil || copied.Creator.ID != s3OwnerID {
		t.Fatalf("copy should name the copying actor, not the source creator: %+v", copied.Creator)
	}
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "spaces",
		map[string]any{"name": "Spoof", "created_by": s3AdminID})
	env.member().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, "spaces", body)
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "spaces/"+env.otherSpace.ID, nil)

	admin.must(http.StatusOK, http.MethodPatch, "/v1/me", map[string]any{"full_name": "Renamed creator"}, nil)
	var listed struct {
		Spaces []spaceCreatorView `json:"spaces"`
	}
	owner.must(http.StatusOK, http.MethodGet, "spaces", nil, &listed)
	found := false
	for _, item := range listed.Spaces {
		if item.ID == created.ID {
			found = true
			if item.Creator == nil || item.Creator.Name == nil || *item.Creator.Name != "Renamed creator" {
				t.Fatalf("list has stale creator: %+v", item.Creator)
			}
		}
	}
	if !found {
		t.Fatal("created space not listed")
	}
	owner.must(http.StatusOK, http.MethodPatch, "members/"+s3AdminID, map[string]any{"status": "REVOKED"}, nil)
	owner.must(http.StatusOK, http.MethodGet, "spaces/"+created.ID, nil, &created)
	if created.Creator == nil || created.Creator.ID != s3AdminID || created.Creator.Name != nil || created.Creator.Status != "unavailable" {
		t.Fatalf("departed creator should have no profile: %+v", created.Creator)
	}
}

func TestSpaceCreatorLegacyAndTenantConstraint(t *testing.T) {
	env := newS3Env(t)
	legacyID := s3NewID(t)
	env.exec(`INSERT INTO openfox.spaces (organization_id, id, prefix, name, visibility)
		VALUES ($1::uuid, $2::uuid, $3, 'Legacy creator', 'SHARED')`, env.org, legacyID, s3Prefix(t))
	var legacy spaceCreatorView
	env.owner().must(http.StatusOK, http.MethodGet, "spaces/"+legacyID, nil, &legacy)
	if legacy.Creator != nil {
		t.Fatalf("legacy creator inferred: %+v", legacy.Creator)
	}
	_, err := env.superuser.Exec(env.ctx, `UPDATE openfox.spaces SET created_by = $1::uuid WHERE id = $2::uuid`, s3OutsiderID, legacyID)
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != "23503" {
		t.Fatalf("cross-organization creator should fail FK: %v", err)
	}

	// A member without other references can be hard-deleted; only created_by is
	// cleared, preserving the space and its non-null tenant identity.
	deletedID := s3NewID(t)
	env.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2)`, deletedID, deletedID+"@example.test")
	env.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER')`, env.org, deletedID)
	env.exec(`UPDATE openfox.spaces SET created_by = $1::uuid WHERE id = $2::uuid`, deletedID, legacyID)
	env.exec(`DELETE FROM openfox.organization_members WHERE organization_id = $1::uuid AND user_id = $2::uuid`, env.org, deletedID)
	env.exec(`DELETE FROM openfox.users WHERE id = $1::uuid`, deletedID)
	env.owner().must(http.StatusOK, http.MethodGet, "spaces/"+legacyID, nil, &legacy)
	if legacy.Creator != nil || legacy.ID != legacyID {
		t.Fatalf("deleted creator = %+v", legacy)
	}
}

type spaceQueryCounter struct {
	pgx.Tx
	queries int
}

func (tx *spaceQueryCounter) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.queries++
	return tx.Tx.Query(ctx, sql, args...)
}

func (tx *spaceQueryCounter) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.queries++
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func TestSpaceCreatorListUsesConstantQueries(t *testing.T) {
	env := newS3Env(t)
	list := func(want int) int {
		t.Helper()
		selection := tenancy.OrganizationSelection{OrganizationID: env.org, ActorID: s3OwnerID, AuthorizationVersion: 1}
		count := 0
		err := env.database.RunForOrganization(env.ctx, env.org, tenancy.SourceUserRequest,
			tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
			func(ctx context.Context, tx pgx.Tx) error {
				counter := &spaceQueryCounter{Tx: tx}
				spaces, err := (collaboration.Service{}).ListSpaceDetails(ctx, counter, env.org,
					collaboration.Actor{UserID: s3OwnerID, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}, "active")
				count = counter.queries
				if err == nil && len(spaces) != want {
					t.Errorf("space count = %d, want %d", len(spaces), want)
				}
				return err
			})
		if err != nil {
			t.Fatal(err)
		}
		return count
	}
	one := list(1)
	env.exec(`INSERT INTO openfox.spaces (organization_id, prefix, name, visibility, created_by)
		SELECT $1::uuid, 'Q' || lpad(n::text, 6, '0'), 'Creator query ' || n, 'SHARED', $2::uuid
		FROM generate_series(1, 25) n`, env.org, s3OwnerID)
	many := list(26)
	if one != 2 || many != one {
		t.Fatalf("queries for 1 / 26 spaces = %d / %d, want 2 / 2", one, many)
	}
}
