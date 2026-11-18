package cliagent

import (
	"bufio"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"openfox/internal/platform/safehttp"
)

const geminiOrigin = "https://generativelanguage.googleapis.com"
const geminiMaxBody = 2 << 20

type geminiRelay struct {
	url, host, token, route, target, key, root string
	ctx                                        context.Context
	cancel                                     context.CancelFunc
	server                                     *http.Server
	client                                     *http.Client
	mu                                         sync.Mutex
	remaining                                  int
	fatal                                      error
	fail                                       func(error)
	active                                     chan struct{}
	requests                                   sync.WaitGroup
	closed                                     bool
	once                                       sync.Once
}

// Target/client injection is package-private, exclusively for actual local TLS
// protocol tests. The production caller always uses the fixed Google origin.
func newGeminiRelay(ctx context.Context, model, key, root string, maxOutput int, target string, client *http.Client) (*geminiRelay, error) {
	route := "/v1beta/models/" + model + ":streamGenerateContent?alt=sse"
	if client == nil {
		client = safehttp.Client(safehttp.Options{})
		target = geminiOrigin
	}
	if _, err := safehttp.CheckURL(target); err != nil {
		return nil, err
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.Jar = nil
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		listener.Close()
		return nil, err
	}
	child, cancel := context.WithCancel(ctx)
	if maxOutput <= 0 {
		maxOutput = 8 << 20
	}
	p := &geminiRelay{host: listener.Addr().String(), token: hex.EncodeToString(token[:]), route: route, target: target + route, key: key, root: root, ctx: child, cancel: cancel, client: &c, remaining: maxOutput, active: make(chan struct{}, 2)}
	p.url = "http://" + p.host
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return child }}
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}
func (p *geminiRelay) Close() {
	p.once.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		p.cancel()
		_ = p.server.Close()
		p.requests.Wait()
		p.client.CloseIdleConnections()
	})
}
func (p *geminiRelay) FatalErr() error { p.mu.Lock(); defer p.mu.Unlock(); return p.fatal }
func (p *geminiRelay) onFailure(f func(error)) {
	p.mu.Lock()
	p.fail = f
	e := p.fatal
	p.mu.Unlock()
	if e != nil {
		f(e)
	}
}
func (p *geminiRelay) reject(message string) {
	p.mu.Lock()
	if p.fatal != nil || p.closed || p.ctx.Err() != nil {
		p.mu.Unlock()
		return
	}
	p.fatal = fmt.Errorf("Gemini model protocol: %s", message)
	e, f := p.fatal, p.fail
	p.mu.Unlock()
	if f != nil {
		f(e)
	}
}
func (p *geminiRelay) take(n int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n > p.remaining {
		return false
	}
	p.remaining -= n
	return true
}
func (p *geminiRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.Host != p.host || r.RequestURI != p.route || r.URL.IsAbs() || r.URL.RawPath != "" {
		http.Error(w, "unsupported Gemini model route", 403)
		return
	}
	auth := r.Header.Values("X-Goog-Api-Key")
	if len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte(p.token)) != 1 {
		http.Error(w, "invalid Gemini local credential", 401)
		return
	}
	p.mu.Lock()
	if p.closed || p.fatal != nil {
		p.mu.Unlock()
		http.Error(w, "Gemini Run ended", 503)
		return
	}
	p.requests.Add(1)
	p.mu.Unlock()
	defer p.requests.Done()
	select {
	case p.active <- struct{}{}:
		defer func() { <-p.active }()
	default:
		http.Error(w, "Gemini request capacity exceeded", 429)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, geminiMaxBody))
	if err != nil {
		p.reject("request exceeds budget")
		http.Error(w, "Gemini request exceeds budget", 413)
		return
	}
	if !geminiRequest(raw) {
		p.reject("unsupported model request")
		http.Error(w, "unsupported Gemini request", 400)
		return
	}
	ctx, cancel := context.WithCancel(p.ctx)
	stop := context.AfterFunc(r.Context(), cancel)
	defer func() { stop(); cancel() }()
	req, err := http.NewRequestWithContext(ctx, "POST", p.target, bytes.NewReader(raw))
	if err != nil {
		p.reject("request construction failed")
		http.Error(w, "Gemini request failed", 502)
		return
	}
	req.Header.Set("X-Goog-Api-Key", p.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	response, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			p.reject("verified TLS connection failed")
		}
		http.Error(w, "Gemini connection failed", 502)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		if response.StatusCode == http.StatusUnauthorized {
			p.reject("Google Gemini API key was rejected (HTTP 401 authentication)")
			http.Error(w, "Gemini authentication rejected", http.StatusUnauthorized)
			return
		}
		if response.StatusCode == http.StatusForbidden {
			p.reject("Google Gemini access forbidden (HTTP 403); verify key permissions, API activation and model access")
			http.Error(w, "Gemini access forbidden", http.StatusForbidden)
			return
		}
		if response.StatusCode >= 200 && response.StatusCode < 400 {
			p.reject("unexpected model status or redirect")
			http.Error(w, "Gemini redirect refused", 502)
			return
		}
		// A transient native retry may recover; do not make ordinary 429/5xx sticky.
		if value := response.Header.Get("Retry-After"); value != "" {
			if seconds, e := strconv.ParseUint(value, 10, 31); e == nil && seconds > 0 {
				w.Header().Set("Retry-After", value)
			}
		}
		http.Error(w, "Gemini model request rejected", response.StatusCode)
		return
	}
	typ, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || typ != "text/event-stream" {
		p.reject("expected SSE response")
		http.Error(w, "Gemini response format refused", 502)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	state := geminiStreamState{}
	err = geminiSSE(&geminiBudgetReader{reader: response.Body, relay: p}, func(data []byte) error {
		if err := state.accept(p.root, data); err != nil {
			return err
		}
		var compact bytes.Buffer
		if json.Compact(&compact, data) != nil {
			return fmt.Errorf("invalid response event")
		}
		// Google SDK consumes one JSON data line. Canonical encoding prevents its
		// line regex and this complete-event parser from interpreting different calls.
		if _, e := fmt.Fprintf(w, "data: %s\n\n", geminiSSEJSON(compact.Bytes())); e != nil {
			return e
		}
		return http.NewResponseController(w).Flush()
	})
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		p.reject("invalid or interrupted model stream")
		return
	}
	if !state.Finished || !state.Content {
		p.reject("model stream ended without content and STOP")
		return
	}
}
func geminiRequest(raw []byte) bool {
	if !geminiJSONObject(raw) {
		return false
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	var contents []json.RawMessage
	if json.Unmarshal(body["contents"], &contents) != nil || len(contents) == 0 || len(contents) > 1000 {
		return false
	}
	if v, ok := body["toolConfig"]; ok {
		var cfg map[string]json.RawMessage
		if json.Unmarshal(v, &cfg) != nil {
			return false
		}
		if v, ok = cfg["functionCallingConfig"]; ok {
			var function map[string]json.RawMessage
			if json.Unmarshal(v, &function) != nil {
				return false
			}
			if _, present := function["streamFunctionCallArguments"]; present {
				return false
			}
		}
	}
	var tools []map[string]json.RawMessage
	if v, ok := body["tools"]; ok {
		if json.Unmarshal(v, &tools) != nil || len(tools) > 1 {
			return false
		}
	}
	for _, tool := range tools {
		if len(tool) != 1 {
			return false
		}
		var decl []map[string]json.RawMessage
		if json.Unmarshal(tool["functionDeclarations"], &decl) != nil || len(decl) > 4 {
			return false
		}
		seen := map[string]bool{}
		for _, d := range decl {
			var name string
			if json.Unmarshal(d["name"], &name) != nil || seen[name] || !geminiReadTool(name) {
				return false
			}
			seen[name] = true
		}
	}
	return true
}
func geminiReadTool(name string) bool {
	return name == "read_file" || name == "list_directory" || name == "glob" || name == "grep_search"
}
func geminiSSE(r io.Reader, accept func([]byte) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 16<<10), 1<<20)
	var event bytes.Buffer
	dispatch := func() error {
		if event.Len() == 0 {
			return nil
		}
		data := bytes.TrimSuffix(event.Bytes(), []byte{'\n'})
		if err := accept(data); err != nil {
			return err
		}
		event.Reset()
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if e := dispatch(); e != nil {
				return e
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		key, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		if key != "data" {
			return fmt.Errorf("unsupported Gemini SSE field")
		}
		if event.Len()+len(value) > 1<<20 {
			return fmt.Errorf("Gemini SSE event exceeds budget")
		}
		event.WriteString(value)
		event.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if event.Len() != 0 {
		return fmt.Errorf("Gemini SSE ended mid event")
	}
	return nil
}

// Count raw bytes as well as JSON: comments and framing cannot bypass the Run budget.
type geminiBudgetReader struct {
	reader io.Reader
	relay  *geminiRelay
}

func (b *geminiBudgetReader) Read(p []byte) (int, error) {
	n, e := b.reader.Read(p)
	if n > 0 && !b.relay.take(n) {
		return 0, fmt.Errorf("Gemini response budget exceeded")
	}
	return n, e
}

func geminiSSEJSON(data []byte) []byte {
	data = bytes.ReplaceAll(data, []byte("\u2028"), []byte(`\u2028`))
	return bytes.ReplaceAll(data, []byte("\u2029"), []byte(`\u2029`))
}
