package mcpconfig

import (
	"errors"
	"net/url"
	"strings"
)

// Servers holds only the explicitly selected native entries for this Run.
type Servers map[string]map[string]any

var ErrUnavailable = errors.New("selected MCP server configuration is unavailable")

func Native(provider string, command string, args []string, env map[string]string, endpoint string) map[string]any {
	entry := map[string]any{}
	if endpoint != "" {
		entry["url"] = endpoint
		switch provider {
		case "claude-code":
			entry["type"] = "http"
		case "opencode":
			entry["type"], entry["oauth"] = "remote", false
		case "pi":
			entry["exposure"] = "direct"
		}
	} else if provider == "opencode" {
		entry["type"], entry["command"] = "local", append([]string{command}, args...)
		if len(env) > 0 {
			entry["environment"] = env
		}
	} else {
		entry["command"] = command
		if len(args) > 0 {
			entry["args"] = args
		}
		if len(env) > 0 {
			entry["env"] = env
		}
		if provider == "pi" {
			entry["exposure"] = "direct"
		}
	}
	if provider == "codex" {
		entry["required"] = true
		entry["default_tools_approval_mode"] = "approve"
	}
	return entry
}

func Inherited(provider, name string, loc Location) (map[string]any, error) {
	entries, status := load(provider, loc)
	entry, found := entries[name]
	if status != "ok" || !found {
		return nil, ErrUnavailable
	}
	meta, ok := metadata(provider, name, entry)
	if !ok || !meta.Enabled {
		return nil, ErrUnavailable
	}
	if provider == "codex" {
		if err := resolveCodexEnv(entry, loc); err != nil {
			return nil, err
		}
		entry["required"] = true
		if _, configured := entry["default_tools_approval_mode"]; !configured {
			entry["default_tools_approval_mode"] = "approve"
		}
	}
	return entry, nil
}

// SensitiveValues prevents native error output or tool results from leaking
// configured args, HTTP headers, credentials, or command environment values.
func SensitiveValues(entries Servers) []string {
	var values []string
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case string:
			if x != "" {
				values = append(values, x)
				if _, token, ok := strings.Cut(x, " "); ok && len(token) >= 8 {
					values = append(values, token)
				}
			}
		case map[string]any:
			for _, e := range x {
				visit(e)
			}
		case map[string]string:
			for _, e := range x {
				visit(e)
			}
		case []string:
			for _, e := range x {
				visit(e)
			}
		case []any:
			for _, e := range x {
				visit(e)
			}
		}
	}
	for _, e := range entries {
		if raw, ok := e["url"].(string); ok {
			if parsed, err := url.Parse(raw); err == nil && parsed.RawQuery != "" {
				visit(parsed.RawQuery)
				for key, pairs := range parsed.Query() {
					key = strings.ToLower(key)
					credential := strings.Contains(key, "key") || strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "auth") || strings.Contains(key, "pass") || strings.Contains(key, "sig")
					for _, v := range pairs {
						if len(v) < 4 && !credential {
							continue
						}
						visit(v)
						if escaped := url.QueryEscape(v); escaped != v {
							visit(escaped)
						}
					}
				}
			}
		}
		for _, k := range []string{"url", "command", "args", "env", "environment", "headers", "http_headers", "oauth", "auth"} {
			visit(e[k])
		}
	}
	return values
}

// Codex runs with an isolated environment. Resolve only variable names
// explicitly requested by the selected personal server, into that entry.
func resolveCodexEnv(entry map[string]any, loc Location) error {
	lookup := func(key string) (string, error) {
		if key == "" || loc.Env == nil {
			return "", ErrUnavailable
		}
		v := loc.Env(key)
		if v == "" {
			return "", ErrUnavailable
		}
		return v, nil
	}
	if raw, ok := entry["env_vars"]; ok {
		keys, ok := raw.([]any)
		if !ok {
			return ErrUnavailable
		}
		values, _ := entry["env"].(map[string]any)
		if values == nil {
			values = map[string]any{}
		}
		for _, k := range keys {
			key, ok := k.(string)
			if !ok {
				return ErrUnavailable
			}
			v, e := lookup(key)
			if e != nil {
				return e
			}
			if _, set := values[key]; !set {
				values[key] = v
			}
		}
		entry["env"] = values
		delete(entry, "env_vars")
	}
	headers, _ := entry["http_headers"].(map[string]any)
	if headers == nil {
		headers = map[string]any{}
	}
	if raw, ok := entry["env_http_headers"]; ok {
		names, ok := raw.(map[string]any)
		if !ok {
			return ErrUnavailable
		}
		for k, key := range names {
			source, ok := key.(string)
			if !ok {
				return ErrUnavailable
			}
			v, e := lookup(source)
			if e != nil {
				return e
			}
			headers[k] = v
		}
		delete(entry, "env_http_headers")
	}
	if raw, ok := entry["bearer_token_env_var"]; ok {
		source, ok := raw.(string)
		if !ok {
			return ErrUnavailable
		}
		v, e := lookup(source)
		if e != nil {
			return e
		}
		headers["Authorization"] = "Bearer " + v
		delete(entry, "bearer_token_env_var")
	}
	if len(headers) > 0 {
		entry["http_headers"] = headers
	}
	return nil
}
