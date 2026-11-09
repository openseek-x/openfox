package wp19

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/platform/postgres"
	"openfox/internal/tenancy"
)

// TestDispatchCapacityWrappersEnforceContextSource 验证应用角色只能经包装函数触达容量原语：
// 直接调用原语、用户请求上下文调用 worker 包装、设备冒用其他设备 ID 领取，都必须以 42501 拒绝。
func TestDispatchCapacityWrappersEnforceContextSource(t *testing.T) {
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := postgres.New(ctx, postgres.Config{URL: url, MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 3 * time.Second, LockTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := &harness{t: t, ctx: ctx, url: url, pool: pool}

	const (
		reservation = "00000000-0000-4000-8000-0000000019a1"
		run         = "00000000-0000-4000-8000-0000000019a2"
		offer       = "00000000-0000-4000-8000-0000000019a3"
	)
	reserve := func(function string) func(context.Context, pgx.Tx) error {
		return func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT openfox_private.`+function+`($1::uuid, $2::uuid, $3::uuid, 1, $4::uuid, $5::uuid, $6::uuid, clock_timestamp() + interval '1 minute')`,
				orgA, reservation, run, device1, agentA, offer)
			return err
		}
	}
	userSelection := tenancy.OrganizationSelection{OrganizationID: orgA, ActorID: alice, AuthorizationVersion: h.authVersion(orgA, alice)}
	cases := map[string]error{
		"app role cannot call reserve_capacity directly": pool.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, selector(userSelection), reserve("reserve_capacity")),
		"user request cannot use worker reservation":     pool.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, selector(userSelection), reserve("dispatch_reserve_capacity")),
		"device request cannot use worker reservation":   h.tryDevice(orgA, reserve("dispatch_reserve_capacity")),
		"user request cannot reap offers": pool.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, selector(userSelection), func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT openfox_private.dispatch_reap_offer($1::uuid, $2::uuid)`, orgA, offer)
			return err
		}),
		"device cannot claim as another device": h.tryDevice(orgA, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT openfox_private.device_claim_offer($1::uuid, $2::uuid, $3::uuid, 1)`, orgA, offer, e2eDevice)
			return err
		}),
		"app role cannot read context source": pool.RunForOrganization(ctx, orgA, tenancy.SourceUserRequest, selector(userSelection), func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT openfox_private.current_context_source()`)
			return err
		}),
	}
	for name, err := range cases {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%s: err = %v, want SQLSTATE 42501", name, err)
		}
	}
}
