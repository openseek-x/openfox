package wp16

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	acceptanceOrgA = "00000000-0000-0000-0000-0000000000a1"
	acceptanceOrgB = "00000000-0000-0000-0000-0000000000b1"
)

func openAcceptanceDatabase(t *testing.T) *pgx.Conn {
	t.Helper()
	url := os.Getenv("OPENFOX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = conn.Close(closeCtx)
	})
	return conn
}

func TestReleaseFixtureHasMultipleOrganizationsRolesAndDevices(t *testing.T) {
	conn := openAcceptanceDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var organizationCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM openfox.organizations").Scan(&organizationCount); err != nil {
		t.Fatal(err)
	}
	if organizationCount != 2 {
		t.Fatalf("organization count = %d, want 2", organizationCount)
	}

	var roleCount int
	if err := conn.QueryRow(ctx, `
		SELECT count(DISTINCT role)
		  FROM openfox.organization_members
		 WHERE organization_id = $1::uuid AND status = 'ACTIVE'
	`, acceptanceOrgA).Scan(&roleCount); err != nil {
		t.Fatal(err)
	}
	if roleCount != 4 {
		t.Fatalf("organization A active role count = %d, want 4", roleCount)
	}

	var deviceCount, agentCount int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM openfox.physical_devices").Scan(&deviceCount); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM openfox.agents").Scan(&agentCount); err != nil {
		t.Fatal(err)
	}
	if deviceCount < 2 || agentCount < 2 {
		t.Fatalf("logical devices/agents = %d/%d, want at least 2/2", deviceCount, agentCount)
	}

	var crossTenantAgents int
	if err := conn.QueryRow(ctx, `
		SELECT count(*)
		  FROM openfox.agents a
		  JOIN openfox.device_grants g ON g.device_id = a.device_id
		 WHERE a.organization_id <> g.organization_id
	`).Scan(&crossTenantAgents); err != nil {
		t.Fatal(err)
	}
	if crossTenantAgents != 0 {
		t.Fatalf("cross-tenant agent grants = %d, want 0", crossTenantAgents)
	}

	var organizationBMembers int
	if err := conn.QueryRow(ctx, `
		SELECT count(*)
		  FROM openfox.organization_members
		 WHERE organization_id = $1::uuid AND status = 'ACTIVE'
	`, acceptanceOrgB).Scan(&organizationBMembers); err != nil {
		t.Fatal(err)
	}
	if organizationBMembers == 0 {
		t.Fatal("organization B has no active member")
	}
}

func TestReleaseSchemaUsesForcedTenantRLS(t *testing.T) {
	conn := openAcceptanceDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, table := range []string{
		"organizations",
		"organization_members",
		"spaces",
		"space_members",
		"tasks",
		"organization_capacity",
		"device_grants",
		"agents",
		"capacity_reservations",
		"dispatch_offers",
		"conversations",
		"runs",
		"run_events",
		"run_results",
		"artifacts",
		"organization_runtime_configs",
	} {
		var enabled, forced bool
		if err := conn.QueryRow(ctx, `
			SELECT c.relrowsecurity, c.relforcerowsecurity
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = 'openfox' AND c.relname = $1
		`, table).Scan(&enabled, &forced); err != nil {
			t.Fatalf("inspect RLS for %s: %v", table, err)
		}
		if !enabled || !forced {
			t.Fatalf("%s RLS = enabled:%t forced:%t, want both true", table, enabled, forced)
		}
	}
}

func TestReleaseWorkflowHasSingleNonTerminalDefaultPerTaskType(t *testing.T) {
	conn := openAcceptanceDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var terminalDefaults int
	if err := conn.QueryRow(ctx, `
		SELECT count(*)
		  FROM openfox.workflow_statuses ws
		  JOIN openfox.task_statuses ts
		    ON ts.organization_id = ws.organization_id AND ts.id = ws.status_id
		 WHERE ws.is_default AND ts.category IN ('DONE', 'CANCELLED', 'DUPLICATE')
	`).Scan(&terminalDefaults); err != nil {
		t.Fatal(err)
	}
	if terminalDefaults != 0 {
		t.Fatalf("terminal default statuses = %d, want 0", terminalDefaults)
	}

	var invalidDefaultGroups int
	if err := conn.QueryRow(ctx, `
		SELECT count(*)
		  FROM (
			SELECT organization_id, space_id, task_type_id
			  FROM openfox.workflow_statuses
			 GROUP BY organization_id, space_id, task_type_id
			 HAVING count(*) FILTER (WHERE is_default) <> 1
		  ) invalid
	`).Scan(&invalidDefaultGroups); err != nil {
		t.Fatal(err)
	}
	if invalidDefaultGroups != 0 {
		t.Fatalf("task types with invalid default count = %d, want 0", invalidDefaultGroups)
	}
}
