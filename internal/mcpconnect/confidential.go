package mcpconnect

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
)

// An upstream can echo tokens in its resource URIs and tool schemas. A public
// manifest is shared with Agent readers, so refuse that manifest before storage.
func (c Config) exposesSecret(m Manifest) bool {
	values := []string{c.Credential}
	for _, h := range c.Headers {
		values = append(values, h)
		if _, v, ok := strings.Cut(h, " "); ok {
			values = append(values, v)
		}
	}
	if u, err := url.Parse(c.URL); err == nil && u.RawQuery != "" {
		values = append(values, u.RawQuery)
		for _, v := range u.Query() {
			for _, s := range v {
				values = append(values, s, url.QueryEscape(s))
			}
		}
	}
	if len(values) == 1 && values[0] == "" {
		return false
	}
	payload, _ := json.Marshal(m)
	for _, s := range values {
		if s == "" {
			continue
		}
		q, _ := json.Marshal(s)
		// Short ordinary parameter values must not match every character in a schema.
		if len(s) < 5 {
			if bytes.Contains(payload, q) {
				return true
			}
			continue
		}
		if bytes.Contains(payload, q[1:len(q)-1]) {
			return true
		}
	}
	return false
}
