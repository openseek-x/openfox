package hermes

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"openfox/internal/platform/safehttp"
	"openfox/internal/runtimepolicy"
)

const maxModelRequestBytes = 2 << 20

// modelProxy is owned by one Run. It is not a general proxy and never gives the
// upstream credential to Hermes. Client injection is package-private for TLS tests.
type modelProxy struct {
	url, token, target, model, key string
	ctx                            context.Context
	cancel                         context.CancelFunc
	server                         *http.Server
	listener                       net.Listener
	client                         *http.Client
	active                         chan struct{}
	mu                             sync.Mutex
	remaining                      int
	failure                        error
	fatal                          error
	closeOnce                      sync.Once
}

func newModelProxy(ctx context.Context, cfg runtimepolicy.HermesModelConfig, maxOutput int, client *http.Client) (*modelProxy, error) {
	if _, err := safehttp.CheckURL(cfg.BaseURL); err != nil {
		return nil, err
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = safehttp.Client(safehttp.Options{})
	} else {
		copy := *client
		client = &copy
	}
	// safehttp's other callers retain their existing redirect behavior.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Jar = nil
	child, cancel := context.WithCancel(ctx)
	p := &modelProxy{url: "http://" + listener.Addr().String() + "/v1", token: hex.EncodeToString(random),
		target: cfg.BaseURL + "/chat/completions", model: cfg.Model, key: cfg.APIKey,
		ctx: child, cancel: cancel, listener: listener, client: client, active: make(chan struct{}, 2), remaining: maxOutput}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10,
		BaseContext: func(net.Listener) context.Context { return child }}
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}

func (p *modelProxy) Close() {
	p.closeOnce.Do(func() {
		p.cancel()
		_ = p.server.Close()
		p.client.CloseIdleConnections()
	})
}

func (p *modelProxy) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failure
}

// Request diagnostics may recover through native retries. Only a Run-wide
// limit/policy failure makes a later ACP end_turn unsuccessful.
func (p *modelProxy) FatalErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fatal
}

func (p *modelProxy) fail(message string) {
	p.mu.Lock()
	if p.failure == nil {
		p.failure = errors.New(message)
	}
	p.mu.Unlock()
}

func (p *modelProxy) take(n int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n > p.remaining {
		p.failure = errors.New("Hermes model response budget exceeded")
		p.fatal = p.failure
		return false
	}
	p.remaining -= n
	return true
}

func (p *modelProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Do not permit absolute-form requests, escaped paths, alternate authorities,
	// duplicate authentication headers, catalog discovery or arbitrary HTTP methods.
	if r.Method != http.MethodPost || r.RequestURI != "/v1/chat/completions" || r.URL.IsAbs() || r.URL.RawPath != "" {
		http.Error(w, "Hermes model route is not supported", http.StatusForbidden)
		return
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte("Bearer "+p.token)) != 1 {
		http.Error(w, "Hermes local authentication failed", http.StatusUnauthorized)
		return
	}
	select {
	case p.active <- struct{}{}:
		defer func() { <-p.active }()
	default:
		http.Error(w, "Hermes model request capacity exceeded", http.StatusTooManyRequests)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxModelRequestBytes))
	if err != nil {
		http.Error(w, "Hermes model request exceeds its limit", http.StatusRequestEntityTooLarge)
		return
	}
	if !validModelBody(data, p.model) {
		http.Error(w, "Hermes model request does not match the Run", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithCancel(p.ctx)
	stop := context.AfterFunc(r.Context(), cancel)
	defer func() { stop(); cancel() }()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.target, bytes.NewReader(data))
	if err != nil {
		p.fail("Hermes model request could not be constructed")
		http.Error(w, "Hermes model request failed", http.StatusBadGateway)
		return
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	res, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			p.fail("Hermes model connection failed; check public DNS, HTTPS trust and network access")
		}
		http.Error(w, "Hermes model connection failed", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// Neither provider error bodies nor Location/Set-Cookie enter native logs.
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			p.fail("Hermes model redirects are not supported")
		} else {
			p.fail("Hermes model rejected the request; verify credentials, model and declared context capacity")
		}
		http.Error(w, "Hermes model request was rejected", res.StatusCode)
		return
	}
	contentType := res.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") && !strings.HasPrefix(contentType, "application/json") {
		p.fail("Hermes model response has an unsupported content type")
		http.Error(w, "Hermes model response is invalid", http.StatusBadGateway)
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
			if !p.take(n) {
				p.cancel()
				return
			}
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
		if readErr != nil {
			if readErr != io.EOF && ctx.Err() == nil {
				p.fail("Hermes model stream ended unexpectedly")
			}
			return
		}
	}
}

func validModelBody(data []byte, model string) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	matched := false
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
