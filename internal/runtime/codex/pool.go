package codex

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	rt "openfox/internal/runtime"
)

const maxPersistentHosts = 16

type hostPool struct {
	mu    sync.Mutex
	hosts map[[32]byte]*runtimeHost
}
type runtimeHost struct {
	addr, token string
	homeDir     string
	birth       time.Time
	busy        bool
	timer       *time.Timer
	parentPipe  io.WriteCloser
	done        chan struct{}
}

// Home paths are random per preparation. Match the actual account and all
// process-wide inputs instead. Per-turn tools and artifact grants are sent
// only on that turn's separate authenticated connection and ephemeral thread.
func poolIdentity(req rt.RunRequest, spec rt.ProcessSpec, p preparedProcess) [32]byte {
	env := make([]string, 0, len(spec.Environment))
	for _, v := range spec.Environment {
		if !strings.HasPrefix(v, "CODEX_HOME=") {
			env = append(env, v)
		}
	}
	data, _ := json.Marshal([]any{req.ProcessScope, spec.Executable, spec.Args, spec.Directory, env, req.Skills, req.ApprovalMode, p.home.sourceAuth, p.home.sourceSum})
	return sha256.Sum256(data)
}

// Closing the inherited pipe terminates the host even when the supervisor is
// killed. The shell evaluates a fixed program; executable and args are positional.
const guardHost = `exec 3<&0
"$@" </dev/null 3<&- &
server=$!
(while IFS= read -r line; do :; done; kill -TERM "$server" 2>/dev/null; sleep 5; kill -KILL "$server" 2>/dev/null) <&3 &
guard=$!
exec 3<&-
wait "$server"
result=$?
kill "$guard" 2>/dev/null
wait "$guard" 2>/dev/null
exit "$result"`

func launchHost(ctx context.Context, spec rt.ProcessSpec) (*runtimeHost, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(secret[:])
	sum := sha256.Sum256([]byte(token))
	args := append([]string{"-c", guardHost, "openfox-session", spec.Executable}, spec.Args...)
	args = append(args, "--listen", "ws://127.0.0.1:0", "--ws-auth", "capability-token", "--ws-token-sha256", hex.EncodeToString(sum[:]))
	cmd := exec.Command("/bin/sh", args...)
	cmd.Dir, cmd.Env = spec.Directory, spec.Environment
	cmd.WaitDelay = 2 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	cmd.Stderr = writer
	if err := cmd.Start(); err != nil {
		stdin.Close()
		reader.Close()
		writer.Close()
		return nil, err
	}
	writer.Close()
	h := &runtimeHost{token: token, birth: time.Now(), busy: true, parentPipe: stdin, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); reader.Close(); close(h.done) }()
	ready := make(chan string, 1)
	go func() {
		defer reader.Close()
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if endpoint, ok := strings.CutPrefix(line, "listening on: "); ok {
				u, err := url.Parse(endpoint)
				if err == nil && u.Scheme == "ws" && u.Hostname() == "127.0.0.1" && u.Port() != "" && u.Path == "" && u.User == nil {
					ready <- endpoint
					// Subsequent stderr can include account diagnostics. Drain directly.
					_, _ = io.Copy(io.Discard, reader)
					return
				}
			}
		}
	}()
	select {
	case h.addr = <-ready:
		return h, nil
	case <-h.done:
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
	}
	stdin.Close()
	<-h.done
	return nil, errors.New("codex: authenticated app server failed to listen")
}

func (a *Adapter) startPooled(ctx context.Context, req rt.RunRequest, spec rt.ProcessSpec, p preparedProcess) (rt.Execution, bool, error) {
	if runtime.GOOS == "windows" {
		return nil, false, nil
	}
	key := poolIdentity(req, spec, p)
	pool := &a.pool
	pool.mu.Lock()
	if pool.hosts == nil {
		pool.hosts = make(map[[32]byte]*runtimeHost)
	}
	h := pool.hosts[key]
	if h != nil && h.busy {
		pool.mu.Unlock()
		return nil, false, nil
	}
	if h != nil && time.Since(h.birth) >= req.SessionMaxAge {
		pool.remove(key, h)
		h = nil
	}
	if h == nil && len(pool.hosts) >= maxPersistentHosts {
		pool.mu.Unlock()
		return nil, false, nil
	}
	if h != nil {
		h.timer.Stop()
		h.busy = true
		p.home.remove()
	} else {
		var err error
		h, err = launchHost(ctx, spec)
		if err != nil {
			pool.mu.Unlock()
			return nil, false, nil
		} // older/unsupported CLI still uses its original stdio path
		h.homeDir = environmentValue(spec.Environment, "CODEX_HOME")
		pool.hosts[key] = h
		liveHomes.Store(p.home.dir, true)
		go func() {
			<-h.done
			pool.mu.Lock()
			pool.remove(key, h)
			pool.mu.Unlock()
			_, _ = p.home.finish()
			liveHomes.Delete(p.home.dir)
		}()
	}
	pool.mu.Unlock()
	dial, cancel := context.WithTimeout(ctx, 5*time.Second)
	conn, _, err := websocket.Dial(dial, h.addr, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + h.token}}, HTTPClient: &http.Client{Transport: localSocketTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}})
	cancel()
	retire := func() { pool.mu.Lock(); pool.remove(key, h); pool.mu.Unlock(); <-h.done }
	if err != nil {
		retire()
		return nil, true, errors.New("codex: authenticated local session unavailable")
	}
	conn.SetReadLimit(int64(spec.MaxLineBytes))
	read, write := io.Pipe()
	disconnected := make(chan struct{})
	var closeOnce sync.Once
	disconnect := func() { closeOnce.Do(func() { write.Close(); conn.CloseNow(); close(disconnected) }) }
	go func() {
		defer disconnect()
		for {
			typ, data, err := conn.Read(context.Background())
			if err != nil {
				write.CloseWithError(errors.New("codex: session connection closed"))
				return
			}
			if typ != websocket.MessageText || !json.Valid(data) {
				write.CloseWithError(rt.ErrProtocol)
				return
			}
			if _, err = write.Write(append(data, '\n')); err != nil {
				return
			}
		}
	}()
	transport := &rt.SessionTransport{Output: read, Send: func(data []byte) error {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return conn.Write(c, websocket.MessageText, data)
	}, Close: disconnect, Stop: func() { retire(); disconnect() }, Wait: func() error { <-disconnected; return nil }}
	execution, err := rt.StartConnectedSession(ctx, spec, transport)
	if err != nil {
		disconnect()
		retire()
		return nil, true, err
	}
	wrapped := &homeExecution{Execution: execution, done: make(chan struct{})}
	go func() {
		defer close(wrapped.done)
		result, _ := execution.Wait(context.Background())
		if result.SessionID != "" {
			_ = a.copySessionToExport(h.homeDir, result.SessionID)
		}
		if result.Status != rt.StatusSucceeded {
			retire()
			return
		}
		pool.mu.Lock()
		defer pool.mu.Unlock()
		if pool.hosts[key] != h {
			return
		}
		h.busy = false
		keep := min(req.SessionIdleTTL, time.Until(h.birth.Add(req.SessionMaxAge)))
		// Capture timer identity so a callback queued before reuse cannot remove the new lease.
		var timer *time.Timer
		timer = time.AfterFunc(keep, func() {
			pool.mu.Lock()
			defer pool.mu.Unlock()
			if !h.busy && h.timer == timer {
				pool.remove(key, h)
			}
		})
		h.timer = timer
	}()
	return wrapped, true, nil
}

func environmentValue(environment []string, name string) string {
	prefix := name + "="
	for _, value := range environment {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return ""
}

var localSocketTransport = &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}

func (p *hostPool) remove(key [32]byte, h *runtimeHost) {
	if p.hosts[key] != h {
		return
	}
	delete(p.hosts, key)
	if h.timer != nil {
		h.timer.Stop()
	}
	h.parentPipe.Close()
}

func (a *Adapter) CloseSessions() {
	pool := &a.pool
	pool.mu.Lock()
	var hosts []*runtimeHost
	for k, h := range pool.hosts {
		hosts = append(hosts, h)
		pool.remove(k, h)
	}
	pool.mu.Unlock()
	for _, h := range hosts {
		<-h.done
	}
}

var liveHomes sync.Map
