package integrations

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/bitbucket"
	"openfox/internal/membership"
)

const bbRepository = "44444444-4444-4444-8444-444444444444"

func bbBool(v bool) *bool { return &v }
func (b *bbPG) repoSpace(visibility, role string) string {
	b.t.Helper()
	id, e := membership.NewID()
	if e != nil {
		b.t.Fatal(e)
	}
	b.exec(`INSERT INTO openfox.spaces(organization_id,id,name,visibility,prefix) VALUES($1::uuid,$2::uuid,'Controlled repository space',$3,$4)`, b.org, id, visibility, "S"+strings.ToUpper(strings.ReplaceAll(id, "-", "")[:6]))
	if role != "" {
		b.exec(`INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES($1::uuid,$2::uuid,$3::uuid,$4)`, b.org, id, b.owner.UserID, role)
	}
	return id
}
func bbRepoInput(space string, connectionVersion uint64) BitbucketRepositoryInput {
	return BitbucketRepositoryInput{ConnectionVersion: connectionVersion, RepositoryID: bbRepository, SpaceIDs: []string{space}, PullRequests: bbBool(true), Commits: bbBool(false), BacklinkPrivate: bbBool(true), BacklinkPublic: bbBool(false), ConfirmMetadata: true, ConfirmSender: true}
}
func bbRepoProof() bitbucket.Repository {
	return bitbucket.Repository{UUID: bbRepository, FullName: "controlled/repo", SCM: "git", Private: bbBool(true), Workspace: bitbucket.Workspace{UUID: bbWorkspace, Slug: "controlled"}}
}
func (b *bbPG) repoTarget(connection string, in BitbucketRepositoryInput) BitbucketAccess {
	b.t.Helper()
	var a BitbucketAccess
	e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		a, e = b.s.PrepareBitbucketRepository(ctx, tx, b.org, b.owner, connection, in)
		return e
	})
	if e != nil {
		b.t.Fatal(e)
	}
	return a
}
func (b *bbPG) repoSave(a BitbucketAccess, in BitbucketRepositoryInput, proof bitbucket.Repository) (BitbucketRepositorySelection, error) {
	var out BitbucketRepositorySelection
	e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		out, e = b.s.SaveBitbucketRepository(ctx, tx, b.org, b.owner, a, in, proof)
		return e
	})
	return out, e
}

func TestBitbucketRepositoriesLifecycleAndCAS(t *testing.T) {
	b := newBBPG(t)
	c := b.connect()
	space := b.repoSpace("PRIVATE", "OWNER")
	in := bbRepoInput(space, c.Version)
	a := b.repoTarget(c.ID, in)
	saved, e := b.repoSave(a, in, bbRepoProof())
	if e != nil {
		t.Fatal(e)
	}
	if saved.Version != 1 || saved.HookReady || len(saved.SpaceIDs) != 1 || !saved.PullRequests || saved.Commits || !saved.BacklinkPrivate || saved.BacklinkPublic {
		t.Fatalf("wrong stored selection: %+v", saved)
	}
	if _, e = b.repoSave(a, in, bbRepoProof()); !errors.Is(e, ErrVersionConflict) {
		t.Fatalf("duplicate create: %v", e)
	}
	in.Version = saved.Version
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, e := b.repoSave(a, in, bbRepoProof()); errs <- e }()
	}
	close(start)
	wg.Wait()
	close(errs)
	ok, conflict := 0, 0
	for e := range errs {
		if e == nil {
			ok++
		} else if errors.Is(e, ErrVersionConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("CAS winners %d/%d", ok, conflict)
	}
	var list []BitbucketRepositorySelection
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		list, e = b.s.ListBitbucketSelections(ctx, tx, b.org, b.owner, c.ID)
		return e
	})
	if e != nil || len(list) != 1 || list[0].Version != 2 {
		t.Fatalf("persisted list %+v: %v", list, e)
	}
	var removed BitbucketRepositorySelection
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		removed, e = b.s.RemoveBitbucketRepository(ctx, tx, b.org, b.owner, c.ID, saved.ID, 2)
		return e
	})
	if e != nil || removed.Status != "REMOVED" || removed.Version != 3 {
		t.Fatalf("remove %+v: %v", removed, e)
	}
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.RemoveBitbucketRepository(ctx, tx, b.org, b.owner, c.ID, saved.ID, 2)
		return e
	})
	if e != nil {
		t.Fatalf("idempotent remove %v", e)
	}
	in.Version = 0
	saved, e = b.repoSave(a, in, bbRepoProof())
	if e != nil || saved.ID != removed.ID || saved.Version != 4 || saved.HookReady {
		t.Fatalf("explicit reselect %+v: %v", saved, e)
	}
}

func TestBitbucketRepositoriesSpaceBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, visibility, role string
		allowed                bool
	}{
		{"private-owner", "PRIVATE", "OWNER", true}, {"private-editor", "PRIVATE", "EDITOR", true}, {"private-viewer", "PRIVATE", "VIEWER", false}, {"private-no-member", "PRIVATE", "", false}, {"shared-org-member", "SHARED", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBBPG(t)
			c := b.connect()
			space := b.repoSpace(tc.visibility, tc.role)
			in := bbRepoInput(space, c.Version)
			e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
				_, e := b.s.PrepareBitbucketRepository(ctx, tx, b.org, b.owner, c.ID, in)
				return e
			})
			if (e == nil) != tc.allowed {
				t.Fatalf("allowed=%v error=%v", tc.allowed, e)
			}
		})
	}
	b := newBBPG(t)
	c := b.connect()
	space := b.repoSpace("PRIVATE", "OWNER")
	in := bbRepoInput(space, c.Version)
	a := b.repoTarget(c.ID, in)
	b.exec(`UPDATE openfox.space_members SET status='REVOKED' WHERE organization_id=$1::uuid AND space_id=$2::uuid AND user_id=$3::uuid`, b.org, space, b.owner.UserID)
	if _, e := b.repoSave(a, in, bbRepoProof()); e == nil {
		t.Fatal("late space revocation allowed save")
	}
	b.exec(`UPDATE openfox.space_members SET status='ACTIVE' WHERE organization_id=$1::uuid AND space_id=$2::uuid AND user_id=$3::uuid`, b.org, space, b.owner.UserID)
	b.exec(`UPDATE openfox.spaces SET status='ARCHIVED' WHERE organization_id=$1::uuid AND id=$2::uuid`, b.org, space)
	if _, e := b.repoSave(a, in, bbRepoProof()); !errors.Is(e, collaboration.ErrSpaceRetired) {
		t.Fatalf("retired %v", e)
	}
	e := b.tx(b.member, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.ListBitbucketSelections(ctx, tx, b.org, b.member, c.ID)
		return e
	})
	if !errors.Is(e, ErrForbidden) {
		t.Fatalf("member listing %v", e)
	}
	other, e := membership.NewID()
	if e != nil {
		t.Fatal(e)
	}
	b.exec(`INSERT INTO openfox.spaces(organization_id,id,name,prefix) VALUES($1::uuid,$2::uuid,'Other tenant','OTHER')`, b.other, other)
	in.SpaceIDs = []string{other}
	if _, e = b.repoSave(a, in, bbRepoProof()); !errors.Is(e, ErrNotFound) {
		t.Fatalf("cross tenant %v", e)
	}
}

func TestBitbucketRepositoriesProofAndExplicitConsent(t *testing.T) {
	b := newBBPG(t)
	c := b.connect()
	in := bbRepoInput(b.repoSpace("SHARED", ""), c.Version)
	a := b.repoTarget(c.ID, in)
	for _, mutate := range []func(*BitbucketRepositoryInput){func(i *BitbucketRepositoryInput) { i.ConfirmMetadata = false }, func(i *BitbucketRepositoryInput) { i.ConfirmSender = false }, func(i *BitbucketRepositoryInput) { i.BacklinkPublic = bbBool(true) }, func(i *BitbucketRepositoryInput) { i.Commits = nil }, func(i *BitbucketRepositoryInput) { i.SpaceIDs = append(i.SpaceIDs, i.SpaceIDs[0]) }} {
		copy := in
		copy.SpaceIDs = append([]string(nil), in.SpaceIDs...)
		mutate(&copy)
		if _, e := b.repoSave(a, copy, bbRepoProof()); !errors.Is(e, ErrInvalidRequest) {
			t.Fatalf("consent/shape %v", e)
		}
	}
	for _, mutate := range []func(*bitbucket.Repository){func(p *bitbucket.Repository) { p.UUID = bbSubject }, func(p *bitbucket.Repository) { p.Workspace.UUID = bbSubject }, func(p *bitbucket.Repository) { p.Private = nil }} {
		p := bbRepoProof()
		mutate(&p)
		if _, e := b.repoSave(a, in, p); !errors.Is(e, ErrInvalidRepository) {
			t.Fatalf("invalid REST proof %v", e)
		}
	}
	if n := b.count(`SELECT count(*) FROM openfox.integration_bitbucket_repositories WHERE organization_id=$1::uuid`, b.org); n != 0 {
		t.Fatalf("failed save wrote %d rows", n)
	}
}

func TestBitbucketWebhookRotationAndRecoverableSecret(t *testing.T) {
	b := newBBPG(t)
	c := b.connect()
	in := bbRepoInput(b.repoSpace("SHARED", ""), c.Version)
	a := b.repoTarget(c.ID, in)
	selection, e := b.repoSave(a, in, bbRepoProof())
	if e != nil {
		t.Fatal(e)
	}
	var before, first, again, second Webhook
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		before, e = b.s.GetBitbucketWebhook(ctx, tx, b.org, b.owner, c.ID)
		return e
	})
	if e != nil || before.PayloadURL != "" || before.Secret != "" {
		t.Fatalf("unconfigured hook %+v %v", before, e)
	}
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		first, e = b.s.RotateBitbucketWebhook(ctx, tx, b.org, b.owner, c.ID, c.Version)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if first.Version != c.Version+1 || len(first.Secret) != 64 || first.PayloadURL == "" || len(first.Events) != 8 {
		t.Fatal("invalid generated hook shape")
	}
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		again, e = b.s.GetBitbucketWebhook(ctx, tx, b.org, b.owner, c.ID)
		return e
	})
	if e != nil || again.PayloadURL != first.PayloadURL || again.Secret != first.Secret {
		t.Fatal("persisted hook not recoverable")
	}
	if _, e = b.repoSave(a, in, bbRepoProof()); !errors.Is(e, ErrVersionConflict) {
		t.Fatalf("rotation left old proof usable: %v", e)
	}
	b.exec(`UPDATE openfox.integration_bitbucket_repositories SET hook_generation=(SELECT hook_generation FROM openfox.integration_bitbucket_runtime WHERE organization_id=$1::uuid AND connection_id=$2::uuid) WHERE organization_id=$1::uuid AND id=$3::uuid`, b.org, c.ID, selection.ID)
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		second, e = b.s.RotateBitbucketWebhook(ctx, tx, b.org, b.owner, c.ID, first.Version)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if second.Secret == first.Secret || second.PayloadURL == first.PayloadURL {
		t.Fatal("rotation retained old secret or token")
	}
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		list, e := b.s.ListBitbucketSelections(ctx, tx, b.org, b.owner, c.ID)
		if e == nil && (len(list) != 1 || list[0].HookReady || list[0].Version != 3) {
			return errors.New("rotation did not fence selection")
		}
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	e = b.tx(b.member, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.GetBitbucketWebhook(ctx, tx, b.org, b.member, c.ID)
		return e
	})
	if !errors.Is(e, ErrForbidden) {
		t.Fatalf("secret visible to member: %v", e)
	}
	e = b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.RotateBitbucketWebhook(ctx, tx, b.org, b.owner, c.ID, first.Version)
		return e
	})
	if !errors.Is(e, ErrVersionConflict) {
		t.Fatalf("stale rotation: %v", e)
	}
}

func TestBitbucketRepositoriesSenderConfirmationIsVersionBound(t *testing.T) {
	b := newBBPG(t)
	c := b.connect()
	in := bbRepoInput(b.repoSpace("SHARED", ""), c.Version)
	oldTarget := b.repoTarget(c.ID, in)
	// Another manager can reauthorize the same workspace with a different
	// delegated sender while the first browser still displays its old checkbox.
	b.f.mu.Lock()
	b.f.subject = bbSubjectOther
	b.f.mu.Unlock()
	session := b.authorize(b.owner, BitbucketStartInput{Purpose: PurposeReauthorize, ConnectionID: c.ID, Version: c.Version})
	verified := b.target(b.owner, session)
	var current Connection
	if e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		current, _, e = b.s.CompleteBitbucketOrganization(ctx, tx, b.org, b.owner, session.SessionID, verified)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if current.BitbucketDelegatingUserID != bbSubjectOther {
		t.Fatal("reauthorization did not change sender")
	}
	if e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.PrepareBitbucketRepository(ctx, tx, b.org, b.owner, c.ID, in)
		return e
	}); !errors.Is(e, ErrVersionConflict) {
		t.Fatalf("stale browser confirmation accepted before REST: %v", e)
	}
	if _, e := b.repoSave(oldTarget, in, bbRepoProof()); !errors.Is(e, ErrVersionConflict) {
		t.Fatalf("stale already-prepared confirmation accepted: %v", e)
	}
	in.ConnectionVersion = current.Version
	fresh := b.repoTarget(c.ID, in)
	if _, e := b.repoSave(fresh, in, bbRepoProof()); e != nil {
		t.Fatalf("explicit current confirmation failed: %v", e)
	}
}

func TestBitbucketRepositoriesMissingRuntimeConfiguration(t *testing.T) {
	b := newBBPG(t)
	c := b.connect()
	b.s.Box = nil
	e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.PrepareBitbucketRepositories(ctx, tx, b.org, b.owner, c.ID)
		return e
	})
	if !errors.Is(e, ErrNotConfigured) {
		t.Fatalf("missing encryption configuration: %v", e)
	}
	// Metadata already in this tenant remains available to its managers, so
	// they can inspect and disconnect an existing unconfigured connection.
	if e := b.tx(b.owner, func(ctx context.Context, tx pgx.Tx) error {
		_, e := b.s.ListBitbucketSelections(ctx, tx, b.org, b.owner, c.ID)
		return e
	}); e != nil {
		t.Fatal(e)
	}
}
