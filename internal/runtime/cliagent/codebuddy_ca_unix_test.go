//go:build unix

package cliagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCodeBuddyCAFileBoundsAndFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "ca-pipe")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := codeBuddyModelClient(map[string]string{"SSL_CERT_FILE": fifo}); err == nil {
		t.Fatal("FIFO CA accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("FIFO blocked preflight")
	}
	large := filepath.Join(dir, "large.pem")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Truncate(codeBuddyMaxCABytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := codeBuddyModelClient(map[string]string{"SSL_CERT_FILE": large}); err == nil {
		t.Fatal("oversized CA accepted")
	}
}
