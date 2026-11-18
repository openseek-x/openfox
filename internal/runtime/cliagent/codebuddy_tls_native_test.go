package cliagent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/runtime"
)

// Opt-in: run the compiled test under a loopback-only OS sandbox with a fresh
// HOME/TMPDIR. The official binary is pinned by hash, never installed by tests.
func TestCodeBuddyNativeTLSBoundary(t *testing.T) {
	executable := os.Getenv("OPENFOX_TEST_CODEBUDDY_NATIVE")
	if executable == "" {
		t.Skip("requires fixed official native CodeBuddy in an OS sandbox")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	if hex.EncodeToString(sum[:]) != "944382d1ec45a75f01426f812545b98d491ee4cb54146ca1f3fad27f8232ac60" {
		t.Fatal("native binary hash does not match qualified CodeBuddy 2.184.0")
	}
	for _, tc := range []struct {
		name, host     string
		trust, expired bool
	}{
		{"unknown-ca", "127.0.0.1", false, false}, {"wrong-hostname", "wrong.invalid", true, false}, {"expired", "127.0.0.1", true, true}, {"trusted", "127.0.0.1", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const key = "SYNTHETIC-R03-TLS-UPSTREAM-KEY"
			var count atomic.Int32
			var authOK atomic.Bool
			root := t.TempDir()
			state := filepath.Join(root, "state")
			work := filepath.Join(root, "work")
			os.MkdirAll(work, 0700)
			srv, ca := codeBuddyTLSFixture(t, tc.host, tc.expired, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/original/v1/chat/completions" {
					http.NotFound(w, r)
					return
				}
				count.Add(1)
				authOK.Store(r.Header.Get("Authorization") == "Bearer "+key)
				_ = filepath.WalkDir(state, func(path string, d os.DirEntry, err error) error {
					if err == nil && !d.IsDir() {
						b, e := os.ReadFile(path)
						if e == nil && strings.Contains(string(b), key) {
							t.Error("real model key appeared in native profile")
						}
					}
					return nil
				})
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"R03_TLS_VERIFIED\"},\"finish_reason\":null}]}\n\n")
				fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\ndata: [DONE]\n\n")
			}))
			// Independent standard TLS control, with its own pool and no relay/native.
			roots := x509.NewCertPool()
			if tc.trust {
				pem, _ := os.ReadFile(ca)
				if !roots.AppendCertsFromPEM(pem) {
					t.Fatal("control CA invalid")
				}
			}
			control := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}, Timeout: 3 * time.Second}
			response, controlErr := control.Get(srv.URL + "/control")
			if response != nil {
				response.Body.Close()
			}
			control.CloseIdleConnections()
			if (controlErr == nil) != (tc.name == "trusted") {
				t.Fatalf("standard TLS control unexpected: %v", controlErr)
			}
			capture := filepath.Join(root, "native.env")
			args := filepath.Join(root, "native.args")
			wrapper := filepath.Join(root, "codebuddy-wrapper")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			script := "#!/bin/sh\numask 077\n/usr/bin/env > " + quote(capture) + "\nprintf '%s\\n' \"$@\" > " + quote(args) + "\nexec " + quote(executable) + " \"$@\"\n"
			if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			a := NewCodeBuddy(wrapper)
			a.StateRoot = state
			a.Environment = map[string]string{"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "HTTP_PROXY": "", "HTTPS_PROXY": "", "http_proxy": "", "https_proxy": "", "NO_PROXY": "", "no_proxy": "", "SSL_CERT_FILE": "", "NODE_EXTRA_CA_CERTS": ""}
			req := codeBuddyRequest()
			req.Environment["CODEBUDDY_API_KEY"] = key
			req.Environment["CODEBUDDY_BASE_URL"] = srv.URL + "/original/v1"
			req.WorkingDirectory = work
			req.ApprovalMode = "READ_ONLY"
			req.Timeout = 15 * time.Second
			if tc.trust {
				req.Environment["NODE_EXTRA_CA_CERTS"] = ca
				req.EnvironmentAllowlist = append(req.EnvironmentAllowlist, "NODE_EXTRA_CA_CERTS")
			}
			execution, err := a.Start(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			eventsDone := make(chan bool, 1)
			go func() {
				leaked := false
				for event := range execution.Events() {
					leaked = leaked || strings.Contains(event.Text, key)
				}
				eventsDone <- leaked
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
			defer cancel()
			result, err := execution.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if <-eventsDone || strings.Contains(result.Output+result.Error, key) {
				t.Fatal("model key escaped redaction")
			}
			nativeEnv, _ := os.ReadFile(capture)
			nativeArgs, _ := os.ReadFile(args)
			if strings.Contains(string(nativeEnv), key) || strings.Contains(string(nativeArgs), key) || !strings.Contains(string(nativeEnv), "CODEBUDDY_BASE_URL=http://127.0.0.1:") || strings.Contains(string(nativeEnv), "NODE_EXTRA_CA_CERTS=") || strings.Contains(string(nativeEnv), "HTTPS_PROXY=") {
				t.Fatal("native received upstream credential/transport instead of local relay")
			}
			entries, err := os.ReadDir(state)
			if err != nil || len(entries) != 0 {
				t.Fatal("native state was not cleaned")
			}
			if tc.name == "trusted" {
				if result.Status != runtime.StatusSucceeded || count.Load() != 1 || !authOK.Load() || !strings.Contains(result.Output, "R03_TLS_VERIFIED") {
					t.Fatalf("trusted result=%+v requests=%d auth=%t", result, count.Load(), authOK.Load())
				}
			} else if result.Status != runtime.StatusFailed || count.Load() != 0 {
				t.Fatalf("TLS rejection result=%+v origin=%d", result, count.Load())
			}
			t.Logf("standard_TLS_accept=%t native_status=%s origin_requests=%d synthetic_authorization_matches=%t native_key_absent=true state_cleaned=true", controlErr == nil, result.Status, count.Load(), authOK.Load())
		})
	}
}

func TestCodeBuddyNativeTransportStop(t *testing.T) {
	executable := os.Getenv("OPENFOX_TEST_CODEBUDDY_NATIVE")
	if executable == "" {
		t.Skip("requires fixed native CLI in OS sandbox")
	}
	for _, kind := range []string{"cancel", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			started, closed := make(chan struct{}, 1), make(chan struct{}, 1)
			srv, ca := codeBuddyTLSFixture(t, "127.0.0.1", false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				started <- struct{}{}
				defer func() { closed <- struct{}{} }()
				<-r.Context().Done()
			}))
			root := t.TempDir()
			a := NewCodeBuddy(executable)
			a.StateRoot = filepath.Join(root, "state")
			a.Environment = map[string]string{"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "HTTP_PROXY": "", "HTTPS_PROXY": "", "http_proxy": "", "https_proxy": "", "NO_PROXY": "", "no_proxy": "", "SSL_CERT_FILE": "", "NODE_EXTRA_CA_CERTS": ""}
			req := codeBuddyRequest()
			req.Environment["CODEBUDDY_BASE_URL"] = srv.URL + "/v1"
			req.Environment["SSL_CERT_FILE"] = ca
			req.EnvironmentAllowlist = append(req.EnvironmentAllowlist, "SSL_CERT_FILE")
			req.WorkingDirectory = root
			req.Timeout = 6 * time.Second
			execution, err := a.Start(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			relay := execution.(*cleanupExecution).Execution.(*codeBuddyRelayExecution).relay
			done := make(chan struct{})
			go func() {
				for range execution.Events() {
				}
				close(done)
			}()
			select {
			case <-started:
			case <-time.After(8 * time.Second):
				t.Fatal("native model request did not start")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			if kind == "cancel" {
				if err := execution.Cancel(ctx); err != nil {
					t.Fatal(err)
				}
			}
			result, err := execution.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			<-done
			want := runtime.StatusTimedOut
			if kind == "cancel" {
				want = runtime.StatusCancelled
			}
			if result.Status != want {
				t.Fatalf("status=%s want=%s", result.Status, want)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("native upstream not cancelled")
			}
			if c, err := net.DialTimeout("tcp", relay.host, 100*time.Millisecond); err == nil {
				c.Close()
				t.Fatal("native relay listener leaked")
			}
			entries, err := os.ReadDir(a.StateRoot)
			if err != nil || len(entries) != 0 {
				t.Fatal("native state leaked")
			}
			t.Logf("native_status=%s upstream_cancelled=true listener_closed=true state_cleaned=true", result.Status)
		})
	}
}
