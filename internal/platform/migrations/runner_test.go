package migrations

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidateHistoryAcceptsContiguousUnchangedPrefix(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "000001_first.sql"),
		filepath.Join(dir, "000002_second.sql"),
	}
	for index, path := range paths {
		if err := os.WriteFile(path, []byte("select "+string(rune('1'+index))+";\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := fileChecksum(files[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	history := []HistoryEntry{{Number: 1, Filename: "000001_first.sql", Checksum: checksum, AppliedAt: time.Now()}}
	if err := ValidateHistory(files, history); err != nil {
		t.Fatalf("ValidateHistory() error = %v", err)
	}
}

func TestValidateHistoryRejectsChangedAppliedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "000001_first.sql")
	if err := os.WriteFile(path, []byte("select 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateHistory(files, []HistoryEntry{{
		Number: 1, Filename: "000001_first.sql", Checksum: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}); !errors.Is(err, ErrHistoryConflict) {
		t.Fatalf("ValidateHistory() error = %v, want ErrHistoryConflict", err)
	}
}

func TestValidateHistoryRejectsSkippedManifestEntry(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"000001_first.sql", "000002_second.sql"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("select 1;\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := fileChecksum(files[1].Path)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateHistory(files, []HistoryEntry{{
		Number: 2, Filename: "000002_second.sql", Checksum: checksum,
	}})
	if !errors.Is(err, ErrHistoryConflict) {
		t.Fatalf("ValidateHistory() error = %v, want ErrHistoryConflict", err)
	}
}
