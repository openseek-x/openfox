package migrations

import (
	"os"
	"path/filepath"
	"strings"
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

func TestRepositoryMigrationIndex(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "db", "migrations")
	files, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, body, start := strings.Cut(string(readme), "<!-- migration-index -->\n")
	index, _, end := strings.Cut(body, "<!-- /migration-index -->")
	var expected strings.Builder
	for i, file := range files {
		if file.Number != i+1 {
			t.Fatalf("迁移编号在 %06d 处不连续", i+1)
		}
		name := filepath.Base(file.Path)
		expected.WriteString("- [" + name + "](" + name + ")\n")
	}
	if !start || !end || index != expected.String() {
		t.Fatal("db/migrations/README.md 的当前执行顺序与 SQL 文件不一致")
	}
}
