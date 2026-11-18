package hermes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestTrustedTreeRejectsChangedAndAdditionalCode(t *testing.T) {
	data := []byte("trusted module\n")
	digest := sha256.Sum256(data)
	manifest := &buildManifest{Files: map[string]fileIdentity{"site/module.py": {SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(data))}}}
	for _, change := range []string{"none", "same-size-edit", "additional-pth", "missing", "symlink"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "module.py")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "same-size-edit":
				if err := os.WriteFile(path, []byte("changed module\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "additional-pth":
				if err := os.WriteFile(filepath.Join(root, "unexpected.pth"), []byte("import unexpected"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("other.py", path); err != nil {
					t.Fatal(err)
				}
			}
			err := verifyTree(context.Background(), manifest, "site", root)
			if (err == nil) != (change == "none") {
				t.Fatalf("change=%s error=%v", change, err)
			}
		})
	}
}

func TestDetectReportsUntrustedInstallationWithoutExecutingIt(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "must-not-exist")
	python := filepath.Join(root, "python")
	if err := os.WriteFile(python, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{SourceRoot: filepath.Join(root, "missing-source"), Python: python}
	report, err := a.Detect(context.Background())
	if err == nil || report.Executable != python || report.ProtocolReady || report.Runnable || report.Reason == "" {
		t.Fatalf("report=%+v error=%v", report, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("untrusted executable was launched")
	}
}
