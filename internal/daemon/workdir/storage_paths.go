package workdir

import (
	"path/filepath"
	"strings"
)

// StoragePaths keeps private journal and manifest references relative to the
// stable CLI location. Only the verified storage root prefix may change.
type StoragePaths struct {
	Stable, Physical string
}

func replaceRoot(path, from, to string) string {
	if from == "" || to == "" || from == to || filepath.Clean(path) != path {
		return path
	}
	if path == from {
		return to
	}
	if suffix, ok := strings.CutPrefix(path, from+string(filepath.Separator)); ok {
		return filepath.Join(to, suffix)
	}
	return path
}

func (s StoragePaths) Stored(path string) string  { return replaceRoot(path, s.Physical, s.Stable) }
func (s StoragePaths) Current(path string) string { return replaceRoot(path, s.Stable, s.Physical) }

func (b Binding) MapPaths(convert func(string) string) Binding {
	b.ActualPath = convert(b.ActualPath)
	b.ConfiguredPath = convert(b.ConfiguredPath)
	b.RepositoryRoot = convert(b.RepositoryRoot)
	return b
}
