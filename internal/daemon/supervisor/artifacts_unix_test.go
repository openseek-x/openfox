//go:build unix

package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestInspectArtifactRejectsSpecialFiles：命名管道与设备文件（含指向它们的符号链接）不是普通文件，一律拒绝且不阻塞。
func TestInspectArtifactRejectsSpecialFiles(t *testing.T) {
	work, out := artifactRoots(t)
	fifo := filepath.Join(work, "pipe.txt")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if err := os.Symlink("/dev/null", filepath.Join(work, "null.txt")); err != nil {
		t.Fatal(err)
	}
	roots := []string{work, out}
	if _, err := inspectArtifact("pipe.txt", roots, 1024); !strings.Contains(rejection(t, err), "not a regular file") {
		t.Fatalf("fifo: %v", err)
	}
	// /dev/null 在允许目录之外：先按越界拒绝。
	if _, err := inspectArtifact("null.txt", roots, 1024); !strings.Contains(rejection(t, err), "outside the managed run roots") {
		t.Fatalf("device symlink: %v", err)
	}
	// 允许目录本身位于 /dev 时（只为覆盖设备文件分支）：设备文件按非普通文件拒绝。
	if _, err := inspectArtifact("null", resolveRoots([]string{"/dev"}), 1024); !strings.Contains(rejection(t, err), "not a regular file") {
		t.Fatalf("device: %v", err)
	}
}
