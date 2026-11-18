package mcpconnect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"openfox/internal/platform/safehttp"
)

var ErrDiscovery = errors.New("MCP server discovery failed")
var ErrUnauthorized = errors.New("MCP server requires authorization")

const maxFrame = 512 << 10

type Tool struct {
	ReadOnly    bool            `json:"read_only,omitempty"`
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
}
type Manifest struct {
	Tools     []Tool     `json:"tools"`
	Resources []Resource `json:"resources"`
}

func EmptyManifest() Manifest { return Manifest{[]Tool{}, []Resource{}} }

func PublicClient(opts safehttp.Options) *http.Client {
	c := safehttp.Client(opts)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

type clientSession struct {
	ctx      context.Context
	client   *http.Client
	config   Config
	session  string
	protocol string
}
type response struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

func (s *clientSession) request(id int, method string, params any, out any) error {
	body := map[string]any{"jsonrpc": "2.0", "method": method}
	if id > 0 {
		body["id"] = id
	}
	if params != nil {
		body["params"] = params
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(s.ctx, "POST", s.config.URL, bytes.NewReader(raw))
	if err != nil {
		return ErrConfig
	}
	for k, v := range s.config.RequestHeaders() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if s.protocol != "" {
		req.Header.Set("MCP-Protocol-Version", s.protocol)
	}
	if s.session != "" {
		req.Header.Set("Mcp-Session-Id", s.session)
	}
	res, err := s.client.Do(req)
	if errors.Is(err, safehttp.ErrBlockedAddress) {
		return safehttp.ErrBlockedAddress
	}
	if err != nil {
		return ErrDiscovery
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return ErrUnauthorized
	}
	if id == 0 {
		if res.StatusCode != 202 {
			return ErrDiscovery
		}
		return nil
	}
	if res.StatusCode != 200 {
		return ErrDiscovery
	}
	if id == 1 {
		s.session = res.Header.Get("MCP-Session-Id")
		if len(s.session) > 1024 || strings.IndexFunc(s.session, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
			return ErrDiscovery
		}
	}
	med, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	match := func(b []byte) (bool, error) {
		var reply response
		if json.Unmarshal(b, &reply) != nil || reply.Version != "2.0" {
			return false, ErrDiscovery
		}
		if string(reply.ID) != strconv.Itoa(id) {
			return false, nil
		}
		if len(reply.Error) > 0 || len(reply.Result) == 0 || json.Unmarshal(reply.Result, out) != nil {
			return false, ErrDiscovery
		}
		return true, nil
	}
	switch med {
	case "application/json":
		b, e := io.ReadAll(io.LimitReader(res.Body, maxFrame+1))
		if e != nil || len(b) > maxFrame {
			return ErrDiscovery
		}
		ok, e := match(b)
		if !ok || e != nil {
			return ErrDiscovery
		}
		return nil
	case "text/event-stream":
		scanner := bufio.NewScanner(io.LimitReader(res.Body, maxFrame+1))
		scanner.Buffer(make([]byte, 4096), maxFrame)
		var data []string
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" && len(data) > 0 {
				ok, e := match([]byte(strings.Join(data, "\n")))
				if e != nil {
					return e
				}
				if ok {
					return nil
				}
				data = nil
			} else if v, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimPrefix(v, " "))
			}
		}
	}
	return ErrDiscovery
}

func Discover(ctx context.Context, opts safehttp.Options, cfg Config) (Manifest, error) {
	var err error
	cfg, err = cfg.Normalize()
	if err != nil || cfg.Scope != Public || cfg.Transport != "http" {
		return Manifest{}, ErrConfig
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	s := clientSession{ctx: ctx, client: PublicClient(opts), config: cfg}
	var init struct {
		Protocol     string                     `json:"protocolVersion"`
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	err = s.request(1, "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "OpenFox", "version": "1"}}, &init)
	if err != nil {
		return Manifest{}, err
	}
	switch init.Protocol {
	case "2025-11-25", "2025-06-18", "2025-03-26":
		s.protocol = init.Protocol
	default:
		return Manifest{}, ErrDiscovery
	}
	if err = s.request(0, "notifications/initialized", nil, nil); err != nil {
		return Manifest{}, err
	}
	manifest := EmptyManifest()
	id := 2
	for _, kind := range []string{"tools", "resources"} {
		if _, ok := init.Capabilities[kind]; !ok {
			continue
		}
		cursor := ""
		seen := map[string]bool{}
		names := map[string]bool{}
		for i := 0; i < 8; i++ {
			var params map[string]string
			if cursor != "" {
				params = map[string]string{"cursor": cursor}
			} else {
				params = map[string]string{}
			}
			var page struct {
				Tools []struct {
					Name, Title, Description string
					Annotations              struct {
						ReadOnly bool `json:"readOnlyHint"`
					} `json:"annotations"`
					Schema json.RawMessage `json:"inputSchema"`
				} `json:"tools"`
				Resources []struct{ URI, Name, Title, Description, MimeType string } `json:"resources"`
				Next      string                                                     `json:"nextCursor"`
			}
			if err = s.request(id, kind+"/list", params, &page); err != nil {
				return Manifest{}, err
			}
			id++
			if (kind == "tools" && page.Tools == nil) || (kind == "resources" && page.Resources == nil) {
				return Manifest{}, ErrDiscovery
			}
			if kind == "tools" {
				for _, t := range page.Tools {
					var schema struct{ Type string }
					if t.Name == "" || len(t.Name) > 128 || names[t.Name] || json.Unmarshal(t.Schema, &schema) != nil || schema.Type != "object" || len(t.Schema) > 32768 || len(t.Description) > 8192 || len(t.Title) > 512 {
						return Manifest{}, ErrDiscovery
					}
					names[t.Name] = true
					manifest.Tools = append(manifest.Tools, Tool{Name: t.Name, Title: t.Title, Description: t.Description, InputSchema: t.Schema, ReadOnly: t.Annotations.ReadOnly})
				}
			}
			if kind == "resources" {
				for _, r := range page.Resources {
					if r.URI == "" || len(r.URI) > 2048 || names[r.URI] || r.Name == "" || len(r.Name) > 256 || len(r.Description) > 8192 || len(r.Title) > 512 || len(r.MimeType) > 256 {
						return Manifest{}, ErrDiscovery
					}
					names[r.URI] = true
					manifest.Resources = append(manifest.Resources, Resource{r.URI, r.Name, r.Title, r.Description, r.MimeType})
				}
			}
			b, _ := json.Marshal(manifest)
			if len(manifest.Tools) > 256 || len(manifest.Resources) > 256 || len(b) > maxFrame {
				return Manifest{}, ErrDiscovery
			}
			if page.Next == "" {
				break
			}
			if i == 7 || seen[page.Next] || len(page.Next) > 2048 {
				return Manifest{}, ErrDiscovery
			}
			cursor = page.Next
			seen[cursor] = true
		}
	}
	if cfg.exposesSecret(manifest) {
		return Manifest{}, ErrDiscovery
	}
	return manifest, nil
}
