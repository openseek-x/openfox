package wp15

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/platform/migrations"
)

func TestMigrationLedgerAndWP14SchemaArePresent(t *testing.T) {
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
	defer conn.Close(ctx)

	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM openfox_meta.schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	files, err := migrations.Load("../../../db/migrations")
	if err != nil || len(files) == 0 {
		t.Fatalf("load migration manifest: %v", err)
	}
	if count != len(files) {
		t.Fatalf("migration count = %d, want %d", count, len(files))
	}
	var last string
	if err := conn.QueryRow(ctx, "SELECT filename FROM openfox_meta.schema_migrations ORDER BY migration_number DESC LIMIT 1").Scan(&last); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Base(files[len(files)-1].Path); last != want {
		t.Fatalf("last migration = %q, want %q", last, want)
	}
	var checksumLength int
	if err := conn.QueryRow(ctx, "SELECT length(checksum_sha256) FROM openfox_meta.schema_migrations WHERE migration_number = 15").Scan(&checksumLength); err != nil {
		t.Fatal(err)
	}
	if checksumLength != 64 {
		t.Fatalf("migration checksum length = %d, want 64", checksumLength)
	}

	var wp14Table string
	if err := conn.QueryRow(ctx, "SELECT to_regclass('openfox.organization_runtime_configs')::text").Scan(&wp14Table); err != nil {
		t.Fatal(err)
	}
	if wp14Table != "openfox.organization_runtime_configs" {
		t.Fatalf("WP-14 table = %q", wp14Table)
	}
}
