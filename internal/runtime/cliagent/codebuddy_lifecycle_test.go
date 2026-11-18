package cliagent

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"openfox/internal/runtime"
)

func TestCodeBuddyRelayProcessLifecycle(t *testing.T) {
	for _, kind := range []string{"cancel", "timeout", "native-early-exit", "disconnect", "fatal-before-native-success", "normal"} {
		t.Run(kind, func(t *testing.T) {
			started, closed := make(chan struct{}, 1), make(chan struct{}, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				started <- struct{}{}
				defer func() { closed <- struct{}{} }()
				switch kind {
				case "fatal-before-native-success":
					w.Header().Set("Location", "http://127.0.0.1:9/else")
					w.WriteHeader(307)
				case "normal":
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{}`)
				case "disconnect":
					conn, _, _ := w.(http.Hijacker).Hijack()
					io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 1024\r\n\r\nshort")
					conn.Close()
				default:
					<-r.Context().Done()
				}
			}))
			defer srv.Close()
			a := NewCodeBuddy("")
			a.proto.probe = func(context.Context, *Adapter, string) (string, error) { return "2.184.0", nil }
			f := newFixture(t, a)
			sc := scenario{CodeBuddyRequest: true, Stdout: []string{`{"type":"result","subtype":"success","result":"native-success"}`}}
			if kind == "native-early-exit" {
				sc.CodeBuddyEarlyExit = true
				sc.Stdout = nil
			}
			f.set(sc)
			req := codeBuddyRequest()
			req.Environment["CODEBUDDY_BASE_URL"] = srv.URL
			req.WorkingDirectory = f.dir
			req.Timeout = 3 * time.Second
			if kind == "timeout" {
				req.Timeout = 250 * time.Millisecond
			}
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
			case <-time.After(2 * time.Second):
				t.Fatal("model did not start")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
			want := runtime.StatusFailed
			switch kind {
			case "cancel":
				want = runtime.StatusCancelled
			case "timeout":
				want = runtime.StatusTimedOut
			case "normal":
				want = runtime.StatusSucceeded
			}
			if result.Status != want {
				t.Fatalf("status=%s want=%s error=%s", result.Status, want, result.Error)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("upstream did not close")
			}
			if c, err := net.DialTimeout("tcp", relay.host, 100*time.Millisecond); err == nil {
				c.Close()
				t.Fatal("listener survived Run")
			}
			files, err := os.ReadDir(a.StateRoot)
			if err != nil || len(files) != 0 {
				t.Fatal("native state survived Run")
			}
		})
	}
}

func TestCodeBuddyRelayStartFailureCleansState(t *testing.T) {
	a := NewCodeBuddy("")
	a.proto.probe = func(context.Context, *Adapter, string) (string, error) { return "2.184.0", nil }
	f := newFixture(t, a)
	f.set(scenario{})
	req := codeBuddyRequest()
	req.WorkingDirectory = filepath.Join(f.dir, "missing-workdir")
	if _, err := a.Start(context.Background(), req); err == nil {
		t.Fatal("missing workdir accepted")
	}
	files, err := os.ReadDir(a.StateRoot)
	if err != nil || len(files) != 0 {
		t.Fatal("start failure leaked state")
	}
}
