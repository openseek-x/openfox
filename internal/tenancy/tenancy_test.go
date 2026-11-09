package tenancy_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"openfox/internal/tenancy"
)

const (
	organizationA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	organizationB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	actorA        = "user-a"
	actorB        = "device-b"
)

var driverSequence uint64

func TestRunnerReusesConnectionOnlyAfterCommit(t *testing.T) {
	db, recorder := openRecordingDB(t)
	runner := newRunner(t, db, tenancy.OrganizationSelectorFunc(func(_ context.Context, organizationID string) (tenancy.OrganizationSelection, error) {
		actorID := actorA
		if strings.EqualFold(organizationID, organizationB) {
			actorID = actorB
		}
		return tenancy.OrganizationSelection{
			OrganizationID:       organizationID,
			ActorID:              actorID,
			AuthorizationVersion: 1,
		}, nil
	}))

	var callbackContext context.Context
	if err := runner.RunForOrganization(context.Background(), organizationA, tenancy.SourceUserRequest, func(ctx context.Context, _ *sql.Tx) error {
		callbackContext = ctx
		tenant, ok := tenancy.FromContext(ctx)
		if !ok {
			t.Fatal("callback context did not contain a trusted tenant")
		}
		if tenant.OrganizationID() != organizationA || tenant.ActorID() != actorA {
			t.Fatalf("tenant = %#v", tenant)
		}
		if err := tenancy.RequireOrganization(ctx, organizationA); err != nil {
			t.Fatalf("RequireOrganization(same) error = %v", err)
		}
		if err := tenancy.RequireOrganization(ctx, organizationB); !errors.Is(err, tenancy.ErrCrossOrganizationContext) {
			t.Fatalf("RequireOrganization(other) error = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("RunForOrganization(A) error = %v", err)
	}
	if _, ok := tenancy.FromContext(context.Background()); ok {
		t.Fatal("background context unexpectedly acquired tenant state")
	}
	if callbackContext == nil {
		t.Fatal("callback context was not captured")
	}

	if err := runner.RunForOrganization(context.Background(), organizationB, tenancy.SourceDeviceRequest, func(ctx context.Context, _ *sql.Tx) error {
		tenant, ok := tenancy.FromContext(ctx)
		if !ok {
			t.Fatal("second callback context did not contain a trusted tenant")
		}
		if tenant.OrganizationID() != organizationB || tenant.ActorID() != actorB || tenant.Source() != tenancy.SourceDeviceRequest {
			t.Fatalf("second tenant = %#v", tenant)
		}
		return nil
	}); err != nil {
		t.Fatalf("RunForOrganization(B) error = %v", err)
	}

	events := recorder.Events()
	if got, want := strings.Join(events, ","), "begin:1,exec:1,exec:1,commit:1,begin:1,exec:1,exec:1,commit:1"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
	execs := recorder.ExecRecords()
	if len(execs) != 4 {
		t.Fatalf("exec records = %#v", execs)
	}
	if got := execs[0].Args[0].Value; got != organizationA {
		t.Fatalf("first organization argument = %#v", got)
	}
	if got := execs[2].Args[0].Value; got != organizationB {
		t.Fatalf("second organization argument = %#v", got)
	}
	if execs[1].Query != "SET LOCAL ROLE openfox_app" || execs[3].Query != "SET LOCAL ROLE openfox_app" {
		t.Fatalf("role queries = %q and %q", execs[1].Query, execs[3].Query)
	}
	if execs[0].ConnectionID != execs[2].ConnectionID {
		t.Fatalf("connection IDs = %d and %d; expected pool reuse after transaction completion", execs[0].ConnectionID, execs[2].ConnectionID)
	}
}

func TestRunnerWithoutContextFailsClosedBeforeBegin(t *testing.T) {
	db, recorder := openRecordingDB(t)
	setter := newSQLSetter(t)
	runner := tenancy.Runner{DB: db, Setter: setter}

	err := runner.Run(context.Background(), func(context.Context, *sql.Tx) error {
		t.Fatal("transaction callback unexpectedly ran")
		return nil
	})
	if !errors.Is(err, tenancy.ErrMissingTenantContext) {
		t.Fatalf("Run() error = %v", err)
	}
	if events := recorder.Events(); len(events) != 0 {
		t.Fatalf("database events = %v, want no connection checkout", events)
	}
}

func TestRunnerRollsBackBeforeConnectionReuse(t *testing.T) {
	db, recorder := openRecordingDB(t)
	runner := newRunner(t, db, staticSelector(actorA))
	callbackErr := errors.New("callback failed")

	err := runner.RunForOrganization(context.Background(), organizationA, tenancy.SourceUserRequest, func(context.Context, *sql.Tx) error {
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("failed transaction error = %v", err)
	}

	if err := runner.RunForOrganization(context.Background(), organizationA, tenancy.SourceUserRequest, func(context.Context, *sql.Tx) error {
		return nil
	}); err != nil {
		t.Fatalf("second transaction error = %v", err)
	}

	events := recorder.Events()
	if got, want := strings.Join(events, ","), "begin:1,exec:1,exec:1,rollback:1,begin:1,exec:1,exec:1,commit:1"; got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestRunnerCancellationRollsBackAndSkipsCommit(t *testing.T) {
	db, recorder := openRecordingDB(t)
	runner := newRunner(t, db, staticSelector(actorA))
	ctx, cancel := context.WithCancel(context.Background())
	callbackCalled := false

	err := runner.RunForOrganization(ctx, organizationA, tenancy.SourceUserRequest, func(context.Context, *sql.Tx) error {
		callbackCalled = true
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled transaction error = %v", err)
	}
	if !callbackCalled {
		t.Fatal("callback did not run before cancellation")
	}
	for _, event := range recorder.Events() {
		if event == "commit:1" {
			t.Fatal("canceled transaction committed")
		}
	}
	if !containsEvent(recorder.Events(), "rollback:1") {
		t.Fatalf("events = %v, want rollback", recorder.Events())
	}
}

func TestCallerCannotInjectTenantWithOwnContextValue(t *testing.T) {
	db, recorder := openRecordingDB(t)
	runner := tenancy.Runner{DB: db, Setter: newSQLSetter(t)}
	forged := context.WithValue(context.Background(), forgedContextKey{}, tenancy.OrganizationSelection{
		OrganizationID:       organizationA,
		ActorID:              actorA,
		AuthorizationVersion: 1,
	})

	err := runner.Run(forged, func(context.Context, *sql.Tx) error {
		t.Fatal("forged context unexpectedly reached transaction callback")
		return nil
	})
	if !errors.Is(err, tenancy.ErrMissingTenantContext) {
		t.Fatalf("Run(forged context) error = %v", err)
	}
	if _, ok := tenancy.FromContext(forged); ok {
		t.Fatal("FromContext accepted caller-owned context value")
	}
	if events := recorder.Events(); len(events) != 0 {
		t.Fatalf("database events = %v, want none", events)
	}
}

func TestRunForOrganizationRejectsSelectorCrossOrganizationResult(t *testing.T) {
	db, recorder := openRecordingDB(t)
	runner := newRunner(t, db, staticSelectorWithOrganization(organizationB, actorA))

	err := runner.RunForOrganization(context.Background(), organizationA, tenancy.SourceUserRequest, func(context.Context, *sql.Tx) error {
		t.Fatal("cross-organization selection reached transaction callback")
		return nil
	})
	if !errors.Is(err, tenancy.ErrCrossOrganizationContext) {
		t.Fatalf("cross-organization selection error = %v", err)
	}
	if events := recorder.Events(); len(events) != 0 {
		t.Fatalf("database events = %v, want none", events)
	}
}

func TestRunForOrganizationRejectsInvalidSelectionBeforeBegin(t *testing.T) {
	db, recorder := openRecordingDB(t)
	runner := newRunner(t, db, staticSelectorWithOrganization(organizationA, " actor-with-space "))

	err := runner.RunForOrganization(context.Background(), organizationA, tenancy.SourceUserRequest, func(context.Context, *sql.Tx) error {
		t.Fatal("invalid selection reached transaction callback")
		return nil
	})
	if !errors.Is(err, tenancy.ErrInvalidTenantContext) || !errors.Is(err, tenancy.ErrInvalidActorID) {
		t.Fatalf("invalid selection error = %v", err)
	}
	if events := recorder.Events(); len(events) != 0 {
		t.Fatalf("database events = %v, want none", events)
	}
}

func TestSQLContextSetterRejectsEmptyQuery(t *testing.T) {
	if _, err := tenancy.NewSQLContextSetter("  "); !errors.Is(err, tenancy.ErrInvalidSetterQuery) {
		t.Fatalf("NewSQLContextSetter() error = %v", err)
	}
}

func newRunner(t *testing.T, db *sql.DB, selector tenancy.OrganizationSelector) tenancy.Runner {
	t.Helper()
	return tenancy.Runner{DB: db, Selector: selector, Setter: newSQLSetter(t)}
}

func newSQLSetter(t *testing.T) tenancy.SQLContextSetter {
	t.Helper()
	setter, err := tenancy.NewSQLContextSetter("SELECT openfox_set_tenant_context($1, $2, $3, $4)")
	if err != nil {
		t.Fatal(err)
	}
	return setter
}

func staticSelector(actorID string) tenancy.OrganizationSelector {
	return staticSelectorWithOrganizationFunc(func(requested string) string { return requested }, actorID)
}

func staticSelectorWithOrganization(organizationID, actorID string) tenancy.OrganizationSelector {
	return staticSelectorWithOrganizationFunc(func(string) string { return organizationID }, actorID)
}

func staticSelectorWithOrganizationFunc(organization func(string) string, actorID string) tenancy.OrganizationSelector {
	return tenancy.OrganizationSelectorFunc(func(ctx context.Context, requested string) (tenancy.OrganizationSelection, error) {
		if err := ctx.Err(); err != nil {
			return tenancy.OrganizationSelection{}, err
		}
		return tenancy.OrganizationSelection{
			OrganizationID:       organization(requested),
			ActorID:              actorID,
			AuthorizationVersion: 1,
		}, nil
	})
}

type forgedContextKey struct{}

type execRecord struct {
	ConnectionID int
	Query        string
	Args         []driver.NamedValue
}

type recordingDriver struct {
	mu         sync.Mutex
	nextConnID int
	events     []string
	execs      []execRecord
}

func (d *recordingDriver) Open(string) (driver.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextConnID++
	return &recordingConn{driver: d, id: d.nextConnID}, nil
}

func (d *recordingDriver) Events() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.events...)
}

func (d *recordingDriver) ExecRecords() []execRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]execRecord(nil), d.execs...)
}

func (d *recordingDriver) record(event string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, event)
}

func (d *recordingDriver) recordExec(record execRecord) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.execs = append(d.execs, record)
	d.events = append(d.events, fmt.Sprintf("exec:%d", record.ConnectionID))
}

type recordingConn struct {
	driver *recordingDriver
	id     int
}

func (c *recordingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recording driver does not support prepared statements")
}

func (c *recordingConn) Close() error { return nil }

func (c *recordingConn) Begin() (driver.Tx, error) {
	return c.begin(context.Background())
}

func (c *recordingConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	return c.begin(ctx)
}

func (c *recordingConn) begin(ctx context.Context) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.driver.record(fmt.Sprintf("begin:%d", c.id))
	return &recordingTx{conn: c}, nil
}

func (c *recordingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.driver.recordExec(execRecord{
		ConnectionID: c.id,
		Query:        query,
		Args:         append([]driver.NamedValue(nil), args...),
	})
	return driver.RowsAffected(1), nil
}

type recordingTx struct {
	conn *recordingConn
}

func (tx *recordingTx) Commit() error {
	tx.conn.driver.record(fmt.Sprintf("commit:%d", tx.conn.id))
	return nil
}

func (tx *recordingTx) Rollback() error {
	tx.conn.driver.record(fmt.Sprintf("rollback:%d", tx.conn.id))
	return nil
}

func openRecordingDB(t *testing.T) (*sql.DB, *recordingDriver) {
	t.Helper()
	recorder := &recordingDriver{}
	driverName := fmt.Sprintf("openfox-tenancy-%d", atomic.AddUint64(&driverSequence, 1))
	sql.Register(driverName, recorder)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close() error = %v", err)
		}
	})
	return db, recorder
}

func containsEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}
