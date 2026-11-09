package migrations

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var filenamePattern = regexp.MustCompile(`^[0-9]{6}_[a-z0-9_]+\.sql$`)

type File struct {
	Number int
	Path   string
}

func Load(dir string) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]File, 0, len(entries))
	seen := make(map[int]string)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		if !filenamePattern.MatchString(entry.Name()) {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		var number int
		if _, err := fmt.Sscanf(entry.Name(), "%06d_", &number); err != nil {
			return nil, fmt.Errorf("parse migration number %q: %w", entry.Name(), err)
		}
		if previous, exists := seen[number]; exists {
			return nil, fmt.Errorf("duplicate migration number %06d in %q and %q", number, previous, entry.Name())
		}
		seen[number] = entry.Name()
		files = append(files, File{Number: number, Path: filepath.Join(dir, entry.Name())})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Number < files[j].Number })
	return files, nil
}
