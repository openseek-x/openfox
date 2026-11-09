// Package postgres owns the real PostgreSQL connection pool and transaction
// boundaries used by runtime application code.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"openfox/internal/tenancy"
)

var (
	ErrNilPool              = errors.New("postgres: nil pool")
	ErrNilTransactionFunc   = errors.New("postgres: nil transaction function")
	ErrInvalidConfig        = errors.New("postgres: invalid pool configuration")
	ErrOrganizationNotFound = errors.New("postgres: organization membership not found")
)

type Config struct {
	URL              string
	MaxConns         int32
	ContextRole      string
	AppRole          string
	StatementTimeout time.Duration
	LockTimeout      time.Duration
}

type Pool struct {
	pool   *pgxpool.Pool
	config Config
}

func New(ctx context.Context, cfg Config) (*Pool, error) {
	if ctx == nil {
		return nil, ErrInvalidConfig
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	parsed, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse postgres URL: %w", err)
	}
	parsed.MaxConns = cfg.MaxConns
	pool, err := pgxpool.NewWithConfig(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	result := &Pool{pool: pool, config: cfg}
	if err := result.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return result, nil
}

func NewFromURLFile(ctx context.Context, cfg Config, path string) (*Pool, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read postgres URL file: %w", err)
	}
	cfg.URL = strings.TrimSpace(string(contents))
	return New(ctx, cfg)
}

func (p *Pool) Close() {
	if p != nil && p.pool != nil {
		p.pool.Close()
	}
}

func (p *Pool) Ping(ctx context.Context) error {
	if p == nil || p.pool == nil {
		return ErrNilPool
	}
	return p.pool.Ping(ctx)
}

// Notify 发送一条 PostgreSQL 通知（autocommit，单条语句）。通知只携带提示信息，
// 不得包含正文或凭据；payload 上限由 PostgreSQL 限定为 8000 字节。
func (p *Pool) Notify(ctx context.Context, channel, payload string) error {
	if p == nil || p.pool == nil {
		return ErrNilPool
	}
	if err := validateRole(channel); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx, "SELECT pg_notify($1, $2)", channel, payload)
	return err
}

// Listen 用一条池外的专用连接 LISTEN channel，逐条回调 onNotify，直到 ctx 取消或连接出错。
// 不占用连接池容量；调用方负责断线重连。onNotify 必须快速返回（不得阻塞）。
func (p *Pool) Listen(ctx context.Context, channel string, onNotify func(payload string)) error {
	if p == nil || p.pool == nil {
		return ErrNilPool
	}
	if err := validateRole(channel); err != nil {
		return err
	}
	connConfig := p.pool.Config().ConnConfig.Copy()
	if connConfig.ConnectTimeout <= 0 || connConfig.ConnectTimeout > 10*time.Second {
		connConfig.ConnectTimeout = 10 * time.Second
	}
	conn, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		return fmt.Errorf("connect listener: %w", err)
	}
	defer func() {
		// 断网时关闭连接可能阻塞，限定在 3 秒内。
		closeContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = conn.Close(closeContext)
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
		return fmt.Errorf("listen %s: %w", channel, err)
	}
	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		onNotify(notification.Payload)
	}
}

// WithTransaction runs an application transaction without a tenant context.
// It is used only for control-plane authentication queries and writes.
func (p *Pool) WithTransaction(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	if err := p.validate(ctx, fn); err != nil {
		return err
	}
	tx, err := p.begin(ctx)
	if err != nil {
		return err
	}
	finished := false
	defer func() {
		if !finished {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err := p.setRole(ctx, tx, p.config.ContextRole); err != nil {
		return fmt.Errorf("set context role: %w", err)
	}
	if err := p.setRole(ctx, tx, p.config.AppRole); err != nil {
		return fmt.Errorf("set application role: %w", err)
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit postgres transaction: %w", err)
	}
	finished = true
	return nil
}

// RunForOrganization validates the requested organization through the trusted
// selector, then installs transaction-local context before invoking the
// tenant callback.
func (p *Pool) RunForOrganization(
	ctx context.Context,
	organizationID string,
	source tenancy.Source,
	selector tenancy.OrganizationSelector,
	fn func(context.Context, pgx.Tx) error,
) error {
	if err := p.validate(ctx, fn); err != nil {
		return err
	}
	if selector == nil {
		return tenancy.ErrNilOrganizationSelector
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	selection, err := selector.SelectOrganization(ctx, organizationID)
	if err != nil {
		return fmt.Errorf("%w: %w", tenancy.ErrOrganizationSelection, err)
	}
	tenant, err := tenancy.NewTenantContext(selection, source)
	if err != nil {
		return fmt.Errorf("%w: %w", tenancy.ErrInvalidTenantContext, err)
	}
	if tenant.OrganizationID() != strings.ToLower(organizationID) {
		return tenancy.ErrCrossOrganizationContext
	}
	tenantContext := tenancy.WithTenantContext(ctx, tenant)
	if tenantContext == ctx {
		return tenancy.ErrInvalidTenantContext
	}
	tx, err := p.begin(tenantContext)
	if err != nil {
		return err
	}
	finished := false
	defer func() {
		if !finished {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err := p.setRole(tenantContext, tx, p.config.ContextRole); err != nil {
		return fmt.Errorf("set context role: %w", err)
	}
	organization, actor, version, contextSource, err := tenant.DatabaseArgs()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(tenantContext,
		"SELECT openfox_private.set_tenant_context($1::uuid, $2::uuid, $3::bigint, $4::text)",
		organization, actor, int64(version), string(contextSource)); err != nil {
		return fmt.Errorf("%w: %w", tenancy.ErrSetTenantContext, err)
	}
	if err := p.setRole(tenantContext, tx, p.config.AppRole); err != nil {
		return fmt.Errorf("set application role: %w", err)
	}
	if err := fn(tenantContext, tx); err != nil {
		return err
	}
	if err := tx.Commit(tenantContext); err != nil {
		return fmt.Errorf("%w: %w", tenancy.ErrCommitTransaction, err)
	}
	finished = true
	return nil
}

// ResolveOrganization reads the narrow control-plane membership index while
// the connection is in the context-capable role. It is the only selector used
// by the HTTP user-request path in this slice.
func (p *Pool) ResolveOrganization(ctx context.Context, actorID, organizationID string) (tenancy.OrganizationSelection, string, error) {
	if p == nil || p.pool == nil {
		return tenancy.OrganizationSelection{}, "", ErrNilPool
	}
	if ctx == nil {
		return tenancy.OrganizationSelection{}, "", tenancy.ErrNilContext
	}
	var selection tenancy.OrganizationSelection
	var role, status string
	err := p.withContextRole(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT organization_id::text, authorization_version, role, membership_status
			FROM openfox_private.resolve_organization($1::uuid, $2::uuid)
		`, actorID, organizationID).Scan(&selection.OrganizationID, &selection.AuthorizationVersion, &role, &status)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return tenancy.OrganizationSelection{}, "", ErrOrganizationNotFound
		}
		return tenancy.OrganizationSelection{}, "", err
	}
	if status != "ACTIVE" {
		return tenancy.OrganizationSelection{}, "", ErrOrganizationNotFound
	}
	selection.ActorID = actorID
	return selection, role, nil
}

// RunControlPlane 在 context 角色下执行只返回组织 ID 的跨组织控制面查询
// （例如派发候选组织列表）。回调不得读取租户业务数据；租户写入必须再经
// RunForOrganization 进入单组织上下文。
func (p *Pool) RunControlPlane(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	if err := p.validate(ctx, fn); err != nil {
		return err
	}
	return p.withContextRole(ctx, func(tx pgx.Tx) error { return fn(ctx, tx) })
}

func (p *Pool) withContextRole(ctx context.Context, fn func(pgx.Tx) error) error {
	if err := p.validate(ctx, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		return err
	}
	tx, err := p.begin(ctx)
	if err != nil {
		return err
	}
	finished := false
	defer func() {
		if !finished {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err := p.setRole(ctx, tx, p.config.ContextRole); err != nil {
		return fmt.Errorf("set context role: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit postgres transaction: %w", err)
	}
	finished = true
	return nil
}

func (p *Pool) begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin postgres transaction: %w", err)
	}
	if err := p.setTimeouts(ctx, tx); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, err
	}
	return tx, nil
}

func (p *Pool) setTimeouts(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", durationMillis(p.config.StatementTimeout)); err != nil {
		return fmt.Errorf("set statement timeout: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)", durationMillis(p.config.LockTimeout)); err != nil {
		return fmt.Errorf("set lock timeout: %w", err)
	}
	return nil
}

func (p *Pool) setRole(ctx context.Context, tx pgx.Tx, role string) error {
	if err := validateRole(role); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "SET LOCAL ROLE "+role)
	return err
}

func (p *Pool) validate(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	if p == nil || p.pool == nil {
		return ErrNilPool
	}
	if ctx == nil {
		return tenancy.ErrNilContext
	}
	if fn == nil {
		return ErrNilTransactionFunc
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func validateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.URL) == "" || cfg.MaxConns < 2 || cfg.StatementTimeout <= 0 || cfg.LockTimeout <= 0 || cfg.LockTimeout > cfg.StatementTimeout {
		return ErrInvalidConfig
	}
	if err := validateRole(cfg.ContextRole); err != nil {
		return err
	}
	return validateRole(cfg.AppRole)
}

func validateRole(role string) error {
	if role == "" {
		return fmt.Errorf("%w: empty role", ErrInvalidConfig)
	}
	for index, character := range role {
		if index == 0 && character >= '0' && character <= '9' {
			return fmt.Errorf("%w: invalid role %q", ErrInvalidConfig, role)
		}
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return fmt.Errorf("%w: invalid role %q", ErrInvalidConfig, role)
		}
	}
	return nil
}

func durationMillis(value time.Duration) string {
	return strconv.FormatInt(value.Milliseconds(), 10) + "ms"
}
