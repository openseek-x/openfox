package cliagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"openfox/internal/runtime"
)

const codeBuddyMaxRequestBytes = 2 << 20

// One relay belongs to one Run and one frozen target/model. The native process
// receives only the local token; the upstream key stays in this Go process.
type codeBuddyModelRelay struct {
	url, host, token, target, model, key string
	ctx                                  context.Context
	cancel                               context.CancelFunc
	server                               *http.Server
	client                               *http.Client
	mu                                   sync.Mutex
	active, remaining                    int
	closed                               bool
	fatal                                error
	fail                                 func(error)
	timer                                *time.Timer
	requests                             sync.WaitGroup
	closeOnce                            sync.Once
}

func newCodeBuddyModelRelay(ctx context.Context, env map[string]string, model string, limit int) (*codeBuddyModelRelay, error) {
	client, err := codeBuddyModelClient(env)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", runtime.ErrInvalidRequest, err)
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("%w: CodeBuddy local credential unavailable", runtime.ErrUnavailable)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("%w: CodeBuddy local model listener unavailable", runtime.ErrUnavailable)
	}
	child, cancel := context.WithCancel(ctx)
	p := &codeBuddyModelRelay{host: listener.Addr().String(), token: hex.EncodeToString(random[:]),
		target: strings.TrimRight(env["CODEBUDDY_BASE_URL"], "/") + "/chat/completions", model: model, key: env["CODEBUDDY_API_KEY"],
		ctx: child, cancel: cancel, client: client, remaining: limit}
	p.url = "http://" + p.host + "/v1"
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, MaxHeaderBytes: 64 << 10,
		BaseContext: func(net.Listener) context.Context { return child }}
	go func() {
		if err := p.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			p.setFatal("CodeBuddy local model listener failed")
		}
	}()
	return p, nil
}

func (p *codeBuddyModelRelay) Close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		if p.timer != nil {
			p.timer.Stop()
		}
		p.mu.Unlock()
		p.cancel()
		_ = p.server.Close()
		p.requests.Wait()
		p.client.CloseIdleConnections()
	})
}

func (p *codeBuddyModelRelay) FatalErr() error { p.mu.Lock(); defer p.mu.Unlock(); return p.fatal }

func (p *codeBuddyModelRelay) onFailure(fail func(error)) {
	p.mu.Lock()
	p.fail = fail
	err := p.fatal
	p.mu.Unlock()
	if err != nil {
		fail(err)
	}
}

func (p *codeBuddyModelRelay) setFatal(message string) {
	p.mu.Lock()
	if p.fatal != nil || p.closed || p.ctx.Err() != nil {
		p.mu.Unlock()
		return
	}
	p.fatal = fmt.Errorf("%w: %s", runtime.ErrProtocol, message)
	err, fail := p.fatal, p.fail
	p.mu.Unlock()
	if fail != nil {
		fail(err)
	}
}

func (p *codeBuddyModelRelay) reject(w http.ResponseWriter, status int, message string) {
	p.setFatal(message)
	http.Error(w, message, status)
}

func (p *codeBuddyModelRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	if p.closed || p.fatal != nil {
		p.mu.Unlock()
		http.Error(w, "CodeBuddy model Run ended", http.StatusServiceUnavailable)
		return
	}
	if p.active >= 2 {
		p.mu.Unlock()
		p.reject(w, http.StatusTooManyRequests, "CodeBuddy model request capacity exceeded")
		return
	}
	p.active++
	p.requests.Add(1)
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.active--; p.mu.Unlock(); p.requests.Done() }()
	if r.Method != http.MethodPost || r.Host != p.host || r.RequestURI != "/v1/chat/completions" || r.URL.IsAbs() || r.URL.RawPath != "" {
		p.reject(w, http.StatusForbidden, "CodeBuddy local model route is not supported")
		return
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte("Bearer "+p.token)) != 1 {
		p.reject(w, http.StatusUnauthorized, "CodeBuddy local model authentication failed")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, codeBuddyMaxRequestBytes))
	if err != nil {
		p.reject(w, http.StatusRequestEntityTooLarge, "CodeBuddy model request exceeds its limit")
		return
	}
	if !validCodeBuddyModelBody(data, p.model) {
		p.reject(w, http.StatusBadRequest, "CodeBuddy model request does not match the Run")
		return
	}
	ctx, cancel := context.WithCancel(p.ctx)
	stop := context.AfterFunc(r.Context(), cancel)
	defer func() { stop(); cancel() }()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.target, bytes.NewReader(data))
	if err != nil {
		p.reject(w, http.StatusBadGateway, "CodeBuddy model request could not be constructed")
		return
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	res, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			p.setFatal("CodeBuddy model connection failed; verify TLS trust, hostname and proxy configuration")
		}
		http.Error(w, "CodeBuddy model connection failed", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			p.reject(w, http.StatusBadGateway, "CodeBuddy model redirects are not supported")
		} else {
			// Provider bodies and response headers (including Location/Set-Cookie)
			// are deliberately excluded from native diagnostics.
			http.Error(w, "CodeBuddy model request was rejected", res.StatusCode)
		}
		return
	}
	contentType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || (contentType != "text/event-stream" && contentType != "application/json") {
		p.reject(w, http.StatusBadGateway, "CodeBuddy model response has an unsupported content type")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	controller := http.NewResponseController(w)
	buffer := make([]byte, 16<<10)
	for {
		n, readErr := res.Body.Read(buffer)
		if n > 0 {
			p.mu.Lock()
			fits := n <= p.remaining
			if fits {
				p.remaining -= n
			}
			p.mu.Unlock()
			if !fits {
				p.setFatal("CodeBuddy model response budget exceeded")
				return
			}
			if _, err = w.Write(buffer[:n]); err != nil {
				p.setFatal("CodeBuddy local model stream disconnected")
				return
			}
			if err = controller.Flush(); err != nil {
				p.setFatal("CodeBuddy local model stream disconnected")
				return
			}
		}
		if readErr != nil {
			if readErr != io.EOF && ctx.Err() == nil {
				p.setFatal("CodeBuddy model stream ended unexpectedly")
			}
			return
		}
	}
}

func validCodeBuddyModelBody(data []byte, model string) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return false
	}
	seen, matched := map[string]bool{}, false
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return false
		}
		if name == "model" {
			var requested string
			matched = json.Unmarshal(value, &requested) == nil && requested == model
		}
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || !matched {
		return false
	}
	var extra any
	return d.Decode(&extra) == io.EOF
}

// The embedded session keeps all existing usage/step/session-id contracts. Fatal
// relay errors reach the process supervisor even if native later prints success.
type codeBuddyRelaySession struct {
	*session
	relay   *codeBuddyModelRelay
	timeout time.Duration
}

func (s *codeBuddyRelaySession) Begin(send runtime.Sender, fail func(error)) error {
	// The process supervisor starts its Run timer before Begin. Transport expiry
	// must not turn a Run timeout into an earlier native connection-error result.
	s.relay.mu.Lock()
	if !s.relay.closed {
		s.relay.timer = time.AfterFunc(s.timeout, s.relay.Close)
	}
	s.relay.mu.Unlock()
	s.relay.onFailure(fail)
	return s.session.Begin(send, fail)
}

func (s *codeBuddyRelaySession) HandleLine(line []byte, send runtime.Sender) ([]runtime.Event, bool, error) {
	if err := s.relay.FatalErr(); err != nil {
		return nil, false, err
	}
	events, done, err := s.session.HandleLine(line, send)
	if done {
		s.relay.Close()
		if fatal := s.relay.FatalErr(); fatal != nil {
			return nil, false, fatal
		}
	}
	return events, done, err
}

func (s *codeBuddyRelaySession) Interrupt(send runtime.Sender) bool {
	s.relay.Close()
	return s.session.Interrupt(send)
}
func (s *codeBuddyRelaySession) Finalize(stopped string) []runtime.Event {
	s.relay.Close()
	if stopped == "" {
		if err := s.relay.FatalErr(); err != nil {
			return s.session.fail(err.Error())
		}
	}
	return s.session.Finalize(stopped)
}

type codeBuddyRelayExecution struct {
	runtime.Execution
	relay *codeBuddyModelRelay
}

func (e *codeBuddyRelayExecution) Cancel(ctx context.Context) error {
	e.relay.cancel()
	err := e.Execution.Cancel(ctx)
	e.relay.Close()
	return err
}

func codeBuddyLocalEnvironment(env map[string]string, relay *codeBuddyModelRelay) {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS", "NODE_TLS_REJECT_UNAUTHORIZED", "NODE_OPTIONS"} {
		delete(env, key)
	}
	env["NO_PROXY"], env["no_proxy"] = "127.0.0.1,localhost", "127.0.0.1,localhost"
	env["CODEBUDDY_BASE_URL"], env["CODEBUDDY_API_KEY"], env["CODEBUDDY_MODEL"] = relay.url, relay.token, relay.model
}
