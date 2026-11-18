//go:build !darwin

package cliagent

import (
	"context"
	"fmt"
)

type geminiSnapshotStats struct {
	Bytes                              int64
	Files, Entries, Excluded, Symlinks int
}

func geminiSnapshot(context.Context, string, string) (geminiSnapshotStats, error) {
	return geminiSnapshotStats{}, fmt.Errorf("Gemini snapshots require macOS")
}
func geminiWorkspaceAllowed(string, string) error {
	return fmt.Errorf("Gemini snapshots require macOS")
}
