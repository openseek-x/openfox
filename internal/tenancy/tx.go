package tenancy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrNilDatabase             = errors.New("tenancy: nil database")
	ErrNilContextSetter        = errors.New("tenancy: nil context setter")
	ErrNilOrganizationSelector = errors.New("tenancy: nil organization selector")
	ErrNilTransactionFunc      = errors.New("tenancy: nil transaction function")
	ErrInvalidSetterQuery      = errors.New("tenancy: context setter query is empty")
	ErrInvalidRoleQuery        = errors.New("tenancy: application role query is empty")
	ErrBeginTransaction        = errors.New("tenancy: begin transaction failed")
	ErrSetTenantContext        = errors.New("tenancy: set tenant context failed")
	ErrCommitTransaction       = errors.New("tenancy: commit transaction failed")
)

// ContextSetter writes tenant identity using transaction-local database
// semantics. Implementations must not use a session-level setting because the
// connection is returned to the pool after Commit or Rollback.
type ContextSetter interface {
	SetTenantContext(context.Context, *sql.Tx, TenantContext) error
}

// SQLContextSetter executes a configured trusted setter statement and switches
// to the data role inside the same transaction. The statements are supplied by
// server configuration/code; they are never taken from a request. The setter's
// arguments are organization ID, actor ID, authorization version, and source,
// in that order.
type SQLContextSetter struct {
	Query     string
	RoleQuery string
}

// NewSQLContextSetter validates a trusted database setter statement.
func NewSQLContextSetter(query string) (SQLContextSetter, error) {
	if strings.TrimSpace(query) == "" {
		return SQLContextSetter{}, ErrInvalidSetterQuery
	}
	return SQLContextSetter{Query: query, RoleQuery: "SET LOCAL ROLE openfox_app"}, nil
}

func (s SQLContextSetter) SetTenantContext(ctx context.Context, tx *sql.Tx, tenant TenantContext) error {
	if tx == nil {
		return ErrInvalidTenantContext
	}
	if err := tenant.valid(); err != nil {
		return err
	}
	if strings.TrimSpace(s.Query) == "" {
		return ErrInvalidSetterQuery
	}
	if strings.TrimSpace(s.RoleQuery) == "" {
		return ErrInvalidRoleQuery
	}
	if _, err := tx.ExecContext(ctx, s.Query,
		tenant.organizationID,
		tenant.actorID,
		int64(tenant.authorizationVersion),
		string(tenant.source),
	); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, s.RoleQuery)
	return err
}

// TxFunc is the callback run while a trusted transaction-local tenant context
// is active.
type TxFunc func(context.Context, *sql.Tx) error

// Runner owns the database transaction boundary. The database connection is
// held by database/sql until the callback has completed and Commit or Rollback
// has finished.
type Runner struct {
	DB       *sql.DB
	Selector OrganizationSelector
	Setter   ContextSetter
	Options  *sql.TxOptions
}

// Run executes a transaction using an already established trusted tenant
// context. It fails closed when the context is absent or invalid.
func (r Runner) Run(ctx context.Context, fn TxFunc) error {
	if err := r.validate(ctx, fn); err != nil {
		return err
	}
	tenant, ok := FromContext(ctx)
	if !ok {
		return ErrMissingTenantContext
	}
	return r.run(ctx, tenant, fn)
}

// RunForOrganization validates an untrusted organization choice through the
// configured selector, then establishes the trusted context only for the
// duration of the transaction callback.
func (r Runner) RunForOrganization(ctx context.Context, organizationID string, source Source, fn TxFunc) error {
	if err := r.validate(ctx, fn); err != nil {
		return err
	}
	if r.Selector == nil {
		return ErrNilOrganizationSelector
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	requestedOrganizationID, err := normalizeOrganizationID(organizationID)
	if err != nil {
		return err
	}
	if active, ok := FromContext(ctx); ok && active.organizationID != requestedOrganizationID {
		return ErrCrossOrganizationContext
	}

	selection, err := r.Selector.SelectOrganization(ctx, requestedOrganizationID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOrganizationSelection, err)
	}
	tenant, err := newTenantContext(selection, source)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTenantContext, err)
	}
	if tenant.organizationID != requestedOrganizationID {
		return ErrCrossOrganizationContext
	}
	if active, ok := FromContext(ctx); ok && !sameTenant(active, tenant) {
		return ErrTenantContextConflict
	}
	return r.run(withTenantContext(ctx, tenant), tenant, fn)
}

func (r Runner) validate(ctx context.Context, fn TxFunc) error {
	if ctx == nil {
		return ErrNilContext
	}
	if r.DB == nil {
		return ErrNilDatabase
	}
	if r.Setter == nil {
		return ErrNilContextSetter
	}
	if fn == nil {
		return ErrNilTransactionFunc
	}
	return nil
}

func (r Runner) run(ctx context.Context, tenant TenantContext, fn TxFunc) error {
	if err := tenant.valid(); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}

	tx, err := r.DB.BeginTx(ctx, r.Options)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBeginTransaction, err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = tx.Rollback()
		}
	}()

	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := r.Setter.SetTenantContext(ctx, tx, tenant); err != nil {
		return fmt.Errorf("%w: %w", ErrSetTenantContext, err)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := fn(withTenantContext(ctx, tenant), tx); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: %w", ErrCommitTransaction, err)
	}
	finished = true
	return nil
}

func sameTenant(left, right TenantContext) bool {
	return left.organizationID == right.organizationID &&
		left.actorID == right.actorID &&
		left.authorizationVersion == right.authorizationVersion &&
		left.source == right.source
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
