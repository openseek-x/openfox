package migrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	metaSchema       = "openfox_meta"
	lockName         = "openfox:schema-migrations"
	defaultApplyTTL  = 10 * time.Minute
	defaultLockLimit = 5 * time.Second
)

var (
	ErrInvalidApplyConfig = errors.New("migrations: invalid apply configuration")
	ErrHistoryConflict    = errors.New("migrations: applied history is incompatible with manifest")
)

// ApplyConfig controls an explicit migration run. Apply never runs as part of
// server startup; callers must invoke it as a maintenance operation.
type ApplyConfig struct {
	URL         string
	Dir         string
	Timeout     time.Duration
	LockTimeout time.Duration
}

// HistoryEntry is the durable identity of one successfully committed file.
// The checksum prevents silently changing an already applied migration.
type HistoryEntry struct {
	Number    int
	Filename  string
	Checksum  string
	AppliedAt time.Time
}

// ApplyResult reports both the files applied by this invocation and the final
// contiguous history. It intentionally does not expose SQL or credentials.
type ApplyResult struct {
	Applied []HistoryEntry
	Current []HistoryEntry
}

// Apply connects using the supplied bootstrap URL, acquires a PostgreSQL
// advisory lock, and applies unapplied migrations in manifest order.
func Apply(ctx context.Context, cfg ApplyConfig) (ApplyResult, error) {
	if ctx == nil || cfg.URL == "" || cfg.Dir == "" {
		return ApplyResult{}, ErrInvalidApplyConfig
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultApplyTTL
	}
	lockTimeout := cfg.LockTimeout
	if lockTimeout <= 0 {
		lockTimeout = defaultLockLimit
	}
	files, err := Load(cfg.Dir)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("load migration manifest: %w", err)
	}
	entries, err := readMigrationFiles(files)
	if err != nil {
		return ApplyResult{}, err
	}

	applyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := pgx.Connect(applyCtx, cfg.URL)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("connect migration database: %w", err)
	}
	defer conn.Close(context.Background())

	if err := bootstrapHistory(applyCtx, conn); err != nil {
		return ApplyResult{}, err
	}
	if err := acquireLock(applyCtx, conn, lockTimeout); err != nil {
		return ApplyResult{}, err
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock(hashtextextended($1, 0))", lockName)
	}()

	history, err := loadHistory(applyCtx, conn)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := ValidateHistory(files, history); err != nil {
		return ApplyResult{}, err
	}

	result := ApplyResult{Current: append([]HistoryEntry(nil), history...)}
	for index := len(history); index < len(entries); index++ {
		entry, err := applyOne(applyCtx, conn, entries[index], lockTimeout)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("apply %s: %w", entries[index].Filename, err)
		}
		result.Applied = append(result.Applied, entry)
		result.Current = append(result.Current, entry)
	}
	return result, nil
}

// ValidateHistory ensures the database contains exactly a contiguous prefix
// of the current manifest and that every applied file has the same checksum.
func ValidateHistory(files []File, history []HistoryEntry) error {
	if len(history) > len(files) {
		return fmt.Errorf("%w: database has %d entries but manifest has %d", ErrHistoryConflict, len(history), len(files))
	}
	for index, applied := range history {
		file := files[index]
		if applied.Number != file.Number || filepath.Base(file.Path) != applied.Filename {
			return fmt.Errorf("%w: entry %d is %06d/%s, manifest has %06d/%s", ErrHistoryConflict, index, applied.Number, applied.Filename, file.Number, filepath.Base(file.Path))
		}
		checksum, err := fileChecksum(file.Path)
		if err != nil {
			return fmt.Errorf("read checksum for %s: %w", file.Path, err)
		}
		if applied.Checksum != checksum {
			return fmt.Errorf("%w: checksum mismatch for %s", ErrHistoryConflict, applied.Filename)
		}
	}
	return nil
}

type migrationFile struct {
	File
	Filename string
	SQL      []byte
	Checksum string
}

func readMigrationFiles(files []File) ([]migrationFile, error) {
	result := make([]migrationFile, 0, len(files))
	for _, file := range files {
		sql, err := os.ReadFile(file.Path)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", file.Path, err)
		}
		checksum := sha256.Sum256(sql)
		result = append(result, migrationFile{
			File: file, Filename: filepath.Base(file.Path), SQL: sql,
			Checksum: hex.EncodeToString(checksum[:]),
		})
	}
	return result, nil
}

func fileChecksum(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	checksum := sha256.Sum256(contents)
	return hex.EncodeToString(checksum[:]), nil
}

func bootstrapHistory(ctx context.Context, conn *pgx.Conn) error {
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS openfox_meta"); err != nil {
		return fmt.Errorf("create migration metadata schema: %w", err)
	}
	if _, err := conn.Exec(ctx, `
CREATE TABLE IF NOT EXISTS openfox_meta.schema_migrations (
    migration_number integer PRIMARY KEY,
    filename text NOT NULL UNIQUE,
    checksum_sha256 text NOT NULL CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$'),
    applied_at timestamptz NOT NULL DEFAULT clock_timestamp()
)`); err != nil {
		return fmt.Errorf("create migration metadata table: %w", err)
	}
	if _, err := conn.Exec(ctx, "REVOKE ALL ON SCHEMA openfox_meta FROM PUBLIC"); err != nil {
		return fmt.Errorf("lock migration metadata schema: %w", err)
	}
	if _, err := conn.Exec(ctx, "REVOKE ALL ON TABLE openfox_meta.schema_migrations FROM PUBLIC"); err != nil {
		return fmt.Errorf("lock migration metadata table: %w", err)
	}
	return nil
}

func acquireLock(ctx context.Context, conn *pgx.Conn, lockTimeout time.Duration) error {
	if _, err := conn.Exec(ctx, "SELECT set_config('lock_timeout', $1, false)", durationMillis(lockTimeout)); err != nil {
		return fmt.Errorf("set migration lock timeout: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended($1, 0))", lockName); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	return nil
}

func loadHistory(ctx context.Context, conn *pgx.Conn) ([]HistoryEntry, error) {
	rows, err := conn.Query(ctx, `
SELECT migration_number, filename, checksum_sha256, applied_at
  FROM openfox_meta.schema_migrations
 ORDER BY migration_number`)
	if err != nil {
		return nil, fmt.Errorf("read migration history: %w", err)
	}
	defer rows.Close()
	history := make([]HistoryEntry, 0)
	for rows.Next() {
		var entry HistoryEntry
		if err := rows.Scan(&entry.Number, &entry.Filename, &entry.Checksum, &entry.AppliedAt); err != nil {
			return nil, fmt.Errorf("scan migration history: %w", err)
		}
		history = append(history, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read migration history rows: %w", err)
	}
	return history, nil
}

func applyOne(ctx context.Context, conn *pgx.Conn, migration migrationFile, lockTimeout time.Duration) (HistoryEntry, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return HistoryEntry{}, fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)", durationMillis(lockTimeout)); err != nil {
		return HistoryEntry{}, fmt.Errorf("set migration lock timeout: %w", err)
	}
	if _, err := tx.Exec(ctx, string(migration.SQL)); err != nil {
		return HistoryEntry{}, err
	}
	// Historical files normally reset their role; enforcing it here also keeps
	// the metadata insert under the bootstrap connection's original role.
	if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
		return HistoryEntry{}, fmt.Errorf("reset migration role: %w", err)
	}
	var entry HistoryEntry
	if err := tx.QueryRow(ctx, `
INSERT INTO openfox_meta.schema_migrations (migration_number, filename, checksum_sha256)
VALUES ($1, $2, $3)
RETURNING migration_number, filename, checksum_sha256, applied_at`, migration.Number, migration.Filename, migration.Checksum).
		Scan(&entry.Number, &entry.Filename, &entry.Checksum, &entry.AppliedAt); err != nil {
		return HistoryEntry{}, fmt.Errorf("record migration: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return HistoryEntry{}, fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return entry, nil
}

func durationMillis(value time.Duration) string {
	return fmt.Sprintf("%dms", value.Milliseconds())
}
