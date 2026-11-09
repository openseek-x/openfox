// Package mcpconnect implements the validated Agent MCP configuration and public discovery protocol.
package mcpconnect

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"openfox/internal/platform/safehttp"
)

const Mask = "••••••"
const Public = "PUBLIC"
const DeviceOwner = "DEVICE_OWNER"

var ErrConfig = errors.New("invalid MCP configuration")
var namePattern = regexp.MustCompile("^[A-Za-z0-9_-]{1,64}$")
var customNamePattern = regexp.MustCompile("^[a-z0-9-]{1,64}$")
var envPattern = regexp.MustCompile("^[A-Za-z_][A-Za-z0-9_]{0,127}$")
var headerPattern = regexp.MustCompile("^[A-Za-z][A-Za-z0-9-]{0,126}$")

func ValidName(name string) bool {
	return namePattern.MatchString(name) && !strings.EqualFold(name, "openfox")
}

type Config struct {
	Name       string            `json:"name"`
	CatalogID  string            `json:"catalog_id,omitempty"`
	Transport  string            `json:"transport"`
	Scope      string            `json:"scope,omitempty"`
	URL        string            `json:"url,omitempty"`
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	AuthMode   string            `json:"auth_mode,omitempty"`
	AuthHeader string            `json:"auth_header,omitempty"`
	Credential string            `json:"credential,omitempty"`
}

type RunServer struct {
	CatalogID   string            `json:"catalog_id,omitempty"`
	ServerID    string            `json:"server_id,omitempty"`
	Name        string            `json:"name"`
	Transport   string            `json:"transport"`
	Scope       string            `json:"scope"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	DeviceID    string            `json:"device_id,omitempty"`
	OwnerUserID string            `json:"owner_user_id,omitempty"`
}

func validText(s string, max int) bool {
	return len(s) <= max && strings.IndexFunc(s, unicode.IsControl) < 0
}
func validHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Host", "Content-Length", "Content-Type", "Transfer-Encoding", "Connection", "Upgrade", "Expect", "Accept", "Accept-Encoding", "Origin", "Cookie", "Set-Cookie", "Te", "Trailer", "Forwarded":
		return false
	}
	n := strings.ToLower(name)
	return headerPattern.MatchString(name) && !strings.HasPrefix(n, "proxy-") && !strings.HasPrefix(n, "mcp-") && !strings.HasPrefix(n, "x-forwarded-")
}

func (c Config) Normalize() (Config, error) {
	c.Name = strings.TrimSpace(c.Name)
	c.Transport = strings.ToLower(c.Transport)
	if c.AuthMode == "" {
		c.AuthMode = "none"
	}
	if c.Scope == "" {
		c.Scope = Public
		if c.Transport == "stdio" {
			c.Scope = DeviceOwner
		}
	}
	if c.Name == "openfox" || !customNamePattern.MatchString(c.Name) || (c.CatalogID != "" && !namePattern.MatchString(c.CatalogID)) || (c.Scope != Public && c.Scope != DeviceOwner) || len(c.Headers) > 32 || len(c.Env) > 32 || len(c.Args) > 64 {
		return Config{}, ErrConfig
	}
	switch c.Transport {
	case "http":
		if c.URL == "" || len(c.URL) > 2048 || c.Command != "" || len(c.Args)+len(c.Env) > 0 {
			return Config{}, ErrConfig
		}
		u, e := url.Parse(c.URL)
		if e != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.RawQuery != "" && (c.AuthMode == "oauth" || u.Scheme != "https")) || u.ForceQuery || u.Opaque != "" || (u.Scheme != "https" && !(u.Scheme == "http" && c.Scope == DeviceOwner)) {
			return Config{}, ErrConfig
		}
		if c.Scope == Public {
			if _, err := safehttp.CheckURL(c.URL); err != nil {
				return Config{}, ErrConfig
			}
		}
	case "stdio":
		if c.Scope != DeviceOwner || c.URL != "" || c.AuthMode != "none" || len(c.Headers) > 0 || c.Command == "" || !validText(c.Command, 1024) || strings.TrimSpace(c.Command) != c.Command {
			return Config{}, ErrConfig
		}
	default:
		return Config{}, ErrConfig
	}
	for _, v := range c.Args {
		if !validText(v, 1024) {
			return Config{}, ErrConfig
		}
	}
	for k, v := range c.Env {
		if !envPattern.MatchString(k) || !validText(v, 4096) {
			return Config{}, ErrConfig
		}
	}
	normalized := make(map[string]string, len(c.Headers))
	for k, v := range c.Headers {
		key := http.CanonicalHeaderKey(k)
		if !validHeader(k) || !validText(v, 4096) {
			return Config{}, ErrConfig
		}
		if _, ok := normalized[key]; ok {
			return Config{}, ErrConfig
		}
		normalized[key] = v
	}
	c.Headers = normalized
	switch c.AuthMode {
	case "none":
		if c.AuthHeader != "" || c.Credential != "" {
			return Config{}, ErrConfig
		}
	case "api_key", "oauth":
		if c.AuthHeader == "" {
			c.AuthHeader = "Authorization"
		}
		c.AuthHeader = http.CanonicalHeaderKey(c.AuthHeader)
		if !validHeader(c.AuthHeader) || !validText(c.Credential, 4096) || (c.AuthMode == "oauth" && (c.Scope != Public || c.AuthHeader != "Authorization")) {
			return Config{}, ErrConfig
		}
		if _, exists := c.Headers[c.AuthHeader]; exists {
			return Config{}, ErrConfig
		}
	default:
		return Config{}, ErrConfig
	}
	encoded, _ := json.Marshal(c)
	if len(encoded) > 8192 {
		return Config{}, ErrConfig
	}
	return c, nil
}

// All query parameters are kept together: even parameter names and order can
// be part of a provider-signed URL. Never copy them to the public metadata.
func maskedURL(raw string) string {
	base, _, found := strings.Cut(raw, "?")
	if found {
		return base + "?" + Mask
	}
	return raw
}
func (c Config) Redacted() Config {
	c.URL = maskedURL(c.URL)
	if c.Credential != "" {
		c.Credential = Mask
	}
	c.Env = maskMap(c.Env)
	c.Headers = maskMap(c.Headers)
	c.Args = slices.Clone(c.Args)
	for i := range c.Args {
		c.Args[i] = Mask
	}
	return c
}

func maskMap(input map[string]string) map[string]string {
	masked := make(map[string]string, len(input))
	for k := range input {
		masked[k] = Mask
	}
	return masked
}

// Merge masked fields only for the exact same authority and launch target. A changed
// argv could redirect a command's network credentials as easily as a changed URL.
func (c Config) Merge(previous Config) (Config, error) {
	if base, query, found := strings.Cut(c.URL, "?"); found && strings.Contains(query, Mask) {
		if query != Mask || c.URL != maskedURL(previous.URL) || c.Transport != previous.Transport || c.Scope != previous.Scope || c.AuthMode != previous.AuthMode || c.CatalogID != previous.CatalogID || !strings.HasPrefix(previous.URL, base+"?") {
			return Config{}, ErrConfig
		}
		c.URL = previous.URL
	}
	same := c.Transport == previous.Transport && c.Scope == previous.Scope && c.URL == previous.URL && c.Command == previous.Command && c.AuthHeader == previous.AuthHeader && c.AuthMode == previous.AuthMode && c.CatalogID == previous.CatalogID
	maskedArgs := slices.Contains(c.Args, Mask)
	c.Args = slices.Clone(c.Args)
	for i, v := range c.Args {
		if v == Mask {
			if !same || i >= len(previous.Args) {
				return Config{}, ErrConfig
			}
			c.Args[i] = previous.Args[i]
		}
	}
	same = same && slices.Equal(c.Args, previous.Args)
	if !same && maskedArgs {
		return Config{}, ErrConfig
	}
	replace := func(input, old map[string]string) (map[string]string, error) {
		r := make(map[string]string, len(input))
		for k, v := range input {
			if v == Mask {
				o, ok := old[k]
				if !same || !ok {
					return nil, ErrConfig
				}
				v = o
			}
			r[k] = v
		}
		return r, nil
	}
	var err error
	if c.Env, err = replace(c.Env, previous.Env); err != nil {
		return Config{}, err
	}
	if c.Headers, err = replace(c.Headers, previous.Headers); err != nil {
		return Config{}, err
	}
	if c.Credential == Mask {
		if !same || previous.Credential == "" {
			return Config{}, ErrConfig
		}
		c.Credential = previous.Credential
	}
	if c.AuthMode == "api_key" && c.Credential == "" {
		return Config{}, ErrConfig
	}
	if err := c.validateProviderCLI(); err != nil {
		return Config{}, err
	}
	return c.Normalize()
}

func (c Config) RequestHeaders() map[string]string {
	h := make(map[string]string, len(c.Headers)+1)
	for k, v := range c.Headers {
		h[k] = v
	}
	if c.Credential != "" {
		v := c.Credential
		if c.AuthHeader == "Authorization" {
			v = "Bearer " + v
		}
		h[c.AuthHeader] = v
	}
	return h
}

func (c Config) validateProviderCLI() error {
	var expected string
	switch c.CatalogID {
	case "feishu", "apifox":
		expected = c.CatalogID
	case "cnb-api":
		expected = "cnb"
	}
	if expected == "" && c.Command != "openfox:provider-cli" {
		return nil
	}
	if expected != "" && c.Scope == DeviceOwner && c.Transport == "stdio" && c.Command == "openfox:provider-cli" && len(c.Args) == 1 && c.Args[0] == expected && len(c.Env)+len(c.Headers) == 0 && c.AuthMode == "none" {
		return nil
	}
	return ErrConfig
}
