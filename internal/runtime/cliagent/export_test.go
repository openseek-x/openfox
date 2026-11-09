package cliagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExportSessionPreservesClaudeAndPiNativeRelativePaths(t *testing.T) {
	const claudeID = "123e4567-e89b-12d3-a456-426614174000"
	const piID = "223e4567-e89b-12d3-a456-426614174000"
	root := t.TempDir()
	claudeRoot := filepath.Join(root, "claude")
	piRoot := filepath.Join(root, "pi")
	claudeName := "projects/project-path/" + claudeID + ".jsonl"
	piName := "sessions/--workspace-path--/20261108T120000_" + piID + ".jsonl"
	for name, body := range map[string]string{
		filepath.Join(claudeRoot, claudeName): "claude session\n",
		filepath.Join(piRoot, piName):         "pi session\n",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	claude := NewClaudeCode("")
	claude.Environment = map[string]string{"CLAUDE_CONFIG_DIR": claudeRoot}
	file, err := claude.ExportSession(context.Background(), claudeID)
	if err != nil {
		t.Fatal(err)
	}
	if file.Name != claudeName || string(file.Data) != "claude session\n" {
		t.Fatalf("Claude export = %q, %q", file.Name, file.Data)
	}

	pi := NewPi("")
	pi.Environment = map[string]string{"PI_CODING_AGENT_DIR": piRoot}
	file, err = pi.ExportSession(context.Background(), piID)
	if err != nil {
		t.Fatal(err)
	}
	if file.Name != piName || string(file.Data) != "pi session\n" {
		t.Fatalf("Pi export = %q, %q", file.Name, file.Data)
	}
}

func TestExportSessionHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	adapter := NewPi("")
	adapter.Environment = map[string]string{"PI_CODING_AGENT_DIR": root}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.ExportSession(ctx, "223e4567-e89b-12d3-a456-426614174000"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ExportSession error = %v, want context canceled", err)
	}
}
