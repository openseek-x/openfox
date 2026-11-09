package mcpconnect

import (
	"strings"
	"testing"

	"openfox/internal/secrets"
)

func TestCredentialURLIsSealedAndBound(t *testing.T) {
	const url = "https://example.test/mcp?key=not-real&signature=synthetic"
	c, e := (Config{Name: "maps", Transport: "http", URL: url}).Normalize()
	if e != nil {
		t.Fatal(e)
	}
	m := c.Redacted()
	if strings.Contains(m.URL, "key") || strings.Contains(m.URL, "synthetic") {
		t.Fatal("URL exposed")
	}
	b, _ := secrets.New(make([]byte, 32))
	s, e := Seal(b, "org", "agent", "id", c)
	if e != nil || s == nil {
		t.Fatal("URL not sealed")
	}
	if _, e = m.Merge(Config{}); e == nil {
		t.Fatal("new connection accepted a mask")
	}
	if v, e := m.Merge(c); e != nil || v.URL != url {
		t.Fatal("unchanged target lost query")
	}
	m.URL = "https://other.test/mcp?" + Mask
	if _, e = m.Merge(c); e == nil {
		t.Fatal("transferred query to other domain")
	}
	m = c.Redacted()
	m.URL = "https://example.test/mcp"
	if v, e := m.Merge(c); e != nil || strings.Contains(v.URL, "?") {
		t.Fatal("could not clear old query")
	}
	if !c.exposesSecret(Manifest{Resources: []Resource{{Name: "resource", URI: "https://cdn.example.test/?signature=synthetic"}}}) {
		t.Fatal("discovery persisted echoed signature")
	}
	c.AuthMode = "oauth"
	if _, e = c.Normalize(); e == nil {
		t.Fatal("query secret could reach issuer")
	}
}
