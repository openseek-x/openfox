package stage11f

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/devices"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

const (
	org        = "00000000-0000-0000-0000-0000000000a1"
	ownerID    = "00000000-0000-0000-0000-000000000001"
	space      = "00000000-0000-0000-0000-0000000000aa"
	otherSpace = "00000000-0000-0000-0000-0000000000ac"
	deviceID   = "00000000-0000-0000-0000-000000000011"
)

func TestVariantPropagationAndConcurrentOverrides(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 8, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 10 * time.Second, LockTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// 扩充隔离 fixture：第二空间仍属同组织，跨组织空间不可在此处引用。
	setup, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, `INSERT INTO openfox.spaces(organization_id,id,name,visibility) VALUES ($1::uuid,$2::uuid,'11f second','PRIVATE')`, org, otherSpace); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, `INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES ($1::uuid,$2::uuid,$3::uuid,'OWNER')`, org, otherSpace, ownerID); err != nil {
		t.Fatal(err)
	}
	setup.Close(ctx)
	sel := tenancy.OrganizationSelection{OrganizationID: org, ActorID: ownerID, AuthorizationVersion: 1}
	actor := agents.Actor{UserID: ownerID, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	list, err := (devices.PostgresStore{Pool: pool}).List(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	grant, restrictedGrant := "", ""
	for _, d := range list {
		if d.ID == deviceID {
			grant = d.GrantID
		}
		if d.ID == "00000000-0000-0000-0000-000000000022" {
			restrictedGrant = d.GrantID
		}
	}
	if grant == "" {
		t.Fatal("fixture grant missing")
	}
	store := agents.PostgresStore{Pool: pool}
	initial := agents.ConfigInput{ComputerID: deviceID, GrantID: grant, Runtime: "codex", Model: "gpt-5", Instructions: "parent-one", DirectoryMode: agents.DirectoryWorkspace}
	parent, err := store.Create(ctx, sel, actor, space, agents.CreateInput{Name: "11f parent", Kind: agents.KindCode, Visibility: agents.VisibilityShared, Config: initial})
	if err != nil {
		t.Fatal(err)
	}
	key := "variant_created_11f"
	variant, replay, err := store.CreateVariant(ctx, sel, actor, space, parent.ID, agents.VariantInput{SpaceID: space}, key)
	if err != nil {
		t.Fatal(err)
	}
	if replay || variant.ParentAgentID != parent.ID || len(variant.InheritedGroups) != 8 {
		t.Fatalf("variant=%+v replay=%v", variant, replay)
	}
	again, replay, err := store.CreateVariant(ctx, sel, actor, space, parent.ID, agents.VariantInput{SpaceID: space}, key)
	if err != nil || !replay || again.ID != variant.ID {
		t.Fatalf("replay=%+v/%v %v", again, replay, err)
	}
	if _, _, err := store.CreateVariant(ctx, sel, actor, space, parent.ID, agents.VariantInput{SpaceID: space, Name: ptr("different")}, key); err == nil {
		t.Fatal("idempotency key with different request accepted")
	}
	if _, _, err := store.CreateVariant(ctx, sel, actor, space, variant.ID, agents.VariantInput{SpaceID: space}, ""); err == nil {
		t.Fatal("second-level variant accepted")
	}
	cross, _, err := store.CreateVariant(ctx, sel, actor, space, parent.ID, agents.VariantInput{SpaceID: otherSpace}, "")
	if err != nil || cross.SpaceID != otherSpace {
		t.Fatalf("cross-space create=%+v %v", cross, err)
	}
	if _, err := store.Get(ctx, sel, actor, otherSpace, cross.ID); err != nil {
		t.Fatalf("cross-space read: %v", err)
	}
	if err := pool.RunForOrganization(ctx, org, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return sel, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO openfox.skills(organization_id,space_id,name,source_kind,created_by,updated_by) VALUES($1::uuid,$2::uuid,'stage11f-skill','manual',$3::uuid,$3::uuid) RETURNING id::text`, org, space, ownerID).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO openfox.agent_skills(organization_id,agent_id,skill_id,space_id,created_by) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid)`, org, parent.ID, id, space, ownerID); err != nil {
				return err
			}
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.agent_skill_loadout WHERE organization_id=$1::uuid AND agent_id=$2::uuid AND space_id=$3::uuid AND id=$4::uuid`, org, cross.ID, otherSpace, id).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return errors.New("variant skill loadout did not follow parent")
			}
			return nil
		}); err != nil {
		t.Fatalf("cross-space skill loadout: %v", err)
	}
	parent, err = store.CreateConfig(ctx, sel, actor, space, parent.ID, parent.Version, config(initial, "parent-two"))
	if err != nil {
		t.Fatal(err)
	}
	variant, err = store.Get(ctx, sel, actor, space, variant.ID)
	if err != nil || variant.Config.Instructions != "parent-two" || variant.CurrentConfigVersion != 2 {
		t.Fatalf("propagation=%+v %v", variant, err)
	}
	own := config(initial, "custom-instruction")
	variant, err = store.CreateConfig(ctx, sel, actor, space, variant.ID, variant.Version, own)
	if err != nil {
		t.Fatal(err)
	}
	if groupHas(variant.InheritedGroups, agents.GroupInstructions) {
		t.Fatal("override remained inherited")
	}
	parent, err = store.CreateConfig(ctx, sel, actor, space, parent.ID, parent.Version, config(initial, "parent-three"))
	if err != nil {
		t.Fatal(err)
	}
	variant, err = store.Get(ctx, sel, actor, space, variant.ID)
	if err != nil || variant.Config.Instructions != "custom-instruction" {
		t.Fatalf("override lost=%+v %v", variant, err)
	}
	variant, err = store.ResetInheritance(ctx, sel, actor, space, variant.ID, []string{agents.GroupInstructions})
	if err != nil || variant.Config.Instructions != "parent-three" {
		t.Fatalf("reset=%+v %v", variant, err)
	}
	// 双写竞争：两个写入都必须遵守版本冲突，不能将父级旧指令覆写分身覆盖值。
	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, e := store.CreateConfig(ctx, sel, actor, space, parent.ID, parent.Version, config(initial, "parent-four"))
		results <- e
	}()
	go func() {
		defer wg.Done()
		_, e := store.CreateConfig(ctx, sel, actor, space, variant.ID, variant.Version, config(initial, "variant-four"))
		results <- e
	}()
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil && !errors.Is(e, agents.ErrVersionConflict) {
			t.Fatalf("concurrent config: %v", e)
		}
	}
	variant, err = store.Get(ctx, sel, actor, space, variant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if variant.Config.Instructions != "variant-four" && variant.Config.Instructions != "parent-four" {
		t.Fatalf("lost update=%+v", variant.Config)
	}
	for i := 0; i < 18; i++ {
		if _, _, err := store.CreateVariant(ctx, sel, actor, space, parent.ID, agents.VariantInput{SpaceID: space}, ""); err != nil {
			t.Fatalf("variant %d/20: %v", i+3, err)
		}
	}
	if _, _, err := store.CreateVariant(ctx, sel, actor, space, parent.ID, agents.VariantInput{SpaceID: space}, ""); err == nil {
		t.Fatal("variant cap exceeded 20")
	}
	p, err := store.Get(ctx, sel, actor, space, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := store.Update(ctx, sel, actor, space, parent.ID, agents.UpdateInput{Archived: boolptr(true), Version: p.Version})
	if err != nil || archived.ArchivedAt == nil {
		t.Fatalf("archive parent: %+v %v", archived, err)
	}
	variant, err = store.Get(ctx, sel, actor, space, variant.ID)
	if err != nil || variant.ArchivedAt == nil {
		t.Fatalf("archive variant: %+v %v", variant, err)
	}
	again, replay, err = store.CreateVariant(ctx, sel, actor, space, parent.ID, agents.VariantInput{SpaceID: space}, key)
	if err != nil || !replay || again.ID != variant.ID || again.ArchivedAt == nil {
		t.Fatalf("archive replay=%+v replay=%v err=%v", again, replay, err)
	}
	if restrictedGrant == "" {
		t.Fatal("second grant missing")
	}
	restrictedParent, err := store.Create(ctx, sel, actor, space, agents.CreateInput{Name: "11f restricted parent", Kind: agents.KindCode, Visibility: agents.VisibilityShared, Config: agents.ConfigInput{ComputerID: "00000000-0000-0000-0000-000000000022", GrantID: restrictedGrant, Runtime: "codex", Model: "gpt-5"}})
	if err != nil {
		t.Fatal(err)
	}
	// 同一操作者对另一父级复用同键、同请求体必须冲突，不能错误重放第一个分身。
	if _, replay, err := store.CreateVariant(ctx, sel, actor, space, restrictedParent.ID, agents.VariantInput{SpaceID: space}, key); err == nil || replay {
		t.Fatalf("cross-parent idempotency accepted: replay=%v err=%v", replay, err)
	}
	setup, err = pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = setup.Exec(ctx, `UPDATE openfox.device_grants SET scope='SPACE',scope_space_id=$2::uuid WHERE organization_id=$1::uuid AND id=$3::uuid`, org, space, restrictedGrant)
	setup.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateVariant(ctx, sel, actor, space, restrictedParent.ID, agents.VariantInput{SpaceID: otherSpace}, ""); err == nil {
		t.Fatal("cross-space unauthorized computer accepted")
	}
	override := agents.VariantOverrides{Computer: &agents.VariantComputer{ComputerID: deviceID, GrantID: grant}}
	created, _, err := store.CreateVariant(ctx, sel, actor, space, restrictedParent.ID, agents.VariantInput{SpaceID: otherSpace, Overrides: override}, "")
	if err != nil || created.Config == nil || created.Config.ComputerID != deviceID {
		t.Fatalf("computer override: %+v %v", created, err)
	}
}
func config(before agents.ConfigInput, instructions string) agents.ConfigInput {
	before.Instructions = instructions
	return before
}
func ptr(s string) *string { return &s }
func boolptr(v bool) *bool { return &v }
func groupHas(groups []string, group string) bool {
	for _, g := range groups {
		if g == group {
			return true
		}
	}
	return false
}
