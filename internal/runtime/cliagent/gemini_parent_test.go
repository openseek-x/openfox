//go:build darwin

package cliagent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestGeminiNativeParentHelper(t *testing.T) {
	if os.Getenv("OPENFOX_GEMINI_PARENT_HELPER") != "1" {
		t.Skip("owned helper process only")
	}
	der, err := base64.StdEncoding.DecodeString(os.Getenv("OPENFOX_GEMINI_PARENT_CERT"))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	a := NewGemini(os.Getenv("OPENFOX_GEMINI_PATH"))
	a.StateRoot = os.Getenv("OPENFOX_GEMINI_PARENT_STATE")
	a.geminiHTTP = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	a.geminiEndpoint = os.Getenv("OPENFOX_GEMINI_PARENT_ENDPOINT")
	execution, err := a.Start(context.Background(), geminiNativeRequest(""))
	if err != nil {
		t.Fatal(err)
	}
	for range execution.Events() {
	}
}

func TestNativeGeminiParentDeath(t *testing.T) {
	a := geminiNativeAdapter(t)
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestGeminiNativeParentHelper$", "-test.timeout=14s")
	cmd.Env = append(os.Environ(), "OPENFOX_GEMINI_PARENT_HELPER=1", "OPENFOX_GEMINI_PARENT_STATE="+a.StateRoot, "OPENFOX_GEMINI_PARENT_ENDPOINT="+server.URL, "OPENFOX_GEMINI_PARENT_CERT="+base64.StdEncoding.EncodeToString(server.Certificate().Raw))
	log, err := os.CreateTemp(t.TempDir(), "helper-log")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("helper did not reach model")
	}
	ps, err := exec.Command("/bin/ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		t.Fatal(err)
	}
	var nativePID int
	for _, line := range strings.Split(string(ps), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 2 && fields[1] == strconv.Itoa(cmd.Process.Pid) && strings.Contains(line, "/bundle/gemini.js") {
			nativePID, _ = strconv.Atoi(fields[0])
		}
	}
	if nativePID == 0 {
		t.Fatal("actual owned native child not found")
	}
	began := time.Now()
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	deadline := time.Now().Add(4 * time.Second)
	for syscall.Kill(nativePID, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if syscall.Kill(nativePID, 0) == nil {
		t.Fatal("native child survived host SIGKILL")
	}
	entries, _ := os.ReadDir(a.StateRoot)
	if len(entries) == 0 {
		t.Fatal("hard-kill probe did not preserve expected evidence of unexecuted host cleanup")
	}
	t.Logf("owned host SIGKILL: native PID %d exited in %s through parent FD; %d private directory retained for host cleanup, removed by test-owned cleanup", nativePID, time.Since(began), len(entries))
}
