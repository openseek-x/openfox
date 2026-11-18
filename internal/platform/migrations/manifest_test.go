package migrations

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSortsAndValidatesMigrations(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"000002_second.sql", "000001_first.sql", "README.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(files) != 2 || files[0].Number != 1 || files[1].Number != 2 {
		t.Fatalf("files = %#v", files)
	}
}

func TestLoadRejectsDuplicateNumbers(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"000001_first.sql", "000001_second.sql"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load() unexpectedly accepted duplicate migration numbers")
	}
}
