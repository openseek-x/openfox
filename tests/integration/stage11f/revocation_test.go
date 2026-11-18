package stage11f

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/agents"
	"openfox/internal/devices"
	"openfox/internal/dispatch"
	"openfox/internal/membership"
	"openfox/internal/platform/postgres"
	"openfox/internal/policy"
	"openfox/internal/secrets"
	"openfox/internal/tenancy"
)

func TestVariantParentSpaceRevocation(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const viewer = "00000000-0000-0000-0000-000000000009"
	const childSpace = "00000000-0000-0000-0000-0000000000ad"
	setup, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO openfox.users(id,email) VALUES('00000000-0000-0000-0000-000000000009','variant-revoke@example.test')`,
		`INSERT INTO openfox.organization_members(organization_id,user_id,role) VALUES('00000000-0000-0000-0000-0000000000a1','00000000-0000-0000-0000-000000000009','MEMBER')`,
		`INSERT INTO openfox.spaces(organization_id,id,name,visibility) VALUES('00000000-0000-0000-0000-0000000000a1','00000000-0000-0000-0000-0000000000ad','11f revoke target','PRIVATE')`,
		`INSERT INTO openfox.space_members(organization_id,space_id,user_id,role) VALUES('00000000-0000-0000-0000-0000000000a1','00000000-0000-0000-0000-0000000000aa','00000000-0000-0000-0000-000000000009','OWNER'),('00000000-0000-0000-0000-0000000000a1','00000000-0000-0000-0000-0000000000ad','00000000-0000-0000-0000-000000000009','OWNER')`,
	} {
		if _, err := setup.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 6, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 10 * time.Second, LockTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	defer setup.Close(ctx)
	box, err := secrets.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	ownerSel := tenancy.OrganizationSelection{OrganizationID: org, ActorID: ownerID, AuthorizationVersion: 1}
	ownerActor := agents.Actor{UserID: ownerID, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	memberSel := tenancy.OrganizationSelection{OrganizationID: org, ActorID: viewer, AuthorizationVersion: 1}
	memberActor := agents.Actor{UserID: viewer, Role: policy.OrganizationRoleMember, AuthorizationVersion: 1}
	list, err := (devices.PostgresStore{Pool: pool}).List(ctx, ownerSel)
	if err != nil {
		t.Fatal(err)
	}
	var grant string
	for _, d := range list {
		if d.ID == deviceID {
			grant = d.GrantID
		}
	}
	if grant == "" {
		t.Fatal("fixture grant missing")
	}
	store := agents.PostgresStore{Pool: pool}
	parent, err := store.Create(ctx, ownerSel, ownerActor, space, agents.CreateInput{Name: "revoke parent", Kind: agents.KindCode, Visibility: agents.VisibilityShared, Config: agents.ConfigInput{ComputerID: deviceID, GrantID: grant, Runtime: "codex", Model: "gpt-5", Instructions: "parent-instruction"}})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := store.CreateVariant(ctx, memberSel, memberActor, space, parent.ID, agents.VariantInput{
		SpaceID:   childSpace,
		Overrides: agents.VariantOverrides{Runtime: &agents.VariantRuntime{Runtime: "codex", Model: "gpt-5"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Get(ctx, memberSel, memberActor, childSpace, child.ID)
	if err != nil || before.Parent == nil || before.ParentUnavailable {
		t.Fatalf("before revoke=%+v err=%v", before, err)
	}
	conversationID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	runID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = setup.Exec(ctx, `INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id)
		VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid)`, org, conversationID, childSpace, child.ID, viewer); err != nil {
		t.Fatal(err)
	}
	if _, err = setup.Exec(ctx, `INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state)
		SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8,g.permission_version,$9::uuid,'QUEUED'
		FROM openfox.device_grants g WHERE g.organization_id=$1::uuid AND g.id=$7::uuid`, org, runID, childSpace, conversationID, child.ID, deviceID, grant, child.CurrentConfigVersion, viewer); err != nil {
		t.Fatal(err)
	}
	// 仅撤销父空间成员权限，不触碰分身所属空间；Run 和用户数据继续保留。
	if _, err = setup.Exec(ctx, `DELETE FROM openfox.space_members WHERE organization_id=$1::uuid AND space_id=$2::uuid AND user_id=$3::uuid`, org, space, viewer); err != nil {
		t.Fatal(err)
	}
	after, err := store.Get(ctx, memberSel, memberActor, childSpace, child.ID)
	if err != nil || after.Name != child.Name || !after.ParentUnavailable || after.Parent != nil || after.Config == nil || after.Config.Instructions != "" {
		t.Fatalf("after revoke=%+v err=%v", after, err)
	}
	// 失去父空间权限不影响本人编辑分身基础资料。
	newName := "private child still editable"
	updated, err := store.UpdateProfile(ctx, memberSel, memberActor, childSpace, child.ID, agents.ProfileInput{Name: &newName, ProfileVersion: after.ProfileVersion})
	if err != nil || updated.Name != newName || !updated.ParentUnavailable {
		t.Fatalf("profile after revoke=%+v err=%v", updated, err)
	}
	if err = pool.RunForOrganization(ctx, org, tenancy.SourceUserRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return memberSel, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			_, e := dispatch.LoadRunEnvironment(ctx, tx, box, org, runID, child.ID, deviceID)
			if !errors.Is(e, dispatch.ErrAuthorizationRevoked) {
				return errors.New("queued run remained authorized after parent space revoke")
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := setup.QueryRow(ctx, `SELECT state FROM openfox.runs WHERE organization_id=$1::uuid AND id=$2::uuid`, org, runID).Scan(&state); err != nil || state != "QUEUED" {
		t.Fatalf("run record lost: %s %v", state, err)
	}
	// 恢复父空间权限后领取同一 Run，再撤权，确认设备已领取路径也实时拒绝上下文。
	if _, err := setup.Exec(ctx, `INSERT INTO openfox.space_members(organization_id,space_id,user_id,role)
		VALUES($1::uuid,$2::uuid,$3::uuid,'OWNER')`, org, space, viewer); err != nil {
		t.Fatal(err)
	}
	reservationID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	offerID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	worker := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return ownerSel, nil })
	if err := pool.RunForOrganization(ctx, org, tenancy.SourceOrganizationWorker, worker, func(ctx context.Context, tx pgx.Tx) error {
		var created string
		if e := tx.QueryRow(ctx, `SELECT openfox_private.dispatch_reserve_capacity($1::uuid,$2::uuid,$3::uuid,1,$4::uuid,$5::uuid,$6::uuid,clock_timestamp()+interval '5 minutes')::text`, org, reservationID, runID, deviceID, child.ID, offerID).Scan(&created); e != nil {
			return e
		}
		if created != offerID {
			return errors.New("offer ID mismatch")
		}
		_, e := tx.Exec(ctx, `UPDATE openfox.runs SET reservation_id=$1::uuid,offer_id=$2::uuid WHERE organization_id=$3::uuid AND id=$4::uuid`, reservationID, offerID, org, runID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	deviceSel := tenancy.OrganizationSelection{OrganizationID: org, ActorID: deviceID, AuthorizationVersion: 1}
	deviceSelector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return deviceSel, nil })
	if err := pool.RunForOrganization(ctx, org, tenancy.SourceDeviceRequest, deviceSelector, func(ctx context.Context, tx pgx.Tx) error {
		_, e := (dispatch.Store{}).Claim(ctx, tx, org, deviceID, offerID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, `DELETE FROM openfox.space_members WHERE organization_id=$1::uuid AND space_id=$2::uuid AND user_id=$3::uuid`, org, space, viewer); err != nil {
		t.Fatal(err)
	}
	if err := pool.RunForOrganization(ctx, org, tenancy.SourceDeviceRequest, deviceSelector, func(ctx context.Context, tx pgx.Tx) error {
		_, e := (dispatch.Store{}).ContextForDevice(ctx, tx, org, deviceID, runID, 1, box, true)
		if !errors.Is(e, dispatch.ErrAuthorizationRevoked) {
			return errors.New("claimed run remained authorized after parent space revoke")
		}
		_, e = (dispatch.Store{}).PrepareFailed(ctx, tx, org, deviceID, runID, 1, "CANCELED", "authorization revoked before start")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	var reservationState string
	if err := setup.QueryRow(ctx, `SELECT status FROM openfox.capacity_reservations WHERE organization_id=$1::uuid AND id=$2::uuid`, org, reservationID).Scan(&reservationState); err != nil || reservationState != "RELEASED" {
		t.Fatalf("revoked reservation=%s err=%v", reservationState, err)
	}
	// 已覆盖的 Runtime 组仍可更新；空的继承指令不会把父指令写回响应。
	configured, err := store.CreateConfig(ctx, memberSel, memberActor, childSpace, child.ID, updated.Version, agents.ConfigInput{Runtime: "codex", Model: "gpt-5-mini"})
	if err != nil || configured.Config == nil || configured.Config.Model != "gpt-5-mini" || configured.Config.Instructions != "" || !configured.ParentUnavailable {
		t.Fatalf("config after revoke=%+v err=%v", configured, err)
	}
	// 父空间重新授权后，父级转 PRIVATE 仍须隐藏继承内容并保留用户资料。
	if _, err := setup.Exec(ctx, `INSERT INTO openfox.space_members(organization_id,space_id,user_id,role)
		VALUES($1::uuid,$2::uuid,$3::uuid,'OWNER')`, org, space, viewer); err != nil {
		t.Fatal(err)
	}
	private := agents.VisibilityPrivate
	if _, err := store.UpdateProfile(ctx, ownerSel, ownerActor, space, parent.ID, agents.ProfileInput{Visibility: &private, ProfileVersion: parent.ProfileVersion}); err != nil {
		t.Fatal(err)
	}
	hidden, err := store.Get(ctx, memberSel, memberActor, childSpace, child.ID)
	if err != nil || hidden.Name != newName || !hidden.ParentUnavailable || hidden.Config == nil || hidden.Config.Instructions != "" {
		t.Fatalf("private parent child=%+v err=%v", hidden, err)
	}
}
