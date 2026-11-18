package assign

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/platform/migrations"
)

// The explicit admin connection must point at a dedicated test cluster. Never
// derive administrative access from an application database or clone its data.
func newWorkingHoursDatabase(t *testing.T) string {
	t.Helper()
	if os.Getenv("OPENFOX_TEST_DATABASE_URL") == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	adminURL := os.Getenv("OPENFOX_TEST_DATABASE_ADMIN_URL")
	if adminURL == "" {
		t.Fatal("OPENFOX_TEST_DATABASE_ADMIN_URL must identify the dedicated test cluster maintenance database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	database := "assign_working_hours_" + hex.EncodeToString(random[:])
	identifier := pgx.Identifier{database}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier+" TEMPLATE template0 ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C'"); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	// Register cleanup only after successful creation; only this random database
	// is ever removed, including when migration or fixture initialization fails.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		defer admin.Close(cleanupCtx)
		if _, err := admin.Exec(cleanupCtx, "DROP DATABASE "+identifier); err != nil {
			t.Errorf("remove isolated scheduling database %s: %v", database, err)
		}
	})
	dsn := adminURL
	if parsed, err := url.Parse(adminURL); err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		parsed.Path = "/" + database
		query := parsed.Query()
		query.Del("dbname")
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " dbname=" + database
	}
	result, err := migrations.Apply(ctx, migrations.ApplyConfig{URL: dsn, Dir: "../../../db/migrations", Timeout: 120 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applied) == 0 || len(result.Applied) != len(result.Current) {
		t.Fatal("isolated scheduling database was not freshly migrated")
	}
	fixture, err := os.ReadFile("../db/fixture.sql")
	if err != nil {
		t.Fatal(err)
	}
	// psql's stop-on-error directive is not SQL. pgx reports execution errors.
	fixtureSQL := strings.TrimPrefix(string(fixture), "\\set ON_ERROR_STOP on\n")
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, fixtureSQL); err != nil {
		t.Fatal(err)
	}
	t.Logf("initialized isolated scheduling database with %d migrations", len(result.Current))
	return dsn
}
