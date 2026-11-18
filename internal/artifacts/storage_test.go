package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalPutBoundsAndDigest(t *testing.T) {
	root := t.TempDir()
	storage := Local{Root: root}
	content := []byte("hello artifact")
	stored, err := storage.Put(context.Background(), "org/run/artifact", bytes.NewReader(content), MaxArtifactBytes)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(content)
	if stored.Size != int64(len(content)) || stored.SHA256 != hex.EncodeToString(hash[:]) {
		t.Fatalf("stored metadata = %#v", stored)
	}
	read, err := os.ReadFile(filepath.Join(root, "org/run/artifact"))
	if err != nil {
		t.Fatal(err)
	}
	if string(read) != string(content) {
		t.Fatalf("stored content = %q, want %q", read, content)
	}
	if _, err := storage.Put(context.Background(), "org/run/artifact", bytes.NewReader([]byte("replacement")), MaxArtifactBytes); err == nil {
		t.Fatal("expected immutable key collision")
	}
}

func TestLocalRejectsTraversalAndOversize(t *testing.T) {
	storage := Local{Root: t.TempDir()}
	if _, err := storage.Put(context.Background(), "../escape", bytes.NewReader([]byte("x")), MaxArtifactBytes); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("traversal error = %v", err)
	}
	if _, err := storage.Put(context.Background(), "a", bytes.NewReader(bytes.Repeat([]byte("x"), 4)), 3); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestLocalReportsFreeBytes(t *testing.T) {
	storage := Local{Root: filepath.Join(t.TempDir(), "artifacts")}
	free, err := storage.FreeBytes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if free == 0 {
		t.Fatalf("free bytes = %d, want a positive filesystem value", free)
	}
}
