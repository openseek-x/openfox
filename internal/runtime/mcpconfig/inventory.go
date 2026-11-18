// Package mcpconfig reads Runtime user configuration without starting an MCP server.
// Only Metadata can be included in the owner's device inventory.
package mcpconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"

	"openfox/internal/mcpconnect"
)

const (
	MaxConfigBytes = 1 << 20
	MaxServers     = 32
)

type Metadata struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Source    string `json:"source"`
	Enabled   bool   `json:"enabled"`
}
type Inventory struct {
	Status  string     `json:"status"`
	Servers []Metadata `json:"servers"`
}

type Location struct {
	Home string
	Env  func(string) string
}

func UserLocation() Location {
	home, _ := os.UserHomeDir()
	return Location{Home: home, Env: os.Getenv}
}

func (l Location) files(provider string) []string {
	dir := func(key, fallback string) string {
		if l.Env != nil {
			if d := l.Env(key); d != "" {
				return d
			}
		}
		return fallback
	}
	switch provider {
	case "codex":
		return []string{filepath.Join(dir("CODEX_HOME", filepath.Join(l.Home, ".codex")), "config.toml")}
	case "claude-code":
		return []string{filepath.Join(dir("CLAUDE_CONFIG_DIR", l.Home), ".claude.json")}
	case "pi":
		return []string{filepath.Join(dir("PI_CODING_AGENT_DIR", filepath.Join(l.Home, ".pi", "agent")), "mcp.json")}
	case "opencode":
		root := filepath.Join(dir("XDG_CONFIG_HOME", filepath.Join(l.Home, ".config")), "opencode")
		files := []string{filepath.Join(root, "config.json"), filepath.Join(root, "opencode.json"), filepath.Join(root, "opencode.jsonc")}
		if extra := dir("OPENCODE_CONFIG", ""); filepath.IsAbs(extra) {
			files = append(files, extra)
		}
		return files
	default:
		return nil
	}
}

func Read(provider string, loc Location) Inventory {
	entries, status := load(provider, loc)
	result := Inventory{Status: status, Servers: []Metadata{}}
	if status != "ok" {
		return result
	}
	for name, entry := range entries {
		if !mcpconnect.ValidName(name) || strings.EqualFold(name, "openfox") {
			continue
		}
		meta, ok := metadata(provider, name, entry)
		if !ok {
			return Inventory{Status: "invalid", Servers: []Metadata{}}
		}
		result.Servers = append(result.Servers, meta)
	}
	sort.Slice(result.Servers, func(i, j int) bool { return result.Servers[i].Name < result.Servers[j].Name })
	return result
}

func load(provider string, loc Location) (map[string]map[string]any, string) {
	files := loc.files(provider)
	if len(files) == 0 || !filepath.IsAbs(loc.Home) {
		return nil, "unavailable"
	}
	entries := make(map[string]map[string]any)
	found := false
	for _, path := range files {
		if !filepath.IsAbs(path) {
			return nil, "unavailable"
		}
		b, status := readRegular(path)
		if status == "missing" {
			continue
		}
		if status != "ok" {
			return nil, status
		}
		found = true
		var doc map[string]any
		var err error
		key := "mcpServers"
		if provider == "codex" {
			err = toml.Unmarshal(b, &doc)
			key = "mcp_servers"
		} else {
			if provider == "opencode" {
				key = "mcp"
				b = stripJSONComments(b)
			}
			err = json.Unmarshal(b, &doc)
		}
		if err != nil || doc == nil {
			return nil, "invalid"
		}
		servers, exists := doc[key]
		if !exists {
			continue
		}
		list, ok := servers.(map[string]any)
		if !ok {
			return nil, "invalid"
		}
		for name, value := range list {
			if !validName(name) {
				return nil, "invalid"
			}
			object, ok := value.(map[string]any)
			if !ok {
				return nil, "invalid"
			}
			existing := entries[name]
			if existing == nil {
				existing = make(map[string]any)
				entries[name] = existing
			}
			for k, v := range object {
				existing[k] = v
			}
		}
		if len(entries) > MaxServers {
			return nil, "too_large"
		}
	}
	if !found {
		return nil, "missing"
	}
	return entries, "ok"
}

func validName(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.IndexFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '-')
	}) < 0
}

func metadata(provider, name string, c map[string]any) (Metadata, bool) {
	m := Metadata{Name: name, Source: "USER_CONFIG", Enabled: true}
	if v, exists := c["enabled"]; exists {
		enabled, ok := v.(bool)
		if !ok {
			return m, false
		}
		m.Enabled = enabled
	}
	if v, exists := c["disabled"]; exists {
		disabled, ok := v.(bool)
		if !ok {
			return m, false
		}
		if disabled {
			m.Enabled = false
		}
	}
	var typ string
	if v, exists := c["type"]; exists {
		var ok bool
		typ, ok = v.(string)
		if !ok {
			return m, false
		}
	}
	switch typ {
	case "stdio", "local":
		m.Transport = "STDIO"
	case "http", "streamable-http", "streamable_http", "remote":
		m.Transport = "HTTP"
	case "sse":
		m.Transport = "SSE"
	case "":
		if _, ok := c["command"]; ok {
			m.Transport = "STDIO"
		} else if _, ok := c["url"]; ok {
			m.Transport = "HTTP"
		} else {
			return m, false
		}
	default:
		return m, false
	}
	return m, true
}

func readRegular(path string) ([]byte, string) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "missing"
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, "unavailable"
	}
	if info.Size() > MaxConfigBytes {
		return nil, "too_large"
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "unavailable"
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !os.SameFile(info, st) {
		return nil, "unavailable"
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil {
		return nil, "unavailable"
	}
	if len(b) > MaxConfigBytes {
		return nil, "too_large"
	}
	return b, "ok"
}

// OpenCode accepts JSONC comments and trailing commas. Quoted URLs and command
// arguments are left untouched. Unterminated comments remain invalid JSON.
func stripJSONComments(b []byte) []byte {
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '"':
			for i++; i < len(b); i++ {
				if b[i] == '\\' {
					i++
					continue
				}
				if b[i] == '"' {
					break
				}
			}
		case '/':
			if i+1 >= len(b) {
				continue
			}
			start := i
			switch b[i+1] {
			case '/':
				i += 2
				for i < len(b) && b[i] != '\n' {
					i++
				}
			case '*':
				end := bytes.Index(b[i+2:], []byte("*/"))
				if end < 0 {
					return nil
				}
				i += end + 4
			default:
				continue
			}
			for j := start; j < i; j++ {
				b[j] = ' '
			}
			i--
		}
	}
	inString := false
	for i := 0; i < len(b); i++ {
		if inString && b[i] == '\\' {
			i++
			continue
		}
		if b[i] == '"' {
			inString = !inString
			continue
		}
		if !inString && b[i] == ',' {
			next := bytes.TrimLeft(b[i+1:], " \r\t\n")
			if len(next) > 0 && (next[0] == ']' || next[0] == '}') {
				b[i] = ' '
			}
		}
	}
	return b
}

// OpenCodeModelSettings copies the model configuration which the CLI otherwise
// loses when its project and global MCP sources are isolated. The config file
// passed to the runtime contains only these keys and the selected MCP servers.
func OpenCodeModelSettings(loc Location) (map[string]any, error) {
	result := map[string]any{}
	if !filepath.IsAbs(loc.Home) {
		return nil, ErrUnavailable
	}
	merge := func(b []byte) error {
		var doc map[string]any
		if len(b) > MaxConfigBytes || json.Unmarshal(stripJSONComments(b), &doc) != nil {
			return ErrUnavailable
		}
		for _, key := range []string{"model", "small_model", "enabled_providers", "disabled_providers"} {
			if value, ok := doc[key]; ok {
				result[key] = value
			}
		}
		if providers, ok := doc["provider"].(map[string]any); ok {
			current, _ := result["provider"].(map[string]any)
			if current == nil {
				current = map[string]any{}
				result["provider"] = current
			}
			for name, provider := range providers {
				current[name] = provider
			}
		}
		return nil
	}
	for _, path := range loc.files("opencode") {
		b, status := readRegular(path)
		switch status {
		case "missing":
			continue
		case "ok":
			if err := merge(b); err != nil {
				return nil, err
			}
		default:
			return nil, ErrUnavailable
		}
	}
	if loc.Env != nil {
		if inline := loc.Env("OPENCODE_CONFIG_CONTENT"); inline != "" {
			if err := merge([]byte(inline)); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}
