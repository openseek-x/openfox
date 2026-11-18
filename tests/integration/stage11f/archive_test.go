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
	"openfox/internal/tenancy"
)

func TestVariantParentArchiveCancelsActiveRuns(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 6, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 10 * time.Second, LockTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sel := tenancy.OrganizationSelection{OrganizationID: org, ActorID: ownerID, AuthorizationVersion: 1}
	actor := agents.Actor{UserID: ownerID, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	devicesList, err := (devices.PostgresStore{Pool: pool}).List(ctx, sel)
	if err != nil {
		t.Fatal(err)
	}
	var grant string
	for _, d := range devicesList {
		if d.ID == deviceID {
			grant = d.GrantID
		}
	}
	if grant == "" {
		t.Fatal("fixture grant missing")
	}
	store := agents.PostgresStore{Pool: pool}
	parent, err := store.Create(ctx, sel, actor, space, agents.CreateInput{Name: "11f archive parent", Kind: agents.KindCode, Visibility: agents.VisibilityShared, Config: agents.ConfigInput{ComputerID: deviceID, GrantID: grant, Runtime: "codex", Model: "gpt-5"}})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := store.CreateVariant(ctx, sel, actor, space, parent.ID, agents.VariantInput{SpaceID: space}, "")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	runIDs := map[string]string{}
	for _, item := range []agents.Agent{parent, child} {
		conversationID, e := membership.NewID()
		if e != nil {
			t.Fatal(e)
		}
		runID, e := membership.NewID()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = connection.Exec(ctx, `INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id)
			VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid)`, org, conversationID, space, item.ID, ownerID); e != nil {
			t.Fatal(e)
		}
		if _, e = connection.Exec(ctx, `INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state)
			SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8,g.permission_version,$9::uuid,'PREPARING'
			FROM openfox.device_grants g WHERE g.organization_id=$1::uuid AND g.id=$7::uuid`, org, runID, space, conversationID, item.ID, deviceID, grant, item.CurrentConfigVersion, ownerID); e != nil {
			t.Fatal(e)
		}
		runIDs[item.ID] = runID
	}
	// 有效 QUEUED 预留走 finish_queued_run 的容量→设备→Agent 锁序。
	queuedConversation, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	queuedRun, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	offerID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, `INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id)
		VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid)`, org, queuedConversation, space, child.ID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, `INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state)
		SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8,g.permission_version,$9::uuid,'QUEUED'
		FROM openfox.device_grants g WHERE g.organization_id=$1::uuid AND g.id=$7::uuid`, org, queuedRun, space, queuedConversation, child.ID, deviceID, grant, child.CurrentConfigVersion, ownerID); err != nil {
		t.Fatal(err)
	}
	setupTx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer setupTx.Rollback(ctx)
	if _, err = setupTx.Exec(ctx, `SELECT openfox_private.set_tenant_context($1::uuid,$2::uuid,1,'user_request')`, org, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err = setupTx.Exec(ctx, `INSERT INTO openfox.capacity_reservations(organization_id,id,run_id,generation,device_id,agent_id)
		VALUES($1::uuid,$2::uuid,$3::uuid,1,$4::uuid,$5::uuid)`, org, reservation, queuedRun, deviceID, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = setupTx.Exec(ctx, `INSERT INTO openfox.dispatch_offers(organization_id,id,reservation_id,run_id,generation,target_device_id,expires_at)
		VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,1,$5::uuid,clock_timestamp()+interval '5 minutes')`, org, offerID, reservation, queuedRun, deviceID); err != nil {
		t.Fatal(err)
	}
	updates := []struct {
		query string
		args  []any
	}{
		{`UPDATE openfox.organization_capacity SET reserved_slots=reserved_slots+1 WHERE organization_id=$1::uuid`, []any{org}},
		{`UPDATE openfox.physical_devices SET reserved_slots=reserved_slots+1 WHERE id=$1::uuid`, []any{deviceID}},
		{`UPDATE openfox.agents SET reserved_slots=reserved_slots+1 WHERE organization_id=$1::uuid AND id=$2::uuid`, []any{org, child.ID}},
		{`UPDATE openfox.runs SET reservation_id=$1::uuid,offer_id=$2::uuid WHERE organization_id=$3::uuid AND id=$4::uuid`, []any{reservation, offerID, org, queuedRun}},
	}
	for _, update := range updates {
		if _, err = setupTx.Exec(ctx, update.query, update.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err = setupTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// claim 真实持有容量、设备、Agent、Run 锁但不提交；父归档须等待领取事务回滚后再取消预留。
	ready := make(chan error, 1)
	releaseClaim := make(chan struct{})
	claimed := make(chan error, 1)
	go func() {
		deviceSel := tenancy.OrganizationSelection{OrganizationID: org, ActorID: deviceID, AuthorizationVersion: 1}
		e := pool.RunForOrganization(ctx, org, tenancy.SourceDeviceRequest,
			tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return deviceSel, nil }),
			func(ctx context.Context, tx pgx.Tx) error {
				_, claimErr := (dispatch.Store{}).Claim(ctx, tx, org, deviceID, offerID)
				ready <- claimErr
				if claimErr != nil {
					return claimErr
				}
				select {
				case <-releaseClaim:
					return errors.New("rollback concurrent claim fixture")
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		claimed <- e
	}()
	if claimErr := <-ready; claimErr != nil {
		t.Fatalf("concurrent claim fixture: %v", claimErr)
	}
	type archiveResult struct {
		item   agents.Agent
		cancel dispatch.AgentRunCancellation
		err    error
	}
	archiving := make(chan archiveResult, 1)
	go func() {
		item, result, e := store.UpdateWithCancellation(ctx, sel, actor, space, parent.ID, agents.UpdateInput{Archived: boolptr(true), Version: parent.Version})
		archiving <- archiveResult{item, result, e}
	}()
	select {
	case result := <-archiving:
		t.Fatalf("archive did not wait for claim capacity lock: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseClaim)
	if claimErr := <-claimed; claimErr == nil || claimErr.Error() != "rollback concurrent claim fixture" {
		t.Fatalf("concurrent claim rollback: %v", claimErr)
	}
	outcome := <-archiving
	archived, canceled, err := outcome.item, outcome.cancel, outcome.err
	if err != nil || archived.ArchivedAt == nil || canceled.Canceled != 3 || len(canceled.Notices) != 2 {
		t.Fatalf("archive=%+v cancel=%+v err=%v", archived, canceled, err)
	}
	for agentID, runID := range runIDs {
		var state string
		if err := connection.QueryRow(ctx, `SELECT state FROM openfox.runs WHERE organization_id=$1::uuid AND id=$2::uuid`, org, runID).Scan(&state); err != nil || state != "CANCEL_REQUESTED" {
			t.Fatalf("agent=%s run state=%s err=%v", agentID, state, err)
		}
	}
	var queueState, reservationState string
	var orgSlots, deviceSlots, agentSlots int
	if err := connection.QueryRow(ctx, `SELECT state FROM openfox.runs WHERE organization_id=$1::uuid AND id=$2::uuid`, org, queuedRun).Scan(&queueState); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `SELECT status FROM openfox.capacity_reservations WHERE organization_id=$1::uuid AND id=$2::uuid`, org, reservation).Scan(&reservationState); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `SELECT reserved_slots FROM openfox.organization_capacity WHERE organization_id=$1::uuid`, org).Scan(&orgSlots); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `SELECT reserved_slots FROM openfox.physical_devices WHERE id=$1::uuid`, deviceID).Scan(&deviceSlots); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `SELECT reserved_slots FROM openfox.agents WHERE organization_id=$1::uuid AND id=$2::uuid`, org, child.ID).Scan(&agentSlots); err != nil {
		t.Fatal(err)
	}
	if queueState != "CANCELED" || reservationState != "RELEASED" || orgSlots != 0 || deviceSlots != 0 || agentSlots != 0 {
		t.Fatalf("queued=%s reservation=%s slots=%d/%d/%d", queueState, reservationState, orgSlots, deviceSlots, agentSlots)
	}
	updated, err := store.Get(ctx, sel, actor, space, child.ID)
	if err != nil || updated.ArchivedAt == nil {
		t.Fatalf("child archive=%+v err=%v", updated, err)
	}
}

func TestVariantArchiveAfterCommittedClaim(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 6, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 10 * time.Second, LockTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	sel := tenancy.OrganizationSelection{OrganizationID: org, ActorID: ownerID, AuthorizationVersion: 1}
	actor := agents.Actor{UserID: ownerID, Role: policy.OrganizationRoleOwner, AuthorizationVersion: 1}
	list, err := (devices.PostgresStore{Pool: pool}).List(ctx, sel)
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
	parent, err := store.Create(ctx, sel, actor, space, agents.CreateInput{Name: "11f claimed parent", Kind: agents.KindCode, Visibility: agents.VisibilityShared, Config: agents.ConfigInput{ComputerID: deviceID, GrantID: grant, Runtime: "codex", Model: "gpt-5"}})
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := store.CreateVariant(ctx, sel, actor, space, parent.ID, agents.VariantInput{SpaceID: space}, "")
	if err != nil {
		t.Fatal(err)
	}
	conversationID, err := membership.NewID()
	if err != nil {
		t.Fatal(err)
	}
	runID, err := membership.NewID()
	if err != nil {
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
	connection, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	if _, err = connection.Exec(ctx, `INSERT INTO openfox.conversations(organization_id,id,space_id,agent_id,owner_user_id)
		VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid)`, org, conversationID, space, child.ID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err = connection.Exec(ctx, `INSERT INTO openfox.runs(organization_id,id,space_id,conversation_id,agent_id,computer_id,grant_id,config_version,grant_version,actor_id,state)
		SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8,g.permission_version,$9::uuid,'QUEUED'
		FROM openfox.device_grants g WHERE g.organization_id=$1::uuid AND g.id=$7::uuid`, org, runID, space, conversationID, child.ID, deviceID, grant, child.CurrentConfigVersion, ownerID); err != nil {
		t.Fatal(err)
	}
	worker := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return sel, nil })
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
	if err := pool.RunForOrganization(ctx, org, tenancy.SourceDeviceRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return deviceSel, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			lease, e := (dispatch.Store{}).Claim(ctx, tx, org, deviceID, offerID)
			if e != nil {
				return e
			}
			if lease.RunID != runID {
				return errors.New("claimed a different Run")
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	archived, canceled, err := store.UpdateWithCancellation(ctx, sel, actor, space, parent.ID, agents.UpdateInput{Archived: boolptr(true), Version: parent.Version})
	if err != nil || archived.ArchivedAt == nil || canceled.Canceled != 1 || len(canceled.Notices) != 1 || canceled.Notices[0].RunID != runID {
		t.Fatalf("archive=%+v cancel=%+v err=%v", archived, canceled, err)
	}
	var state, reservationState string
	if err := connection.QueryRow(ctx, `SELECT state FROM openfox.runs WHERE organization_id=$1::uuid AND id=$2::uuid`, org, runID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `SELECT status FROM openfox.capacity_reservations WHERE organization_id=$1::uuid AND id=$2::uuid`, org, reservationID).Scan(&reservationState); err != nil {
		t.Fatal(err)
	}
	if state != "CANCEL_REQUESTED" || reservationState != "CLAIMED" {
		t.Fatalf("claimed run=%s reservation=%s", state, reservationState)
	}
	// 设备确认尚未启动的取消：经正式终态路径释放已领取容量，避免污染共享测试夹具。
	if err := pool.RunForOrganization(ctx, org, tenancy.SourceDeviceRequest,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return deviceSel, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			finished, e := (dispatch.Store{}).PrepareFailed(ctx, tx, org, deviceID, runID, 1, "CANCELED", "archive confirmed before start")
			if e != nil {
				return e
			}
			if string(finished.State) != "CANCELED" {
				return errors.New("cancellation acknowledgement did not finish Run")
			}
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	var orgSlots, deviceSlots, agentSlots int
	if err := connection.QueryRow(ctx, `SELECT c.reserved_slots,d.reserved_slots,a.reserved_slots
		FROM openfox.organization_capacity c CROSS JOIN openfox.physical_devices d
		JOIN openfox.agents a ON a.device_id=d.id AND a.organization_id=c.organization_id
		WHERE c.organization_id=$1::uuid AND d.id=$2::uuid AND a.id=$3::uuid`, org, deviceID, child.ID).Scan(&orgSlots, &deviceSlots, &agentSlots); err != nil {
		t.Fatal(err)
	}
	if err := connection.QueryRow(ctx, `SELECT status FROM openfox.capacity_reservations WHERE organization_id=$1::uuid AND id=$2::uuid`, org, reservationID).Scan(&reservationState); err != nil {
		t.Fatal(err)
	}
	if reservationState != "RELEASED" || orgSlots != 0 || deviceSlots != 0 || agentSlots != 0 {
		t.Fatalf("acknowledged reservation=%s slots=%d/%d/%d", reservationState, orgSlots, deviceSlots, agentSlots)
	}
}
