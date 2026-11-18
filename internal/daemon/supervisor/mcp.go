package supervisor

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"openfox/internal/mcpconnect"
	"openfox/internal/platform/safehttp"
	"openfox/internal/runtime/mcpconfig"
)

var errAgentMCP = errors.New("one or more configured MCP servers are invalid or unavailable for this run")

type runMCP struct {
	servers mcpconfig.Servers
	secrets []string
	stop    []func()
}

func (m *runMCP) close() {
	for _, stop := range m.stop {
		stop()
	}
}

func prepareMCP(rc RunContext, deviceID string, loc mcpconfig.Location) (result *runMCP, err error) {
	m := &runMCP{servers: mcpconfig.Servers{}}
	defer func() {
		if err != nil {
			m.close()
		}
	}()
	// An owner can combine up to 32 shared and 32 device-scoped bindings.
	if len(rc.MCPServers) > 64 {
		return nil, errAgentMCP
	}
	seen := map[string]bool{}
	for _, s := range rc.MCPServers {
		key := strings.ToLower(s.Name)
		if rc.Runtime == "pi" {
			key = strings.ReplaceAll(key, "-", "_")
		}
		if !mcpconnect.ValidName(s.Name) || key == "openfox" || seen[key] {
			return nil, errAgentMCP
		}
		seen[key] = true
		switch rc.Runtime {
		case "codex", "claude-code", "opencode", "pi":
		default:
			return nil, errAgentMCP
		}
		switch s.Scope {
		case mcpconnect.Public:
			if s.Transport != "HTTP" {
				return nil, errAgentMCP
			}
		case mcpconnect.DeviceOwner:
			if deviceID == "" || s.DeviceID != deviceID || rc.MCPDeviceOwnerID == "" || s.OwnerUserID != rc.MCPDeviceOwnerID || rc.MCPActorUserID != s.OwnerUserID {
				return nil, errAgentMCP
			}
		default:
			return nil, errAgentMCP
		}
		if s.Transport == "INHERITED" {
			entry, e := mcpconfig.Inherited(rc.Runtime, s.Name, loc)
			if e != nil {
				return nil, errAgentMCP
			}
			m.servers[s.Name] = entry
			continue
		}
		conf, e := (mcpconnect.Config{Name: s.Name, CatalogID: s.CatalogID, Transport: s.Transport, Scope: s.Scope, URL: s.URL, Headers: s.Headers, Command: s.Command, Args: s.Args, Env: s.Env}).Normalize()
		if e != nil {
			return nil, errAgentMCP
		}
		if conf.Command == "openfox:provider-cli" {
			if s.Scope != mcpconnect.DeviceOwner || len(conf.Env) != 0 || len(conf.Args) != 1 {
				return nil, errAgentMCP
			}
			switch conf.Args[0] {
			case "feishu", "cnb", "apifox":
			default:
				return nil, errAgentMCP
			}
			executable, e := os.Executable()
			if e != nil || loc.Home == "" {
				return nil, errAgentMCP
			}
			conf.Args = []string{"mcp", "provider-cli", conf.Args[0]}
			conf.Command = executable
			conf.Env = map[string]string{"HOME": loc.Home, "PATH": os.Getenv("PATH")}
		}
		var endpoint string
		if conf.Transport == "http" {
			client := mcpconnect.PublicClient(safehttp.Options{})
			if conf.Scope == mcpconnect.DeviceOwner {
				client = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 15 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			}
			url, stop, e := startMCPProxy(conf, client)
			if e != nil {
				return nil, errAgentMCP
			}
			m.stop = append(m.stop, stop)
			endpoint = url
			m.secrets = append(m.secrets, mcpconfig.SensitiveValues(mcpconfig.Servers{s.Name: {"headers": conf.Headers, "url": conf.URL}})...)
		}
		m.servers[s.Name] = mcpconfig.Native(rc.Runtime, conf.Command, conf.Args, conf.Env, endpoint)
	}
	m.secrets = append(m.secrets, mcpconfig.SensitiveValues(m.servers)...)
	return m, nil
}

const mcpBodyLimit = 8 << 20

// Every upstream is fixed before the runtime starts. The client only sees an
// unguessable loopback endpoint. Its supplied headers never replace credentials.
func startMCPProxy(c mcpconnect.Config, client *http.Client) (string, func(), error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	host := listener.Addr().String()
	path := "/" + hex.EncodeToString(token[:])
	endpoint := "http://" + host + path
	secrets := mcpconfig.SensitiveValues(mcpconfig.Servers{"upstream": {"headers": c.Headers, "url": c.URL}})
	slots := make(chan struct{}, 8)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10}
	client.Timeout = 100 * time.Second
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host || r.URL.Path != path || r.URL.RawQuery != "" || r.Header.Get("Origin") != "" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case "POST", "DELETE", "GET":
		default:
			w.WriteHeader(405)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.WriteHeader(429)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if err != nil || len(body) > 1<<20 {
			w.WriteHeader(413)
			return
		}
		up, err := http.NewRequestWithContext(r.Context(), r.Method, c.URL, bytes.NewReader(body))
		if err != nil {
			w.WriteHeader(502)
			return
		}
		for _, key := range []string{"Content-Type", "Accept", "Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-ID"} {
			if v := r.Header.Get(key); v != "" {
				up.Header.Set(key, v)
			}
		}
		for k, v := range c.Headers {
			up.Header.Set(k, v)
		}
		resp, err := client.Do(up)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if resp.StatusCode >= 400 && resp.StatusCode <= 599 {
				w.WriteHeader(resp.StatusCode)
			} else {
				w.WriteHeader(502)
			}
			return
		}
		if session := resp.Header.Get("Mcp-Session-Id"); session != "" {
			if len(session) > 512 || reflectsMCPSecret([]byte(session), secrets) {
				w.WriteHeader(502)
				return
			}
			w.Header().Set("Mcp-Session-Id", session)
		}
		media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		if resp.StatusCode == 202 || resp.StatusCode == 204 {
			w.WriteHeader(resp.StatusCode)
			return
		}
		if media != "application/json" && media != "text/event-stream" {
			w.WriteHeader(502)
			return
		}
		w.Header().Set("Content-Type", media)
		if media == "application/json" {
			b, e := io.ReadAll(io.LimitReader(resp.Body, mcpBodyLimit+1))
			if e != nil || len(b) > mcpBodyLimit || reflectsMCPSecret(b, secrets) {
				w.WriteHeader(502)
				return
			}
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(b)
			return
		}
		w.WriteHeader(resp.StatusCode)
		scan := bufio.NewScanner(io.LimitReader(resp.Body, mcpBodyLimit+1))
		scan.Buffer(make([]byte, 4096), mcpBodyLimit)
		for scan.Scan() {
			b := scan.Bytes()
			if len(b) > 0 && reflectsMCPSecret(b, secrets) {
				return
			}
			if _, e := w.Write(append(b, '\n')); e != nil {
				return
			}
			if len(b) == 0 {
				if e := http.NewResponseController(w).Flush(); e != nil {
					return
				}
			}
		}
	})
	go func() { _ = server.Serve(listener) }()
	return endpoint, func() { _ = server.Close(); client.CloseIdleConnections() }, nil
}

// With short settings such as lang=en, match standalone string values rather
// than breaking ordinary MCP responses containing "content" or JSON-RPC id 1.
func reflectsMCPSecret(body []byte, secrets []string) bool {
	if len(secrets) == 0 {
		return false
	}
	if bytes.ContainsRune(body, '\\') {
		// A JSON encoder may escape characters in a reflected URL or header.
		payload := bytes.TrimSpace(bytes.TrimPrefix(body, []byte("data:")))
		decoder := json.NewDecoder(bytes.NewReader(payload))
		for {
			token, err := decoder.Token()
			if err != nil {
				break
			}
			if s, ok := token.(string); ok {
				for _, secret := range secrets {
					if secret != "" && (s == secret || len(secret) >= 4 && strings.Contains(s, secret)) {
						return true
					}
				}
			}
		}
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if len(secret) >= 4 && bytes.Contains(body, []byte(secret)) || bytes.Equal(body, []byte(secret)) {
			return true
		}
		encoded, _ := json.Marshal(secret)
		if bytes.Contains(body, encoded) {
			return true
		}
	}
	return false
}
